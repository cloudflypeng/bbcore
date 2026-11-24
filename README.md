# bbcore

[![CI](https://github.com/cloudflypeng/bbcore/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/cloudflypeng/bbcore/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/cloudflypeng/bbcore)](https://github.com/cloudflypeng/bbcore/releases)
[![License: GPL-3.0](https://img.shields.io/github/license/cloudflypeng/bbcore)](LICENSE)

从 [PiliPlus](https://github.com/bggRGjQaUbCoE/PiliPlus) 提取全部 B 站接口、打包成可嵌入其它软件的**独立接口进程**（sidecar）。

- **单二进制**，Go 编写，无运行时依赖。
- **stdio JSON-RPC 2.0**（NDJSON；输入同时兼容 LSP `Content-Length` 分帧），宿主进程 spawn 后即可调用。
- **315 个 HTTP 接口全量注册**为 `bili.<名称>`，另有 125 个语义化高层方法。
- 直播弹幕 **WebSocket** 客户端（zlib/brotli 解压、心跳、自动重连、事件推送）。
- **gRPC** 原始透传 + 弹幕分段类型化解码。
- **扫码登录 + Cookie 导入 + 多账号持久化**，每个请求可指定账号。

> 非官方项目，仅供学习与个人使用。请遵守 B 站用户协议，控制请求频率。

## 下载 / 安装

从 [Releases](https://github.com/cloudflypeng/bbcore/releases) 下载对应平台的压缩包：

| 平台 | 架构 | 文件 |
| --- | --- | --- |
| Linux | amd64 / arm64 | `bbcore-vX.Y.Z-linux-*.tar.gz` |
| macOS | amd64 / arm64 | `bbcore-vX.Y.Z-darwin-*.tar.gz` |
| Windows | amd64 / arm64 | `bbcore-vX.Y.Z-windows-*.zip` |

解压后直接运行；发布页附有 `SHA256SUMS.txt` 可用于校验。也可以从源码构建：

## 构建

需要 Go 1.24+。

```bash
go build -o bbcore ./cmd/bbcore
# 注入版本号：
go build -ldflags "-X github.com/cloudflypeng/bbcore/internal/service.Version=0.1.0" -o bbcore ./cmd/bbcore
# 交叉编译：
GOOS=linux  GOARCH=amd64 go build -o bbcore-linux ./cmd/bbcore
GOOS=darwin GOARCH=arm64 go build -o bbcore-mac   ./cmd/bbcore
GOOS=windows GOARCH=amd64 go build -o bbcore.exe  ./cmd/bbcore
```

## CI 与发布

- `.github/workflows/ci.yml`：在 Linux / macOS / Windows 上执行 `go vet`、`go test` 和构建，并在 Linux 上检查 `gofmt`。
- `.github/workflows/release.yml`：推送 `v*` 标签时交叉构建 6 个平台/架构，生成压缩包与 `SHA256SUMS.txt`，并创建 GitHub Release。

```bash
git tag v0.1.0
git push origin v0.1.0
```

发布产物的 `--version` 由标签自动注入（去掉前缀 `v`）。

## 运行

```bash
./bbcore [--data-dir DIR] [--debug] [--max-concurrent N] [--version]
```

| 参数 / 环境变量 | 说明 |
| --- | --- |
| `--data-dir` / `BBCORE_DATA_DIR` | 状态目录，默认 `~/.bbcore` |
| `--debug` / `BBCORE_DEBUG` | 打印每个上游请求 |
| `--max-concurrent` | 并发 RPC 上限，默认 32 |

`stdout` 只输出协议消息，日志一律写 `stderr`。数据目录内容：

- `state.json` — 账号、Cookie、access_key、匿名 Cookie（权限 `0600`）
- `wbi.json` — 每日 WBI mixin key 缓存

## 宿主集成

任意语言：启动子进程 → 写一行 JSON → 读一行 JSON。

```python
import json, subprocess

proc = subprocess.Popen(["./bbcore"], stdin=subprocess.PIPE,
                        stdout=subprocess.PIPE, text=True)

def call(method, params=None, id=1):
    proc.stdin.write(json.dumps(
        {"jsonrpc": "2.0", "id": id, "method": method, "params": params or {}}) + "\n")
    proc.stdin.flush()
    while True:
        msg = json.loads(proc.stdout.readline())
        if msg.get("id") == id:
            return msg

print(call("video.info", {"bvid": "BV1atse6EEfx"}))
proc.stdin.close()
```

```js
import { spawn } from "node:child_process";
import readline from "node:readline";

const proc = spawn("./bbcore", [], { stdio: ["pipe", "pipe", "inherit"] });
const rl = readline.createInterface({ input: proc.stdout });
const pending = new Map();

rl.on("line", (line) => {
  const msg = JSON.parse(line);
  if (msg.id !== undefined && pending.has(msg.id)) {
    pending.get(msg.id)(msg);
    pending.delete(msg.id);
  } else if (msg.method) {
    console.log("event:", msg.method, msg.params); // 通知（无 id）
  }
});

let seq = 0;
function call(method, params = {}) {
  const id = ++seq;
  proc.stdin.write(JSON.stringify({ jsonrpc: "2.0", id, method, params }) + "\n");
  return new Promise((resolve) => pending.set(id, resolve));
}

console.log(await call("search.suggest", { term: "初音" }));
```

完整协议见 [docs/PROTOCOL.md](docs/PROTOCOL.md)。

## 快速验证

```bash
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"core.ping"}' | ./bbcore
bash test/smoke.sh ./bbcore
```

## 登录与账号

### 扫码登录（TV 端协议）

1. `auth.qr.generate` → 返回 `data.url` 与 `data.auth_code`，把 `url` 渲染成二维码。
2. 每 1–2 秒调用 `auth.qr.poll {auth_code}`。
   - `code=86101` 未扫码，`86090` 已扫码待确认，`86038` 已过期。
   - `code=0` 时自动保存账号（含 Cookie、access_token、refresh_token）并设为默认账号，返回 `account`。
3. 成功/失败都会收到 `event.account.login` 通知。

### Cookie 导入

```json
{"method":"auth.import_cookies","params":{
  "cookies":"SESSDATA=...; bili_jct=...; DedeUserID=...; buvid3=...",
  "access_key":"可选",
  "make_active":true
}}
```

会先请求 `/x/member/web/account` 校验；拒绝时返回 `-32602`，`error.data` 带上游响应。

### 账号选择

所有需要登录的方法都接受 `account` 参数：

| 值 | 含义 |
| --- | --- |
| 省略 / `"active"` | 当前默认账号；未登录时用匿名（buvid） |
| `"anon"` / `"anonymous"` / `"guest"` | 强制匿名 |
| 数字 mid | 指定已保存账号 |

账号管理：`auth.accounts`、`auth.account`、`auth.account.use`、`auth.account.remove`、`auth.account.info`、`auth.logout`。

## 方法目录

全部方法可运行时查询：

```json
{"method":"core.methods","params":{"include_endpoints":true}}
{"method":"core.endpoints","params":{"q":"playurl","base":"api"}}
```

### 基础

`core.ping` `core.version` `core.info` `core.methods` `core.endpoints` `core.endpoint` `core.shutdown`
`api.call` `api.endpoint`
`auth.qr.generate` `auth.qr.poll` `auth.import_cookies` `auth.accounts` `auth.account` `auth.account.use` `auth.account.remove` `auth.account.info` `auth.logout`

### 视频

`video.recommend` `video.recommend_app` `video.hot` `video.rank` `video.info` `video.playurl` `video.play_info` `video.related` `video.relation` `video.tags` `video.online` `video.like` `video.coin` `video.triple` `video.dislike` `video.ai_conclusion`

### 用户 / 收藏

`user.me` `user.info` `user.stat` `user.card` `user.videos` `user.history` `user.later` `user.later.add` `user.later.del` `user.later.clear` `user.followings` `user.followers` `user.follow` `user.blacklist` `user.folders` `user.sub_folders`

`fav.all_folders` `fav.folders` `fav.info` `fav.list` `fav.add` `fav.remove` `fav.create_folder` `fav.edit_folder` `fav.delete_folder` `fav.clean`

### 搜索 / 动态

`search.default` `search.all` `search.type` `search.suggest` `search.trending` `search.hot` `search.recommend`

`dynamic.feed` `dynamic.space` `dynamic.detail` `dynamic.like` `dynamic.opus`

### 直播

`live.list` `live.following` `live.room_info` `live.room_info_h5` `live.info_user` `live.danmaku_info` `live.history` `live.super_chat` `live.send_msg` `live.connect` `live.disconnect` `live.rooms`

`live.connect` 后弹幕以 `event.live.message` 通知推送，无需轮询。

### 评论 / 弹幕

`reply.list` `reply.replies` `reply.add` `reply.delete` `reply.like` `reply.hate` `reply.top` `reply.report`
`danmaku.seg` `danmaku.seg_raw` `danmaku.send` `danmaku.like` `danmaku.report` `danmaku.recall`

### 消息 / 番剧 / 其它

`msg.replies` `msg.ats` `msg.likes` `msg.like_detail` `msg.unread` `msg.system` `msg.update_cursor`
`pgc.timeline` `pgc.info` `pgc.playurl` `pgc.rank` `pgc.season_rank` `pgc.status` `pgc.review` `pgc.review_action` `pgc.review_post` `pgc.review_modify` `pgc.review_delete` `pgc.follow`
`misc.sponsor_segments` `misc.match_info` `misc.music_detail` `misc.music_recommend` `misc.music_wish` `misc.video_note`

### gRPC

- `grpc.call` — 任意路径，`payload_base64` 为 protobuf 请求，返回 `message_base64`。
- `grpc.reply_main` — 评论主列表（请求编码，响应仍为 protobuf base64；可直接用 Web 版 `reply.list`）。
- `danmaku.seg` — 弹幕分段，返回已解码的 `elems`。

## 原始接口（全量 315）

`lib/http/api.dart` 中的每个常量都被注册为 `bili.<名称>`：

```json
{"method":"bili.playInfo","params":{"query":{"bvid":"BV1atse6EEfx","cid":43750131546}}}
{"method":"bili.searchByType","params":{"query":{"keyword":"初音","search_type":"video","page":1}}}
```

- 签名自动选择：`app` → APP 签名；`api` → WBI 签名；其余 → 不签名；个别端点使用注册表内的值。可用 `sign` 参数强制覆盖。
- 通用参数：`query`、`form`、`json`、`body_base64`、`content_type`、`headers`、`method`、`no_cookies`、`skip_access_key`、`meta`。
- `api.call` 可调用任意 URL/路径（含 `body_base64` 上传任意请求体）。

上游返回原样透传，包括非 0 的 `code`。网络/解码失败才转换成 JSON-RPC 错误。

## 事件（服务端通知）

无 `id` 的 JSON-RPC 通知：

| 方法 | 负载 |
| --- | --- |
| `event.live.connected` | `{room_id}` |
| `event.live.disconnected` | `{room_id, reason}`（自动重连前发出） |
| `event.live.popularity` | `{room_id, value}` |
| `event.live.message` | `{room_id, cmd, data, danmaku?}` |
| `event.account.login` / `logout` / `removed` | `{mid, name?}` |

`event.live.message` 的 `data` 是上游原始 JSON；`DANMU_MSG` 额外带 `danmaku` 摘要：`text`、`uid`、`uname`、`id`、`dm_type`、`fontsize`、`color`、`mode`。

## 数据与安全

- Cookie / access_key 只保存在本地 `state.json`（`0600`），不经过任何第三方；请求直连 B 站与 SponsorBlock 服务器。
- 无遥测、无自建服务端。
- `state.json` 等同登录凭据，勿提交到版本库或分享；仓库与发布包中不包含任何账号、Cookie、access_key 或测试数据。
- `internal/bili/constants.go` 中的 appkey / appsec / aurora key 是上游 PiliPlus 开源客户端的公开标识，不是用户凭据。
- SponsorBlock 功能会向 `https://www.bsbsb.top` 发送 bvid/cid。

## 开发

```bash
go test ./...        # 签名、分帧、protobuf、弹幕解析等单元测试
bash test/smoke.sh   # 真实网络冒烟测试
python3 test/probe_all.py --bin ./bbcore --data-dir ~/.bbcore  # 全量接口探测
python3 tools/gen_endpoints.py   # 从 reference/PiliPlus/lib/http/api.dart 重新生成注册表
```

`test/probe_all.py` 默认使用 `./.bbcore-probe` 作为数据目录，只调用只读接口；写接口使用匿名账号探测并跳过会修改账号状态的接口。`reference/` 是本地参考源码目录，已被 `.gitignore` 排除；重新生成端点注册表时需自行克隆 PiliPlus 到该位置，或通过 `--src` 指定 `api.dart`。

目录：

```
cmd/bbcore          进程入口（stdin/stdout 接线、信号处理、buvid 初始化）
internal/bili       HTTP 传输、签名、Cookie、WBI、端点注册表
internal/biligrpc   gRPC 传输与 protobuf 编解码
internal/jsonrpc    JSON-RPC 2.0 + NDJSON/Content-Length 分帧
internal/live       直播弹幕 WebSocket
internal/service    RPC 方法层（core/auth/api/各业务模块）
internal/state      多账号持久化
docs/PROTOCOL.md    完整协议说明
```

## 已知限制

- access_token 过期后不会自动刷新（refresh_token 已保存，可后续补充刷新流程）。
- 泛型 `api.call` 不解析 multipart；需要上传时请自行构造 `body_base64` + `content_type`。
- `grpc.reply_main` 只编码请求，响应为原始 protobuf；评论内容建议走 `reply.list`。
- 部分 APP 专用接口依赖客户端商业参数，`bili.<名称>` 已保证参数透传能力，必要时需宿主补齐。
- 少量接口需要 APP access_key 或特定账号权限（如实名、直播推荐），仅导入 Web Cookie 时可能返回 `-101` / `-352`。
- 上游已下线或受隐私限制的接口可能返回 404 / 22115，会原样透传。

## 致谢与许可

- 接口清单与客户端参数来自 [PiliPlus](https://github.com/bggRGjQaUbCoE/PiliPlus)，端点注册表由 `lib/http/api.dart` 生成。
- 本项目与哔哩哔哩无关联，未获得官方授权。
- PiliPlus 以 GNU GPL v3 发布；本项目作为其衍生作品同样采用 [GPL-3.0](LICENSE)。
