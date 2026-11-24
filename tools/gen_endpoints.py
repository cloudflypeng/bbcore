#!/usr/bin/env python3
"""Extract Bilibili API endpoint constants from PiliPlus lib/http/api.dart.

Generates internal/bili/endpoints_gen.go so the Go binary has the complete
endpoint registry embedded without depending on the Dart sources at build time.

Usage: python3 tools/gen_endpoints.py [--src reference/PiliPlus/lib/http/api.dart]
"""
import argparse
import os
import re

BASES = {
    "baseUrl": "https://www.bilibili.com",
    "apiBaseUrl": "https://api.bilibili.com",
    "tUrl": "https://api.vc.bilibili.com",
    "appBaseUrl": "https://app.bilibili.com",
    "liveBaseUrl": "https://api.live.bilibili.com",
    "liveTraceBaseUrl": "https://live-trace.bilibili.com",
    "liveUrl": "https://live.bilibili.com",
    "passBaseUrl": "https://passport.bilibili.com",
    "messageBaseUrl": "https://message.bilibili.com",
    "dynamicShareBaseUrl": "https://t.bilibili.com",
    "opusBaseUrl": "https://www.bilibili.com/opus",
    "spaceBaseUrl": "https://space.bilibili.com",
    "accountBaseUrl": "https://account.bilibili.com",
    "mallBaseUrl": "https://mall.bilibili.com",
    "sponsorBlockBaseUrl": "https://www.bsbsb.top",
}

# base -> short tag used by the Go runtime router
BASE_TAGS = {
    "https://www.bilibili.com": "www",
    "https://api.bilibili.com": "api",
    "https://api.vc.bilibili.com": "vc",
    "https://app.bilibili.com": "app",
    "https://api.live.bilibili.com": "live",
    "https://live-trace.bilibili.com": "live-trace",
    "https://live.bilibili.com": "live-web",
    "https://passport.bilibili.com": "passport",
    "https://message.bilibili.com": "message",
    "https://t.bilibili.com": "dynamic-share",
    "https://www.bilibili.com/opus": "opus",
    "https://space.bilibili.com": "space",
    "https://account.bilibili.com": "account",
    "https://mall.bilibili.com": "mall",
    "https://www.bsbsb.top": "sponsor-block",
}


def strip_comments(text: str) -> str:
    out = []
    i = 0
    n = len(text)
    while i < n:
        c = text[i]
        if c in "'\"":
            quote = c
            out.append(c)
            i += 1
            while i < n:
                ch = text[i]
                out.append(ch)
                if ch == "\\" and i + 1 < n:
                    out.append(text[i + 1])
                    i += 2
                    continue
                if ch == quote:
                    i += 1
                    break
                i += 1
            continue
        if c == "/" and i + 1 < n and text[i + 1] == "/":
            while i < n and text[i] != "\n":
                i += 1
            continue
        if c == "/" and i + 1 < n and text[i + 1] == "*":
            end = text.find("*/", i + 2)
            i = n if end < 0 else end + 2
            continue
        out.append(c)
        i += 1
    return "".join(out)


def eval_expr(expr: str):
    """Evaluate a Dart const String expression made of literals and
    ${HttpString.x} interpolations."""
    parts = []
    i = 0
    n = len(expr)
    while i < n:
        c = expr[i]
        if c in "'\"":
            quote = c
            j = i + 1
            buf = []
            while j < n:
                ch = expr[j]
                if ch == "\\" and j + 1 < n:
                    esc = expr[j + 1]
                    buf.append({"n": "\n", "t": "\t", "r": "\r"}.get(esc, esc))
                    j += 2
                    continue
                if ch == quote:
                    j += 1
                    break
                buf.append(ch)
                j += 1
            parts.append("".join(buf))
            i = j
            continue
        if c.isspace() or c == "+":
            i += 1
            continue
        return None
    value = "".join(parts)
    if "${" not in value:
        return value
    def repl(m):
        name = m.group(1).strip()
        if name.startswith("HttpString."):
            key = name.split(".", 1)[1]
            return BASES.get(key, m.group(0))
        return m.group(0)
    value = re.sub(r"\$\{([^}]+)\}", repl, value)
    if "${" in value:
        return None
    return value


CONST_RE = re.compile(r"static\s+const\s+(?:String\s+)?(\w+)\s*=\s*(.*?);", re.S)

# Endpoints PiliPlus signs with WBI even though the path has no /wbi/ marker.
WBI_OVERRIDES = {
    "ackSessionMsg", "aiConclusion", "archiveNoteList", "articleView",
    "createTextDynamic", "fansMedalPanel", "followSearch", "getLiveEmoticons",
    "historyList", "liveRoomInfoH5", "liveSetSilent", "memberDynamic",
    "memberInfo", "opusDetail", "pgcRank", "pgcSeasonRank", "popularPrecious",
    "popularSeriesOne", "removeMsg", "searchArchive", "sendMsg", "setTop",
    "superChatMsg", "superChatReport", "upowerRank", "videoRelation",
    "voteInfo", "wishUpdate",
}

# Passport endpoints that use the APP signature (appkey/ts/md5), not WBI.
APP_SIGN_OVERRIDES = {
    "getTVCode", "qrcodePoll", "qrcodeConfirm", "loginByPwdApi",
    "appSmsCode", "logInByAppSms", "oauth2AccessToken",
    "safeCenterSmsCode", "safeCenterSmsVerify", "loginDevices",
}


def parse(src_path: str):
    text = strip_comments(open(src_path, encoding="utf-8").read())
    endpoints = []
    seen = set()
    for m in CONST_RE.finditer(text):
        name = m.group(1)
        raw = m.group(2).strip()
        value = eval_expr(raw)
        if not value:
            continue
        if not (value.startswith("http://") or value.startswith("https://") or value.startswith("/")):
            continue
        if name in seen:
            continue
        seen.add(name)
        base_tag, base_url, path = split_url(value)
        if path in ("", "/"):
            continue
        endpoints.append((name, value, base_tag, base_url, path))
    endpoints.sort(key=lambda e: e[0])
    return endpoints


def split_url(value: str):
    candidates = sorted(BASE_TAGS.keys(), key=len, reverse=True)
    for base in candidates:
        if value == base:
            return BASE_TAGS[base], base, ""
        if value.startswith(base + "/"):
            return BASE_TAGS[base], base, value[len(base):]
    if value.startswith("/"):
        return "api", "https://api.bilibili.com", value
    return "url", "", value


GO_TEMPLATE = """// Code generated by tools/gen_endpoints.py from PiliPlus lib/http/api.dart. DO NOT EDIT.

package bili

// Endpoint describes one Bilibili API endpoint extracted from PiliPlus.
type Endpoint struct {
\tName string `json:"name"`
\tURL  string `json:"url"`
\tBase string `json:"base"`
\tPath string `json:"path"`
\tSign string `json:"sign"`
}

// Endpoints is the full registry keyed by the Dart constant name.
var Endpoints = map[string]Endpoint{
%s}

// EndpointList is the registry sorted by name.
var EndpointList = []Endpoint{
%s}
"""


def default_sign(name: str, base: str, path: str) -> str:
    if name in APP_SIGN_OVERRIDES:
        return "app"
    if name in WBI_OVERRIDES:
        return "wbi"
    if base == "app":
        return "app"
    if base in ("passport", "live", "live-web", "live-trace", "vc", "message", "url"):
        return "none"
    if "/wbi/" in path:
        return "wbi"
    if base == "api":
        # Web API requests tolerate w_rid/wts; signing by default maximizes
        # coverage for the endpoints that enforce it (with unsigned fallback
        # when the mixin key cannot be fetched).
        return "wbi"
    return "none"


def go_escape(s: str) -> str:
    return s.replace("\\", "\\\\").replace('"', '\\"')


def emit(endpoints, out_path):
    entries = []
    for name, url, base, _base_url, path in endpoints:
        sign = default_sign(name, base, path)
        entries.append((name, url, base, path, sign))

    map_lines = []
    list_lines = []
    for name, url, base, path, sign in entries:
        map_lines.append(
            '\t"%s": {Name: "%s", URL: "%s", Base: "%s", Path: "%s", Sign: "%s"},\n'
            % (go_escape(name), go_escape(name), go_escape(url), base, go_escape(path), sign)
        )
        list_lines.append(
            '\t{Name: "%s", URL: "%s", Base: "%s", Path: "%s", Sign: "%s"},\n'
            % (go_escape(name), go_escape(url), base, go_escape(path), sign)
        )
    content = GO_TEMPLATE % ("".join(map_lines), "".join(list_lines))
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    with open(out_path, "w", encoding="utf-8") as f:
        f.write(content)


def main():
    ap = argparse.ArgumentParser()
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    ap.add_argument("--src", default=os.path.join(root, "reference/PiliPlus/lib/http/api.dart"))
    ap.add_argument("--out", default=os.path.join(root, "internal/bili/endpoints_gen.go"))
    args = ap.parse_args()
    endpoints = parse(args.src)
    emit(endpoints, args.out)
    by_base = {}
    for e in endpoints:
        by_base[e[2]] = by_base.get(e[2], 0) + 1
    print("endpoints: %d" % len(endpoints))
    for base, count in sorted(by_base.items(), key=lambda kv: -kv[1]):
        print("  %-14s %d" % (base, count))
    wbi = sum(1 for e in endpoints if "/wbi/" in e[4] or e[2] == "app")
    print("signed by default: %d" % wbi)


if __name__ == "__main__":
    main()
