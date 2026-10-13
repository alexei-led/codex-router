#!/usr/bin/env python3
"""Offline checks for shape-proxy.py and appserver-client.py (called by selftest.sh).

Starts a fake upstream, drives the proxy through its modes, and runs the app-server
client against a fake newline JSON-RPC server. Prints PASS/FAIL lines only; the fake
secrets are never printed.
"""

import asyncio
import http.client
import json
import os
import subprocess
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

SECRET = "secret-XYZ"
ACCOUNT = "acct-secret-ABC"
TAIL = "TAIL-MARKER"

rt, proxy_py, client_py = sys.argv[1:4]
failures = []
seen = []


def check(name, ok):
    print(("PASS " if ok else "FAIL ") + name, flush=True)
    if not ok:
        failures.append(name)


EVENTS = [
    ("response.created", {"type": "response.created"}),
    (
        "response.output_item.done",
        {"type": "response.output_item.done", "item": {"type": "message"}},
    ),
    (
        "response.output_item.done",
        {"type": "response.output_item.done", "item": {"type": "message"}},
    ),
    (
        "response.completed",
        {
            "type": "response.completed",
            "response": {
                "status": "completed",
                "usage": {"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
            },
        },
    ),
]


class Upstream(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("content-length", 0))))
        seen.append(
            {
                "path": self.path,
                "model": body.get("model"),
                "effort": (body.get("reasoning") or {}).get("effort"),
                "auth_ok": self.headers.get("authorization") == "Bearer " + SECRET,
                "account_ok": self.headers.get("chatgpt-account-id") == ACCOUNT,
            }
        )
        self.send_response(200)
        self.send_header("content-type", "text/event-stream")
        self.send_header("openai-model", "gpt-x")
        self.send_header("x-openai-model", "gpt-x")
        self.end_headers()
        try:
            for kind, data in EVENTS:
                self.wfile.write(
                    ("event: %s\ndata: %s\n\n" % (kind, json.dumps(data))).encode()
                )
                self.wfile.flush()
                time.sleep(0.05)
        except BrokenPipeError:
            pass

    def do_GET(self):
        seen.append(
            {
                "path": self.path,
                "auth_ok": self.headers.get("authorization") == "Bearer " + SECRET,
            }
        )
        data = b'{"models":[]}'
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
upstream.daemon_threads = True
threading.Thread(target=upstream.serve_forever, daemon=True).start()
UPSTREAM_URL = "http://127.0.0.1:%d/base" % upstream.server_address[1]


class Proxy:
    def __init__(self, name, *extra):
        self.record = os.path.join(rt, name + ".jsonl")
        self.log = os.path.join(rt, name + ".proxy.log")
        port_file = os.path.join(rt, name + ".port")
        self.proc = subprocess.Popen(
            [
                sys.executable,
                "-I",
                proxy_py,
                "--port",
                "0",
                "--port-file",
                port_file,
                "--upstream",
                UPSTREAM_URL,
                "--record",
                self.record,
                "--log",
                self.log,
                "--case",
                name,
                *extra,
            ],
            stdout=open(os.path.join(rt, name + ".proxy.out"), "w"),
            stderr=open(os.path.join(rt, name + ".proxy.err"), "w"),
        )
        for _ in range(100):
            if os.path.exists(port_file):
                break
            time.sleep(0.05)
        self.port = int(open(port_file).read())

    def request(self, method, path, body=None):
        conn = http.client.HTTPConnection("127.0.0.1", self.port, timeout=15)
        headers = {
            "Authorization": "Bearer " + SECRET,
            "ChatGPT-Account-ID": ACCOUNT,
            "x-codex-turn-metadata": json.dumps(
                {
                    "session_id": "s",
                    "thread_id": "th",
                    "turn_id": "tu",
                    "agent_name": "a",
                }
            ),
        }
        conn.request(
            method,
            path,
            body=json.dumps(body) if body is not None else None,
            headers=headers,
        )
        resp = conn.getresponse()
        got = b""
        try:
            got = resp.read()
        except (http.client.IncompleteRead, ConnectionError):
            pass
        return (
            resp.status,
            {k.lower() for k, _ in resp.getheaders()},
            got.decode(errors="replace"),
        )

    def records(self):
        with open(self.record) as fh:
            return [json.loads(line) for line in fh if line.strip()]

    def stop(self):
        self.proc.terminate()
        self.proc.wait(timeout=5)


def responses_body(model="gpt-6-sol", extra_items=0):
    items = [
        {
            "type": "message",
            "role": "user",
            "content": [
                {"type": "input_text", "text": "hello " + "x" * 70 + " " + TAIL}
            ],
        }
    ]
    items += [{"type": "function_call", "name": "shell"}] * extra_items
    return {
        "model": model,
        "reasoning": {"effort": "medium"},
        "stream": True,
        "input": items,
    }


def test_passthrough_and_redaction():
    proxy = Proxy("pass")
    before = len(seen)
    status, headers, body = proxy.request("POST", "/v1/responses", responses_body())
    time.sleep(0.2)
    check(
        "forwards POST /v1/responses with status 200",
        status == 200 and "response.completed" in body,
    )
    check(
        "upstream received auth and account headers unchanged",
        seen[before]["auth_ok"] and seen[before]["account_ok"],
    )
    check(
        "upstream path is base + /responses", seen[before]["path"] == "/base/responses"
    )
    check(
        "body model untouched without --rewrite", seen[before]["model"] == "gpt-6-sol"
    )
    check(
        "openai-model headers stripped from the reply",
        not headers & {"openai-model", "x-openai-model"},
    )
    rec = proxy.records()[0]
    check(
        "record has turn_id and metadata keys",
        rec["turn_id_present"]
        and rec["turn_metadata_keys"]
        == ["agent_name", "session_id", "thread_id", "turn_id"],
    )
    check(
        "record lists header names",
        {"authorization", "chatgpt-account-id"} <= set(rec["header_names"]),
    )
    check(
        "record has item types and usage",
        rec["item_types"] == ["message/user"] and rec["usage"]["total_tokens"] == 15,
    )
    check(
        "last user text cut to 60 chars",
        rec["last_user_text"].startswith("hello") and len(rec["last_user_text"]) <= 60,
    )
    check(
        "text beyond 60 chars not recorded",
        TAIL not in open(proxy.record).read() and TAIL not in open(proxy.log).read(),
    )
    status, _, _ = proxy.request("GET", "/v1/models?client_version=1")
    check(
        "GET /v1/models forwarded",
        status == 200 and seen[-1]["path"] == "/base/models?client_version=1",
    )
    status, _, _ = proxy.request("POST", "/other", {})
    check("path outside /v1/ gets 404", status == 404)
    proxy.stop()
    return proxy


def test_rewrite():
    proxy = Proxy("rewrite", "--rewrite", "model=gpt-6-luna,effort=low")
    before = len(seen)
    proxy.request("POST", "/v1/responses", responses_body())
    time.sleep(0.2)
    check(
        "--rewrite sets model and effort",
        (seen[before]["model"], seen[before]["effort"]) == ("gpt-6-luna", "low"),
    )
    proxy.stop()
    proxy = Proxy(
        "rewrite-drop",
        "--rewrite",
        "model=gpt-6-luna,effort=low",
        "--rewrite-after-drop",
    )
    before = len(seen)
    proxy.request("POST", "/v1/responses", responses_body(extra_items=3))
    proxy.request("POST", "/v1/responses", responses_body(extra_items=3))
    proxy.request("POST", "/v1/responses", responses_body(extra_items=0))
    time.sleep(0.2)
    models = [s["model"] for s in seen[before:]]
    check(
        "--rewrite-after-drop waits for the item count to drop",
        models == ["gpt-6-sol", "gpt-6-sol", "gpt-6-luna"],
    )
    proxy.stop()


def test_faults():
    proxy = Proxy("fault-before", "--fault", "before-upstream", "--fault-count", "1")
    before = len(seen)
    status, _, _ = proxy.request("POST", "/v1/responses", responses_body())
    check(
        "before-upstream answers 502 without contacting upstream",
        status == 502 and len(seen) == before,
    )
    status, _, _ = proxy.request("POST", "/v1/responses", responses_body())
    check("fault-count 1 lets the retry through", status == 200)
    check(
        "same body hashes to the same fingerprint",
        len({r["body_sha256"] for r in proxy.records()}) == 1,
    )
    proxy.stop()
    proxy = Proxy("fault-after", "--fault", "after-first-item", "--fault-count", "1")
    status, _, body = proxy.request("POST", "/v1/responses", responses_body())
    check(
        "after-first-item closes the stream after one item",
        "output_item" in body and "response.completed" not in body,
    )
    time.sleep(0.2)
    check(
        "record marks the injected fault",
        proxy.records()[0]["fault"] == "after-first-item",
    )
    status, _, body = proxy.request("POST", "/v1/responses", responses_body())
    check(
        "next request completes normally",
        status == 200 and "response.completed" in body,
    )
    proxy.stop()
    port_file = os.path.join(rt, "fault-bind.port")
    code = subprocess.run(
        [
            sys.executable,
            "-I",
            proxy_py,
            "--fault",
            "before-bind",
            "--port-file",
            port_file,
            "--log",
            os.path.join(rt, "fault-bind.log"),
        ],
        timeout=10,
        capture_output=True,
    ).returncode
    check(
        "before-bind exits without binding", code == 0 and not os.path.exists(port_file)
    )


def test_summarize(proxy):
    out = subprocess.run(
        [sys.executable, "-I", proxy_py, "--summarize", proxy.record],
        capture_output=True,
        text=True,
        timeout=10,
    ).stdout
    open(os.path.join(rt, "summarize.out"), "w").write(out)
    check(
        "--summarize prints cases and counts",
        "== case pass" in out and "posts=1" in out,
    )


def fake_replies(msg):
    method, req_id = msg.get("method"), msg.get("id")
    if method == "thread/loaded/list":
        return [{"id": req_id, "result": {"data": ["thr-1"]}}]
    if method == "thread/resume":
        return [
            {"id": req_id, "result": {}},
            {"method": "turn/started", "params": {"turnId": "t1"}},
        ]
    if method == "thread/settings/update":
        p = msg["params"]
        return [
            {"id": req_id, "result": {}},
            {
                "method": "thread/settings/updated",
                "params": {
                    "threadId": "thr-1",
                    "settings": {"model": p["model"], "effort": p["effort"]},
                },
            },
        ]
    if req_id is not None:
        return [{"id": req_id, "result": {}}]
    return []


def run_client(sock, log):
    proc = subprocess.run(
        [
            sys.executable,
            "-I",
            client_py,
            "--sock",
            sock,
            "--log",
            log,
            "--update-delay",
            "0.2",
            "--listen-seconds",
            "1",
            "--thread-timeout",
            "5",
            "--turn-timeout",
            "5",
        ],
        timeout=40,
        capture_output=True,
    )
    return proc.returncode, open(log).read()


def check_client_run(label, transport, code, text):
    check(f"appserver-client {label} exits 0", code == 0)
    check(
        f"appserver-client {label} uses transport {transport}",
        f"event=transport kind={transport}" in text,
    )
    check(
        f"appserver-client {label} sends settings update after turn/started",
        "event=turn_started_seen" in text
        and "event=update_sent model=gpt-6-luna effort=low" in text,
    )
    check(
        f"appserver-client {label} logs settings/updated values",
        "method=thread/settings/updated model=gpt-6-luna effort=low" in text,
    )


def serve_in_thread(start):
    loop = asyncio.new_event_loop()
    ready = threading.Event()

    def serve():
        asyncio.set_event_loop(loop)
        loop.run_until_complete(start())
        ready.set()
        loop.run_forever()

    threading.Thread(target=serve, daemon=True).start()
    ready.wait(5)
    return loop


def test_appserver_client_lines():
    sock = os.path.join(rt, "fake-app.sock")

    async def handle(reader, writer):
        while line := await reader.readline():
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            for reply in fake_replies(msg):
                writer.write(json.dumps(reply).encode() + b"\n")
                await writer.drain()

    loop = serve_in_thread(lambda: asyncio.start_unix_server(handle, sock))
    code, text = run_client(sock, os.path.join(rt, "client-lines.log"))
    check_client_run("newline fallback", "jsonl", code, text)
    check(
        "appserver-client records the ws failure before falling back",
        "event=transport_failed kind=ws" in text,
    )
    loop.call_soon_threadsafe(loop.stop)


def test_appserver_client_ws():
    try:
        from websockets.asyncio.server import unix_serve
        from websockets.exceptions import ConnectionClosed
    except ImportError:
        print("SKIP appserver-client websocket (websockets not installed)")
        return
    sock = os.path.join(rt, "fake-ws.sock")

    async def handle(ws):
        try:
            async for raw in ws:
                for reply in fake_replies(json.loads(raw)):
                    await ws.send(json.dumps(reply))
        except ConnectionClosed:
            pass

    async def start():
        await unix_serve(handle, sock)

    loop = serve_in_thread(start)
    code, text = run_client(sock, os.path.join(rt, "client-ws.log"))
    check_client_run("websocket", "ws", code, text)
    loop.call_soon_threadsafe(loop.stop)


proxy_pass = test_passthrough_and_redaction()
test_rewrite()
test_faults()
test_summarize(proxy_pass)
test_appserver_client_lines()
test_appserver_client_ws()
sys.exit(1 if failures else 0)
