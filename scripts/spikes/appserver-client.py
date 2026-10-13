#!/usr/bin/env python3
# /// script
# requires-python = ">=3.10"
# dependencies = ["websockets"]
# ///
"""Second client for a `codex app-server --listen unix://...` socket (spike 1a).

Connects over WebSocket-on-unix-socket, falls back to newline-delimited JSON-RPC,
joins the thread the TUI created, waits for turn/started, then sends
thread/settings/update and logs every notification by method name.

Logs method names, ids, error codes and model/effort values only; never item or
message text. Run: uv run --with websockets scripts/spikes/appserver-client.py --sock S --log F
"""

import argparse
import asyncio
import json
import signal
import time

START = time.monotonic()
LOG_PATH = ""


def log(line):
    text = f"t={time.monotonic() - START:.3f} {line}"
    with open(LOG_PATH, "a") as fh:
        fh.write(text + "\n")


def find_key(obj, key):
    if isinstance(obj, dict):
        if key in obj and isinstance(obj[key], (str, int, float, type(None))):
            return obj[key]
        for value in obj.values():
            found = find_key(value, key)
            if found is not None:
                return found
    elif isinstance(obj, list):
        for value in obj:
            found = find_key(value, key)
            if found is not None:
                return found
    return None


class WsTransport:
    def __init__(self, conn):
        self.conn = conn

    async def send(self, obj):
        await self.conn.send(json.dumps(obj))

    async def recv(self):
        return json.loads(await self.conn.recv())

    async def close(self):
        await self.conn.close()


class LineTransport:
    def __init__(self, reader, writer):
        self.reader, self.writer = reader, writer

    async def send(self, obj):
        self.writer.write(json.dumps(obj).encode() + b"\n")
        await self.writer.drain()

    async def recv(self):
        line = await self.reader.readline()
        if not line:
            raise ConnectionError("eof")
        return json.loads(line)

    async def close(self):
        self.writer.close()


async def connect_ws(sock):
    from websockets.asyncio.client import unix_connect

    conn = await asyncio.wait_for(unix_connect(sock, "ws://localhost/"), 5)
    return WsTransport(conn)


async def connect_lines(sock):
    reader, writer = await asyncio.open_unix_connection(sock)
    return LineTransport(reader, writer)


async def connect(sock):
    try:
        transport = await connect_ws(sock)
        log("event=transport kind=ws")
        return transport
    except Exception as exc:
        log(f"event=transport_failed kind=ws error={type(exc).__name__}")
    transport = await connect_lines(sock)
    log("event=transport kind=jsonl")
    return transport


class Client:
    def __init__(self, transport):
        self.transport = transport
        self.next_id = 0
        self.pending = {}
        self.events = asyncio.Queue()

    async def request(self, method, params):
        self.next_id += 1
        req_id = self.next_id
        fut = asyncio.get_running_loop().create_future()
        self.pending[req_id] = fut
        await self.transport.send({"id": req_id, "method": method, "params": params})
        sent = time.monotonic()
        reply = await asyncio.wait_for(fut, 30)
        elapsed = int((time.monotonic() - sent) * 1000)
        if "error" in reply:
            err = reply["error"]
            log(
                f"event=response method={method} ms={elapsed} error_code={err.get('code')} "
                f"error={str(err.get('message'))[:100]!r}"
            )
        else:
            log(f"event=response method={method} ms={elapsed} ok=1")
        return reply

    async def notify(self, method, params=None):
        msg = {"method": method}
        if params is not None:
            msg["params"] = params
        await self.transport.send(msg)

    async def reader(self):
        while True:
            msg = await self.transport.recv()
            if "method" in msg and "id" not in msg:
                params = msg.get("params")
                model, effort = find_key(params, "model"), find_key(params, "effort")
                extra = f" model={model} effort={effort}" if model or effort else ""
                log(f"event=notify method={msg['method']}{extra}")
                await self.events.put(msg["method"])
            elif "method" in msg:
                log(f"event=server_request method={msg['method']} (not answered)")
            elif msg.get("id") in self.pending:
                self.pending.pop(msg["id"]).set_result(msg)


async def wait_method(client, method, timeout):
    deadline = time.monotonic() + timeout
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            return False
        try:
            seen = await asyncio.wait_for(client.events.get(), remaining)
        except TimeoutError:
            return False
        if seen == method:
            return True


def first_list(result):
    if isinstance(result, dict):
        for value in result.values():
            if isinstance(value, list):
                return value
    return []


async def find_thread(client, timeout):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        reply = await client.request("thread/loaded/list", {})
        ids = first_list(reply.get("result"))
        if isinstance(ids, list) and ids:
            return ids[0] if isinstance(ids[0], str) else find_key(ids[0], "id")
        await asyncio.sleep(1)
    return None


async def run(args):
    client = Client(await connect(args.sock))
    reader = asyncio.create_task(client.reader())
    await client.request(
        "initialize",
        {
            "clientInfo": {"name": "codex-router-spike", "title": None, "version": "0"},
            "capabilities": {"experimentalApi": True},
        },
    )
    await client.notify("initialized")
    log("event=initialized experimentalApi=true")
    thread_id = await find_thread(client, args.thread_timeout)
    if not thread_id:
        log("event=no_thread_found")
        return 2
    log("event=thread_found")
    await client.request("thread/resume", {"threadId": thread_id})
    if not await wait_method(client, "turn/started", args.turn_timeout):
        log("event=turn_started_timeout")
        return 3
    log(f"event=turn_started_seen sleeping={args.update_delay}")
    await asyncio.sleep(args.update_delay)
    await client.request(
        "thread/settings/update",
        {
            "threadId": thread_id,
            "model": args.update_model,
            "effort": args.update_effort,
        },
    )
    log(f"event=update_sent model={args.update_model} effort={args.update_effort}")
    await asyncio.sleep(args.listen_seconds)
    reader.cancel()
    return 0


def main():
    global LOG_PATH
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    parser.add_argument("--sock", required=True)
    parser.add_argument("--log", required=True)
    parser.add_argument("--update-model", default="gpt-6-luna")
    parser.add_argument("--update-effort", default="low")
    parser.add_argument(
        "--update-delay", type=float, default=3, help="seconds after turn/started"
    )
    parser.add_argument("--thread-timeout", type=float, default=180)
    parser.add_argument("--turn-timeout", type=float, default=300)
    parser.add_argument(
        "--listen-seconds",
        type=float,
        default=900,
        help="keep logging notifications after the update; stop with SIGTERM",
    )
    args = parser.parse_args()
    LOG_PATH = args.log
    loop = asyncio.new_event_loop()
    main_task = loop.create_task(run(args))
    loop.add_signal_handler(signal.SIGTERM, main_task.cancel)
    loop.add_signal_handler(signal.SIGINT, main_task.cancel)
    try:
        code = loop.run_until_complete(main_task)
    except asyncio.CancelledError:
        code = 0
    except Exception as exc:
        log(f"event=client_error error={type(exc).__name__}")
        code = 1
    log(f"event=exit code={code}")
    raise SystemExit(code)


if __name__ == "__main__":
    main()
