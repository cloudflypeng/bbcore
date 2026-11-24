#!/usr/bin/env bash
# End-to-end smoke test for bbcore. Requires network access (disable parts with
# BBCORE_SMOKE_NET=0 / BBCORE_SMOKE_LIVE=0).
set -euo pipefail

BIN=${1:-./bbcore}
if [[ ! -x "$BIN" ]]; then
  echo "building $BIN"
  go build -o "$BIN" ./cmd/bbcore
fi
BIN=$(cd "$(dirname "$BIN")" && pwd)/$(basename "$BIN")

DATA_DIR=$(mktemp -d "${TMPDIR:-/tmp}/bbcore-smoke.XXXXXX")
trap 'rm -rf "$DATA_DIR"' EXIT

BBCORE_DATA_DIR="$DATA_DIR" \
BBCORE_SMOKE_NET="${BBCORE_SMOKE_NET:-1}" \
BBCORE_SMOKE_LIVE="${BBCORE_SMOKE_LIVE:-1}" \
python3 - "$BIN" <<'PY'
import json, os, queue, subprocess, sys, threading, time

bin_path = sys.argv[1]
net = os.environ.get("BBCORE_SMOKE_NET", "1") != "0"
live = os.environ.get("BBCORE_SMOKE_LIVE", "1") != "0"

proc = subprocess.Popen(
    [bin_path],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    text=True,
    bufsize=1,
)
lines = queue.Queue()

def pump():
    for line in proc.stdout:
        lines.put(line)
    lines.put(None)

threading.Thread(target=pump, daemon=True).start()
notifications = []
seq = 0

def read(timeout=90):
    try:
        line = lines.get(timeout=timeout)
    except queue.Empty:
        raise AssertionError(f"timeout after {timeout}s waiting for response")
    if line is None:
        stderr = proc.stderr.read()
        raise AssertionError(f"bbcore exited early\n{stderr}")
    return json.loads(line)

def call(method, params=None, timeout=90, allow_error=False):
    global seq
    seq += 1
    req = {"jsonrpc": "2.0", "id": seq, "method": method}
    if params is not None:
        req["params"] = params
    proc.stdin.write(json.dumps(req) + "\n")
    proc.stdin.flush()
    deadline = time.time() + timeout
    while True:
        msg = read(max(1, deadline - time.time()))
        if msg.get("id") == seq:
            if "error" in msg and not allow_error:
                raise AssertionError(f"{method} failed: {msg['error']}")
            return msg
        if "method" in msg:
            notifications.append(msg)

checks = 0
def check(cond, label, detail=""):
    global checks
    checks += 1
    if not cond:
        raise AssertionError(f"{label} {detail}")
    print(f"  ok  {label}")

print("== core ==")
check(call("core.ping")["result"]["pong"] is True, "core.ping")
ver = call("core.version")["result"]
check(ver["protocol"] == 1 and ver["name"] == "bbcore", "core.version", ver)
info = call("core.info")["result"]
check(info["endpoints"] == 315, "315 endpoints registered", info["endpoints"])
check(info["methods"] >= 440, "method count", info["methods"])
check(call("auth.accounts")["result"]["accounts"] == [], "no accounts initially")
missing = call("no.such.method", allow_error=True)
check(missing["error"]["code"] == -32601, "unknown method -> -32601")

if net:
    print("== network (anonymous) ==")
    hot = call("video.hot", {"ps": 1, "pn": 1})["result"]
    check(hot["code"] == 0 and hot["data"]["list"], "video.hot")
    sample = hot["data"]["list"][0]
    bvid, cid = sample["bvid"], sample["cid"]

    vinfo = call("video.info", {"bvid": bvid})["result"]
    check(vinfo["code"] == 0 and vinfo["data"].get("bvid") == bvid, "video.info")

    suggest = call("search.suggest", {"term": "初音"})["result"]
    check(suggest["code"] == 0, "search.suggest")

    seg = call("danmaku.seg", {"cid": cid, "segment_index": 1}, timeout=120)["result"]
    check("elems" in seg, "danmaku.seg (gRPC)", f"{len(seg.get('elems') or [])} elems")

    qr = call("auth.qr.generate")["result"]
    check(qr["code"] == 0 and qr["data"]["url"].startswith("https://"), "auth.qr.generate")

    if live:
        print("== network (live) ==")
        conn = call("live.connect", {"room_id": 6})["result"]
        check(conn["room_id"] == 6 and conn["host"], "live.connect")
        deadline = time.time() + 20
        while time.time() < deadline:
            if any(n["method"] == "event.live.connected" for n in notifications):
                break
            try:
                n = lines.get(timeout=max(0.1, deadline - time.time()))
            except queue.Empty:
                break
            if n is None:
                break
            msg = json.loads(n)
            if "method" in msg:
                notifications.append(msg)
        check(any(n["method"] == "event.live.connected" for n in notifications),
              "event.live.connected")
        check(call("live.disconnect", {"room_id": 6})["result"]["disconnected"] is True,
              "live.disconnect")

print("== shutdown ==")
check(call("core.shutdown")["result"]["ok"] is True, "core.shutdown")
try:
    code = proc.wait(timeout=15)
except subprocess.TimeoutExpired:
    proc.kill()
    raise AssertionError("process did not exit after core.shutdown (stdin still open)")
try:
    proc.stdin.close()
except BrokenPipeError:
    pass
check(code == 0, "clean exit", code)

print(f"\nPASS: {checks} checks")
PY
