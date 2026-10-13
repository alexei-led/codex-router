#!/usr/bin/env python3
"""Recording Responses proxy for the codex-router spikes (stdlib only).

Forwards /v1/* to the backend, streams SSE back, and records request shapes.
Header VALUES are never logged or recorded; only names. Body text is reduced to
the first 60 characters of the last user text.

  uv run scripts/spikes/shape-proxy.py --port 47911 --record shapes.jsonl
  uv run scripts/spikes/shape-proxy.py --summarize shapes.jsonl
"""

import argparse
import hashlib
import http.client
import json
import os
import sys
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

DEFAULT_UPSTREAM = "https://chatgpt.com/backend-api/codex"
HOP_BY_HOP = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailers",
    "transfer-encoding",
    "upgrade",
}
DROP_REQUEST = HOP_BY_HOP | {"host", "content-length", "accept-encoding"}
DROP_RESPONSE = HOP_BY_HOP | {
    "content-length",
    "content-encoding",
    "openai-model",
    "x-openai-model",
}
FAULTS = ("none", "before-bind", "before-upstream", "after-first-item")
TEXT_HEAD = 60

LOCK = threading.Lock()
STATE = {"seq": 0, "faults_used": 0, "prev_items": None, "dropped": False}
START = time.monotonic()
ARGS = argparse.Namespace()


def stamp():
    return round(time.monotonic() - START, 3)


def log(line):
    text = "t=%.3f %s" % (stamp(), line)
    with LOCK:
        if ARGS.log:
            with open(ARGS.log, "a") as fh:
                fh.write(text + "\n")
        else:
            print(text, file=sys.stderr, flush=True)


def head(text):
    return " ".join(str(text).split())[:TEXT_HEAD]


def item_label(item):
    if not isinstance(item, dict):
        return type(item).__name__
    kind = item.get("type") or "message"
    role = item.get("role")
    return "%s/%s" % (kind, role) if role else kind


def last_user_text(items):
    for item in reversed(items):
        if isinstance(item, dict) and item.get("role") == "user":
            content = item.get("content")
            if isinstance(content, str):
                return head(content)
            for part in reversed(content or []):
                if isinstance(part, dict) and isinstance(part.get("text"), str):
                    return head(part["text"])
    return ""


def turn_metadata(raw):
    try:
        meta = json.loads(raw)
    except (TypeError, ValueError):
        return None
    return meta if isinstance(meta, dict) else None


def compaction_hints(header_names, body, labels):
    hints = []
    if any("compact" in n for n in header_names):
        hints.append("header")
    if isinstance(body, dict):
        hints += [
            "body-key:" + k
            for k in body
            if "compact" in k.lower() or k == "context_management"
        ]
    hints += [
        "item:" + label for label in sorted(set(labels)) if "compact" in label.lower()
    ]
    return hints


def describe_request(handler, raw, body):
    names = sorted({k.lower() for k in handler.headers.keys()})
    meta = turn_metadata(handler.headers.get("x-codex-turn-metadata"))
    items = body.get("input") if isinstance(body, dict) else None
    items = items if isinstance(items, list) else []
    labels = [item_label(i) for i in items]
    reasoning = body.get("reasoning") if isinstance(body, dict) else None
    return {
        "method": handler.command,
        "path": urllib.parse.urlsplit(handler.path).path,
        "header_names": names,
        "subagent_header": "x-openai-subagent" in names,
        "turn_metadata_keys": sorted(meta) if meta is not None else None,
        "turn_id_present": bool(meta and meta.get("turn_id")),
        "content_encoding": handler.headers.get("content-encoding") is not None,
        "body_bytes": len(raw),
        "body_sha256": hashlib.sha256(raw).hexdigest(),
        "body_keys": sorted(body) if isinstance(body, dict) else None,
        "model": body.get("model") if isinstance(body, dict) else None,
        "effort": reasoning.get("effort") if isinstance(reasoning, dict) else None,
        "item_count": len(items),
        "item_types": labels,
        "compaction_hints": compaction_hints(names, body, labels),
        "last_user_text": last_user_text(items),
    }


def parse_rewrite(spec):
    out = {}
    for pair in filter(None, (spec or "").split(",")):
        key, _, value = pair.partition("=")
        if key not in ("model", "effort") or not value:
            sys.exit("bad --rewrite entry %r (use model=...,effort=...)" % pair)
        out[key] = value
    return out


def apply_rewrite(body, rewrite):
    if "model" in rewrite:
        body["model"] = rewrite["model"]
    if "effort" in rewrite:
        reasoning = body.get("reasoning")
        if not isinstance(reasoning, dict):
            reasoning = body["reasoning"] = {}
        reasoning["effort"] = rewrite["effort"]


def take_fault(path):
    if ARGS.fault in ("none", "before-bind") or not path.endswith("/responses"):
        return False
    with LOCK:
        if ARGS.fault_count and STATE["faults_used"] >= ARGS.fault_count:
            return False
        STATE["faults_used"] += 1
        return True


def sse_events(buf):
    while b"\n\n" in buf:
        block, buf = buf.split(b"\n\n", 1)
        yield block, buf


class Handler(BaseHTTPRequestHandler):
    server_version = "shape-proxy"

    def log_message(self, format, *args):
        pass

    def do_GET(self):
        self.handle_request()

    def do_POST(self):
        self.handle_request()

    def reply(self, status, text):
        data = text.encode()
        self.send_response(status)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def handle_request(self):
        started = time.monotonic()
        path = urllib.parse.urlsplit(self.path).path
        raw = b""
        if self.command == "POST":
            raw = self.rfile.read(int(self.headers.get("content-length") or 0))
        try:
            body = (
                json.loads(raw)
                if raw and not self.headers.get("content-encoding")
                else None
            )
        except ValueError:
            body = None
        record = describe_request(self, raw, body)
        with LOCK:
            STATE["seq"] += 1
            record["seq"] = STATE["seq"]
        record["case"] = ARGS.case
        record["t_start"] = stamp()
        record["fault"] = None
        record["rewritten"] = None
        record["status"] = None
        record["usage"] = None
        record["response_status"] = None
        record["events"] = {}
        try:
            if not path.startswith("/v1/"):
                record["status"] = 404
                self.reply(404, "not found")
                return
            if isinstance(body, dict) and path.endswith("/responses"):
                self.maybe_rewrite(body, record)
                if record["rewritten"]:
                    raw = json.dumps(body, separators=(",", ":")).encode()
            if ARGS.fault == "before-upstream" and take_fault(path):
                record["fault"] = ARGS.fault
                record["status"] = 502
                self.reply(502, "injected fault")
                return
            self.forward(path, raw, record)
        except (BrokenPipeError, ConnectionResetError) as exc:
            record["aborted"] = type(exc).__name__
        except Exception as exc:  # spike tool: report the class, keep serving
            record["aborted"] = type(exc).__name__
        finally:
            record["duration_ms"] = int((time.monotonic() - started) * 1000)
            self.write_record(record)

    def maybe_rewrite(self, body, record):
        if not ARGS.rewrite:
            return
        items = body.get("input") if isinstance(body.get("input"), list) else []
        with LOCK:
            prev = STATE["prev_items"]
            if prev is not None and len(items) < prev:
                STATE["dropped"] = True
            STATE["prev_items"] = len(items)
            active = STATE["dropped"] or not ARGS.rewrite_after_drop
        if active:
            apply_rewrite(body, ARGS.rewrite)
            record["rewritten"] = dict(ARGS.rewrite)

    def forward(self, path, raw, record):
        base = urllib.parse.urlsplit(ARGS.upstream)
        target = base.path.rstrip("/") + path[len("/v1") :]
        query = urllib.parse.urlsplit(self.path).query
        if query:
            target += "?" + query
        conn_cls = (
            http.client.HTTPSConnection
            if base.scheme == "https"
            else http.client.HTTPConnection
        )
        conn = conn_cls(base.netloc, timeout=ARGS.upstream_timeout)
        headers = {
            k: v for k, v in self.headers.items() if k.lower() not in DROP_REQUEST
        }
        headers["Accept-Encoding"] = "identity"
        conn.request(
            self.command,
            target,
            body=raw if self.command == "POST" else None,
            headers=headers,
        )
        resp = conn.getresponse()
        record["status"] = resp.status
        self.send_response(resp.status)
        for key, value in resp.getheaders():
            if key.lower() not in DROP_RESPONSE:
                self.send_header(key, value)
        streaming = "text/event-stream" in (resp.getheader("content-type") or "")
        if not streaming:
            data = resp.read()
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
            return
        self.end_headers()
        self.relay_stream(resp, record, path)

    def relay_stream(self, resp, record, path):
        buf = b""
        fault_now = ARGS.fault == "after-first-item" and take_fault(path)
        while True:
            chunk = resp.read1(65536)
            if not chunk:
                break
            buf += chunk
            for block, buf in sse_events(buf):
                kind = self.note_event(block, record)
                self.wfile.write(block + b"\n\n")
                self.wfile.flush()
                if fault_now and kind.startswith("response.output_item"):
                    record["fault"] = ARGS.fault
                    self.close_connection = True
                    self.connection.shutdown(2)
                    return
        if buf:
            self.wfile.write(buf)
            self.wfile.flush()

    def note_event(self, block, record):
        kind = ""
        data = None
        for line in block.split(b"\n"):
            if line.startswith(b"event:"):
                kind = line[6:].strip().decode(errors="replace")
            elif line.startswith(b"data:"):
                try:
                    data = json.loads(line[5:])
                except ValueError:
                    data = None
        if not kind and isinstance(data, dict):
            kind = str(data.get("type", ""))
        record["events"][kind] = record["events"].get(kind, 0) + 1
        if kind == "response.output_item.done" and "t_first_item" not in record:
            record["t_first_item"] = stamp()
        if kind in (
            "response.completed",
            "response.failed",
            "response.incomplete",
        ) and isinstance(data, dict):
            response = data.get("response") or {}
            record["response_status"] = response.get("status")
            usage = response.get("usage")
            record["usage"] = usage if isinstance(usage, dict) else None
        return kind

    def write_record(self, record):
        line = "case=%s seq=%d %s %s status=%s items=%d sha=%s ms=%d%s%s" % (
            record["case"],
            record["seq"],
            record["method"],
            record["path"],
            record["status"],
            record["item_count"],
            record["body_sha256"][:12],
            record["duration_ms"],
            " fault=" + record["fault"] if record["fault"] else "",
            " aborted=" + record["aborted"] if record.get("aborted") else "",
        )
        log(line)
        if ARGS.record:
            with LOCK, open(ARGS.record, "a") as fh:
                fh.write(json.dumps(record, sort_keys=True) + "\n")


def summarize(path):
    with open(path) as fh:
        rows = [json.loads(line) for line in fh if line.strip()]
    case = None
    for r in rows:
        if r.get("case") != case:
            case = r.get("case")
            print("== case %s" % case)
            print("   header names: %s" % ",".join(r["header_names"]))
            print("   turn-metadata keys: %s" % r["turn_metadata_keys"])
            print("   body keys: %s" % ",".join(r["body_keys"] or []))
        usage = r.get("usage") or {}
        tokens = usage.get("total_tokens", usage.get("input_tokens", "-"))
        print(
            "  #%d t=%.1f %s %s st=%s items=%d turn_id=%s sub=%s model=%s/%s rw=%s tok=%s sha=%s"
            % (
                r["seq"],
                r["t_start"],
                r["method"],
                r["path"].replace("/v1", ""),
                r["status"],
                r["item_count"],
                "y" if r["turn_id_present"] else "n",
                "y" if r["subagent_header"] else "n",
                r["model"],
                r["effort"],
                "y" if r["rewritten"] else "n",
                tokens,
                r["body_sha256"][:10],
            )
        )
        if r["fault"] or r.get("aborted"):
            print("      fault=%s aborted=%s" % (r["fault"], r.get("aborted")))
        if r["compaction_hints"]:
            print("      compaction: %s" % ",".join(r["compaction_hints"]))
        print(
            "      tail: %s | last user: %r"
            % (",".join(r["item_types"][-4:]), r["last_user_text"])
        )
    posts = [
        r for r in rows if r["method"] == "POST" and r["path"].endswith("/responses")
    ]
    print(
        "== /responses posts=%d distinct_bodies=%d"
        % (len(posts), len({r["body_sha256"] for r in posts}))
    )


def main():
    global ARGS
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    parser.add_argument(
        "--port", type=int, default=0, help="listen port on 127.0.0.1 (0 = ephemeral)"
    )
    parser.add_argument("--port-file", help="write the bound port here")
    parser.add_argument("--upstream", default=DEFAULT_UPSTREAM)
    parser.add_argument(
        "--rewrite", help="model=...,effort=... applied to /responses bodies"
    )
    parser.add_argument(
        "--rewrite-after-drop",
        action="store_true",
        help="apply --rewrite only after a request whose item count dropped (post-compaction)",
    )
    parser.add_argument("--fault", choices=FAULTS, default="none")
    parser.add_argument(
        "--fault-count",
        type=int,
        default=0,
        help="inject the fault this many times (0 = always)",
    )
    parser.add_argument("--record", help="append one JSON object per request here")
    parser.add_argument("--log", help="append log lines here instead of stderr")
    parser.add_argument("--case", default="", help="label stored in every record")
    parser.add_argument("--upstream-timeout", type=float, default=180)
    parser.add_argument(
        "--summarize", metavar="FILE", help="print a summary of a record file and exit"
    )
    ARGS = parser.parse_args()
    if ARGS.summarize:
        summarize(ARGS.summarize)
        return
    ARGS.rewrite = parse_rewrite(ARGS.rewrite)
    if ARGS.fault == "before-bind":
        log("fault=before-bind: not binding")
        return
    server = ThreadingHTTPServer(("127.0.0.1", ARGS.port), Handler)
    server.daemon_threads = True
    port = server.server_address[1]
    if ARGS.port_file:
        tmp = ARGS.port_file + ".tmp"
        with open(tmp, "w") as fh:
            fh.write(str(port))
        os.replace(tmp, ARGS.port_file)
    log(
        "listening 127.0.0.1:%d upstream=%s fault=%s rewrite=%s"
        % (port, ARGS.upstream, ARGS.fault, ARGS.rewrite or "none")
    )
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
