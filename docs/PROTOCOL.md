# bbcore 协议说明（v1）

bbcore 是一个通过 **stdin/stdout** 与宿主进程通信的接口进程。协议基于 **JSON-RPC 2.0**，
传输层使用 NDJSON（每行一条消息）；输入侧同时接受 LSP 风格的 `Content-Length` 分帧。
通过 `-listen` 启动时也可以改用本机 HTTP POST 传输，见 1.3。

- 宿主 → bbcore：请求（带 `id`）或通知（不带 `id`）。
- bbcore → 宿主：响应（对应请求 `id`）或服务端通知（无 `id`，用于推送事件）。
- `stdout` 仅包含协议消息，一行一条，UTF-8，无 BOM。
- 日志、警告、panic 栈一律写 `stderr`。

## 1. 传输与分帧

### 1.1 NDJSON（推荐）

每行一个完整 JSON 对象，以 `\n` 结束。bbcore 的输出永远是 NDJSON。

```
{"jsonrpc":"2.0","id":1,"method":"core.ping"}
{"jsonrpc":"2.0","id":1,"result":{"pong":true,"time":1795517325240}}
```

### 1.2 Content-Length（可选输入）

宿主可按 LSP 方式发送：

```
Content-Length: 53\r\n
\r\n
{"jsonrpc":"2.0","id":1,"method":"core.version"}
```

头部大小写不敏感，其余头部被忽略。响应仍为 NDJSON。

### 1.3 HTTP（可选）

以 `bbcore -listen 127.0.0.1:8787` 启动时，bbcore 不再读取 stdin，而是在指定地址接受
HTTP `POST`。请求体为单个 JSON-RPC 请求，响应体为单个 JSON-RPC 响应：

```bash
curl -s http://127.0.0.1:8787 \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"core.ping"}'
```

不带 `id` 的通知返回 `204 No Content`。

服务端事件通过 `GET /events` 以 SSE 推送，支持断线续传与 topic 过滤：

```bash
curl -N 'http://127.0.0.1:8787/events?topics=live.*&since=0'
```

| 参数 | 说明 |
| --- | --- |
| `topics` | 逗号分隔的 topic，支持 `live.*` 前缀；省略表示全部 |
| `since` | 从该 `seq` 之后开始推送；也可用 `Last-Event-ID` 请求头 |
| `Authorization` | 启动时设置 `--token` 或 `BBCORE_HTTP_TOKEN` 后必须为 `Bearer <token>` |

SSE 每条事件格式为 `id: <seq>` + `data: <JSON-RPC 通知>`，服务端每 15 秒发送一次
`: heartbeat` 注释。`since=0` 只接收订阅之后的新事件；传入上次收到的 `seq` 可回放最近的
保留窗口。该模式供无法创建子进程的宿主使用（例如 iOS 模拟器）。服务只应绑定回环地址。

### 1.4 并发

请求可并发处理（默认上限 32，`--max-concurrent` 可调）。响应**不保证顺序**：宿主必须按 `id` 匹配。

## 2. 消息格式

### 2.1 请求

```json
{"jsonrpc":"2.0","id":1,"method":"video.info","params":{"bvid":"BV1atse6EEfx"}}
```

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `jsonrpc` | 否 | 固定 `"2.0"`，省略也接受 |
| `id` | 否 | 字符串或数字；省略或 `null` 表示通知，不产生响应 |
| `method` | 是 | 方法名，见第 4 节 |
| `params` | 否 | 对象，或单元素数组 `[{...}]` |

### 2.2 响应

成功：

```json
{"jsonrpc":"2.0","id":1,"result":{...}}
```

失败：

```json
{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"cid is required","data":{...}}}
```

### 2.3 通知（服务端推送）

```json
{"jsonrpc":"2.0","method":"event.live.message","params":{"seq":42,"ts":1795517325240,"data":{"room_id":6,"cmd":"DANMU_MSG","data":{...}}}}
```

所有事件使用统一信封：`method` 为 `event.<topic>`，`params.seq` 单调递增，`params.ts`
为毫秒时间戳，`params.data` 为事件负载。宿主应始终处理无 `id` 的消息；断线重连时用上次
收到的 `seq` 作为 `since` 或 `Last-Event-ID`。

### 2.4 批量

输入接受 JSON 数组（批量请求），每个元素独立处理并各自返回；输出不使用批量信封。

## 3. 错误码

| code | 含义 |
| --- | --- |
| `-32700` | 解析错误（非法 JSON / 分帧错误） |
| `-32600` | 非法请求（缺少 `method`） |
| `-32601` | 方法不存在 |
| `-32602` | 参数错误；Cookie 校验被上游拒绝时也使用，`data` 携带上游响应 |
| `-32603` | 内部错误：网络失败、解码失败、上游 gRPC 调用失败等 |

注意：**上游 B 站返回的非 0 `code` 不算 JSON-RPC 错误**，而是作为正常 `result` 原样返回，
以便宿主读取 `code` / `message` / `data`。只有传输或解析层面失败才返回 `error`。

## 4. 方法命名

| 前缀 | 说明 |
| --- | --- |
| `core.` | 进程信息、方法发现、关闭 |
| `auth.` | 登录、账号管理 |
| `api.` | 通用 HTTP 调用 |
| `bili.` | 315 个原始接口，一对一映射 `lib/http/api.dart` |
| `video.` `user.` `search.` `dynamic.` `live.` `reply.` `danmaku.` `fav.` `msg.` `pgc.` `misc.` | 语义化高层方法 |
| `grpc.` | gRPC 透传 / 类型化调用 |

运行时发现：

- `core.capabilities` — 版本、事件 topic、功能开关（字幕/播放器/下载/IM 等）与方法数量。
- `core.methods` — 列出语义方法；`include_endpoints:true` 时连同 `bili.*`。
- `core.endpoints` — 列出接口注册表，可按 `q`（名称/URL 子串）和 `base` 过滤。
- `core.endpoint` — 查看单个接口的 `{name,url,base,path,sign}`。

### 4.1 语义方法参数约定

- 所有参数放在 `params` 对象中；`account` 见第 6 节。
- ID 类参数既可为数字，也可为数字字符串（`"123"`），内部按 int64 解析，避免精度丢失。
- 列表类参数（如 `resources`、`media_ids`、`aids`）接受字符串（逗号分隔）或数组。
- 布尔参数接受 `true/false`、`"true"/"false"`、`0/1`。
- `query` / `form` 接受对象；数组值会展开为重复键。
- `meta:true` 返回 `{status, headers, content_type, json|body_base64}`，便于调试。

### 4.2 原始接口参数

```json
{"method":"bili.playInfo","params":{
  "query":{"bvid":"BV1atse6EEfx","cid":43750131546},
  "headers":{"referer":"https://www.bilibili.com"},
  "account":"active",
  "meta":false
}}
```

| 参数 | 说明 |
| --- | --- |
| `query` / `form` | 查询串 / 表单对象 |
| `json` | 任意 JSON 请求体（自动 `Content-Type: application/json`） |
| `body_base64` | 原始请求体 |
| `content_type` | 覆盖请求体类型 |
| `method` | `GET` / `POST` / …；省略时按是否有请求体推断 |
| `headers` | 额外请求头 |
| `sign` | `app` / `wbi` / `none`，覆盖注册表默认签名 |
| `no_cookies` | 不发送 Cookie |
| `skip_access_key` | 不附加 `access_key` |
| `meta` | 返回元信息信封 |

`api.call` 额外支持 `base`（见下）与 `url`（绝对地址，优先于 `path`）。
`api.endpoint` 等价于按名称调用 `bili.<name>`（`name` 可带 `bili.` 前缀）。

### 4.3 base 取值

`www` `api` `vc` `app` `live` `live-trace` `live-web` `passport` `message`
`dynamic-share` `opus` `space` `account` `mall` `sponsor-block`。
使用绝对 `url` 时忽略 `base`。

### 4.4 签名策略

| 条件 | 签名 |
| --- | --- |
| `base=app` | APP 签名（`appkey` + `ts` + `sign`，MD5） |
| `base=api` | WBI 签名（`wts` + `w_rid`，每日密钥缓存于 `wbi.json`） |
| 其它 base | 不签名 |

WBI 密钥获取失败时自动退化为不签名请求（上游通常会拒绝并返回可读 `code`）。
注册表内标记为 `none` 的接口（如 `liveRoomInfo`、`hotSearchList`）即使 `base=api` 也不签名。

## 5. 结果语义

- HTTP 成功且响应为 JSON：`result` 即该 JSON 对象。
- 响应非 JSON：`{status, content_type, body_base64}`。
- `meta:true`：`{status, headers, content_type, json?, body_base64?}`。
- gRPC：`{status, grpc_status, grpc_message, message_base64, headers?, trailers?}`。

## 6. 账号引用

所有需要凭据的方法都接受 `account`：

| 值 | 行为 |
| --- | --- |
| 省略 / `"active"` | 默认账号；未登录时使用匿名 Cookie（buvid3/4、b_nut） |
| `"anon"` / `"anonymous"` / `"guest"` | 强制匿名 |
| 数字 mid | 指定账号；不存在时 `-32602` |

匿名 Cookie 在进程启动时通过 `/x/frontend/finger/spi` 获取并持久化；上游 `Set-Cookie`
会自动合并回 `state.json`（登录账号按 mid 分开保存）。

## 7. 登录流程

### 7.1 扫码登录（TV 协议）

**步骤 1**：

```json
{"jsonrpc":"2.0","id":1,"method":"auth.qr.generate"}
```

```json
{"jsonrpc":"2.0","id":1,"result":{"code":0,"message":"OK","data":{
  "auth_code":"34b280c9...","url":"https://passport.bilibili.com/x/passport-tv-login/h5/qrcode/auth?auth_code=..."
}}}
```

宿主把 `data.url` 渲染为二维码。TV 协议下 B 站 App「扫一扫」可直接登录。

**步骤 2**：每 1–2 秒轮询（建议 180 秒超时）：

```json
{"jsonrpc":"2.0","id":2,"method":"auth.qr.poll","params":{"auth_code":"34b280c9..."}}
```

| 上游 `code` | 含义 |
| --- | --- |
| `0` | 登录成功；账号已保存并激活 |
| `86101` | 未扫码 |
| `86090` | 已扫码，等待确认 |
| `86038` | 二维码已过期 |

成功响应：

```json
{"jsonrpc":"2.0","id":2,"result":{"code":0,"data":{...上游原始登录响应...},
  "account":{"mid":123,"name":"...","avatar":"...","has_access_key":true,...}}}
```

同时推送 `event.account.login`。`make_active:false` 可只保存不激活。

### 7.2 Cookie 导入

```json
{"jsonrpc":"2.0","id":3,"method":"auth.import_cookies","params":{
  "cookies":{"SESSDATA":"...","bili_jct":"...","DedeUserID":"123","buvid3":"..."},
  "access_key":"可选",
  "refresh_token":"可选",
  "make_active":true
}}
```

`cookies` 也可以是一整串 `k=v; k=v` 文本。进程先访问 `/x/member/web/account` 校验：

- 通过：返回 `{account, profile}`，并推送 `event.account.login`。
- 拒绝：`error.code=-32602`，`error.data` 为上游原始响应。

### 7.3 账号管理

| 方法 | 参数 | 说明 |
| --- | --- | --- |
| `auth.accounts` | — | `{active_mid, accounts:[...]}` |
| `auth.account` | `account` | 单个账号详情 |
| `auth.account.use` | `mid` | 切换默认账号 |
| `auth.account.remove` | `mid` | 仅本地删除 |
| `auth.account.info` | `account` | 实时请求 nav，验证是否仍有效 |
| `auth.logout` | `account` | 通知上游退出并本地删除 |

账号详情字段：`mid`、`name`、`avatar`、`added_at`、`checked_at`、`has_access_key`、`cookies`（数量）、`active`。
不会返回 Cookie 明文。

## 8. 直播弹幕事件

订阅：

```json
{"jsonrpc":"2.0","id":10,"method":"live.connect","params":{"room_id":6,"account":"active"}}
```

返回：

```json
{"room_id":6,"host":"zj-cn-live-comet.chat.bilibili.com:2245","token":"...","uid":0}
```

随后推送（统一事件信封，`data` 为下表负载）：

| 方法 | `data` 负载 |
| --- | --- |
| `event.live.connected` | `{room_id}`（WebSocket 认证成功；重连成功也会再次推送） |
| `event.live.disconnected` | `{room_id, reason}`（连接断开，自动重连前） |
| `event.live.popularity` | `{room_id, value}`（每 30 秒心跳回包） |
| `event.live.message` | `{room_id, cmd, data, danmaku?}` |

`data` 为上游原始消息对象。`DANMU_MSG` 系列额外附带摘要：

```json
{"seq":42,"ts":1795517325240,"data":{"room_id":6,"cmd":"DANMU_MSG","data":{...},
 "danmaku":{"text":"哈哈哈","uid":123,"uname":"用户","id":"2248...","dm_type":1,
            "fontsize":25,"color":16777215,"mode":1}}}
```

同一房间重复 `live.connect` 返回现有连接信息。`live.disconnect {room_id}` 停止订阅；
`live.rooms` 列出当前订阅；进程退出或 `core.shutdown` 时全部关闭。

重连策略：1s 起指数退避，上限 30s，直到 `disconnect` 或进程结束。
协议：16 字节头（总长、头长、protover、opcode、sequence），支持 zlib（protover=2）
与 brotli（protover=3）压缩包，嵌套解压深度上限 4。

## 9. HLS 播放网关

`video.playurl` 返回的是 DASH/fMP4：视频和音频分开，每路由 `SegmentBase` 指向一个带
`sidx` 索引的完整文件。AVPlayer 无法直接消费这种形式，`stream.start` 会把它转换成
本机 HLS：

1. 选择视频轨：优先 HEVC（`codecid=12`），否则 AVC（`codecid=7`）；跳过 AV1。
2. 选择音频轨：默认最高码率 AAC，可用 `audio_id` 指定。
3. 拉取每路的初始化段和 `sidx`，按索引生成 `#EXT-X-MAP` + `#EXT-X-BYTERANGE` 的 VOD 播放列表。
4. 在本机回环地址启动临时 HTTP 网关，把播放器的 Range 请求转发给 B 站 CDN；主地址失败时尝试 `backupUrl`。
5. 返回 master playlist 的本地 URL，宿主直接创建 `AVPlayerItem` 即可。

请求：

```json
{"jsonrpc":"2.0","id":7,"method":"stream.start","params":{
  "bvid":"BV1atse6EEfx","qn":80,"codec":"hevc"
}}
```

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `bvid` | 是 | 视频 BV 号 |
| `cid` | 否 | 分 P 的 cid；省略时按 `page` 解析 |
| `page` | 否 | 分 P 序号，默认 1 |
| `qn` | 否 | 目标清晰度，默认 80；选择不高于该值的最高画质 |
| `codec` | 否 | `hevc`（默认）或 `avc` |
| `audio_id` | 否 | 指定音频流 ID；省略时选最高码率 AAC |
| `account` | 否 | 账号引用；高清晰度通常需要登录 |

响应：

```json
{"session_id":"<32 位十六进制>",
 "master_url":"http://127.0.0.1:<port>/s/<session>/master.m3u8",
 "expires_at":1790000000,
 "duration_ms":1265088,
 "video":{"id":80,"codecid":12,"codecs":"hvc1.1.6.L150.90",
          "width":1920,"height":1080,"bandwidth":922000,"segments":254},
 "audio":{"id":30280,"codecs":"mp4a.40.2","bandwidth":179000,"segments":253}}
```

- `session_id` 是随机能力令牌；URL 只绑定 `127.0.0.1`，不会暴露到局域网。
- 会话默认保留 2 小时；每次访问播放列表或分片会续期。宿主播放结束应调用 `stream.stop`：
  ```json
  {"jsonrpc":"2.0","id":8,"method":"stream.stop","params":{"session_id":"<session>"}}
  ```
  返回 `{stopped:true|false}`；停止后该会话的 playlist 和分片请求返回 404。
- 网关不转码，只解析索引并代理字节范围；HEVC/AVC 与 AAC 的原始编码保持不变。
- 当前只支持 UGC；番剧/影视使用 `pgc.play`（见 10.1），直播使用 `live.play`（见 9.1）。

### 9.1 直播播放

`live.play` 读取直播间信息与播放地址，优先选择 HTTP HLS（fmp4 或 ts），并返回可直接
交给 `AVPlayer` 的本地地址。上游播放列表和分片由 `/l/` 网关实时代理：每次请求都会重新
拉取上游播放列表并重写其中的分片 URI，因此直播滚动窗口始终是最新的。

```json
{"jsonrpc":"2.0","id":9,"method":"live.play","params":{
  "room_id":13360144,"qn":10000
}}
```

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `room_id` | 是 | 直播间号（支持短号） |
| `qn` | 否 | 目标清晰度，默认 10000；实际返回以账号权限为准 |
| `format` | 否 | `fmp4` 或 `ts`；省略时优先高清晰度，同清晰度优先 fmp4 |
| `codec` | 否 | `avc`、`hevc` 或 `av1`；省略时按 AVC → HEVC → AV1 选择，避免不支持 AV1 的播放器拿到无法解码的流 |
| `account` | 否 | 账号引用；高清晰度通常需要登录 |

响应：

```json
{"playable":true,
 "session_id":"<32 位十六进制>",
 "playback_url":"http://127.0.0.1:<port>/l/<session>/playlist.m3u8",
 "expires_at":1790000000,
 "format":"fmp4","codec":"avc",
 "quality":{"qn":10000,"desc":"原画"},
 "qualities":[{"qn":10000,"desc":"原画","current":true}],
 "room":{"room_id":13360144,"short_id":0,"uid":349516380,
         "title":"…","anchor_name":"…","anchor_face":"https://…",
         "cover":"https://…","keyframe":"https://…",
         "area_name":"…","parent_area_name":"…","description":"",
         "online":1560,"watched":481,"attention":319769,
         "live_status":1,"live_start_time":1795531773}}
```

- 未开播时返回 `{"playable":false,"reason":"not_live","room":{…}}`，宿主应展示房间信息而不是报错。
- 上游只提供 FLV 时返回 `reason:"hls_unavailable"`，网关不做 FLV 转码。
- 默认选流优先 AVC，其次 HEVC，最后 AV1；显式传入 `codec` 时只使用该编码，缺失则返回
  `reason:"hls_unavailable"`。
- 风控（上游 `-352`）返回 `reason:"risk_control"`；其他上游异常返回 `reason:"upstream_error"`。
- 每次调用都会创建新会话；切换清晰度或房间后应停止旧会话：
  `{"jsonrpc":"2.0","method":"stream.stop","params":{"session_id":"<session>"}}`。
- `/l/` 网关只监听 `127.0.0.1`，分片代理只允许 B 站 CDN 域名（`*.bilivideo.com`、
  `*.bilivideo.cn`、`*.hdslb.com`、`*.acgvideo.com` 等），其他地址返回 403。
- 会话默认保留 30 分钟；访问播放列表会续期，但不会超过上游签名 URL 的 `expires` 时间。

### 9.2 播放器辅助方法

`player.resolve` 聚合详情、分 P、清晰度、音频、字幕和续播点，宿主不需要分别调用
`video.info` / `video.playurl` / `video.play_info`。

```json
{"jsonrpc":"2.0","id":9,"method":"player.resolve","params":{
  "bvid":"BV1atse6EEfx","page":1,"qn":80,"codec":"hevc"
}}
```

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `bvid` / `aid` | 二选一 | 视频标识 |
| `cid` | 否 | 指定分 P；省略时按 `page` 获取 |
| `page` | 否 | 分 P 序号，默认 1 |
| `qn` | 否 | 目标清晰度，默认 80；返回不高于该值的 `default_qn` |
| `codec` | 否 | 默认编码偏好，默认 `hevc` |
| `account` | 否 | 账号引用 |

响应：

```json
{"bvid":"BV1atse6EEfx","aid":113000000000000,"cid":43750131546,"page":1,
 "title":"…","page_title":"P1","duration_ms":1266000,
 "pages":[{"page":1,"cid":43750131546,"part":"P1","duration_ms":1266000}],
 "qualities":[{"qn":80,"label":"1080P","codecid":12,"codec":"hevc",
               "codecs":"hvc1.1.6.L150.90","width":1920,"height":1080,
               "bandwidth":922000,"frame_rate":"30"}],
 "audio":[{"id":30280,"codecs":"mp4a.40.2","bandwidth":179000}],
 "subtitles":[{"lan":"zh-CN","label":"中文（中国）","url":"https://…","id":"…"}],
 "resume_ms":0,"last_play_cid":0,"default_qn":80,"default_codec":"hevc",
 "playable":true,"is_preview":false}
```

- `qualities` 按清晰度降序、同清晰度按 HEVC/AVC/AV1 排序；`audio` 按码率降序。
- `resume_ms` 只在 `last_play_cid` 与当前 `cid` 相同时返回。
- `subtitles[].url` 已校验为 B 站 HTTPS 域；可直接交给 `video.subtitle_vtt`。
- `playable=false` 表示没有 DASH 流（例如预览或权益不足），宿主应展示提示而不是继续 `stream.start`。

`player.progress` 上报播放进度；匿名账号返回 `{"reported":false,"reason":"auth_required"}`，
不发起上游请求。

```json
{"jsonrpc":"2.0","id":10,"method":"player.progress","params":{
  "bvid":"BV1atse6EEfx","cid":43750131546,"progress_ms":125000,"event":"heartbeat"
}}
```

`event` 可为 `start` / `heartbeat` / `pause` / `seek` / `end`；`end` 时 `played_time` 固定为
`-1`。建议 15 秒心跳一次，暂停、seek、关闭时各补报一次。

`video.subtitle_vtt` 拉取字幕 JSON 并转换为 WebVTT：

```json
{"jsonrpc":"2.0","id":11,"method":"video.subtitle_vtt","params":{"url":"https://…"}}
```

返回 `{"content_type":"text/vtt","cue_count":392,"vtt":"WEBVTT\n\n…"}`。只允许
`*.hdslb.com`、`*.bilibili.com`、`*.bilivideo.com` 的 HTTPS 地址，避免把核心当成任意 URL 代理。

历史管理：

| 方法 | 参数 | 说明 |
| --- | --- | --- |
| `history.delete` | `kid` 或 `kids` | 删除单条/多条历史 |
| `history.clear` | — | 清空历史 |
| `history.pause` | `pause`，默认 `true` | 暂停/恢复记录 |
| `history.status` | — | 查询暂停状态 |

这些方法与写接口一样遵循账号引用，建议只在测试账号上验证。

### 9.3 下载与离线播放

`download.*` 把一路 UGC DASH 流保存到数据目录的 `downloads/<job_id>/`：`video.mp4`、
`audio.mp4`（完整 fMP4）以及本地 `master.m3u8` / `video.m3u8` / `audio.m3u8` 和 init 段。
播放列表只含相对路径与字节范围，不包含上游签名 URL；完成后可直接用 `/d/` 网关离线播放，
重启核心后仍然有效。

```json
{"jsonrpc":"2.0","id":12,"method":"download.start","params":{
  "bvid":"BV1atse6EEfx","cid":43750131546,"qn":80,"codec":"hevc",
  "title":"…","cover":"https://…","owner":"…","duration_ms":1266000
}}
```

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `bvid` | 是 | 视频 BV 号 |
| `cid` / `page` | 否 | 分 P；省略时按 `page` 解析 |
| `qn` / `codec` / `audio_id` | 否 | 与 `stream.start` 相同，默认 1080P HEVC + 最高 AAC |
| `title` / `cover` / `owner` / `duration_ms` | 否 | 列表展示用的元数据，由宿主传入 |

返回任务快照：

```json
{"id":"<16 位十六进制>","bvid":"BV1…","cid":43750131546,"title":"…",
 "status":"queued","downloaded_bytes":0,"total_bytes":0,
 "qn":80,"codec":"hevc","has_audio":false,"created_at":1790000000000}
```

| 方法 | 参数 | 说明 |
| --- | --- | --- |
| `download.list` | — | `{items, summary, used_bytes, free_bytes, disk_bytes}` |
| `download.status` | `id` | 单个任务；完成后附 `master_url` |
| `download.pause` | `id` | 暂停，保留已下载字节 |
| `download.resume` | `id` | 重新解析签名 URL 并从断点续传 |
| `download.remove` | `id` | 取消任务并删除文件 |
| `download.url` | `id` | 仅完成任务：`{master_url, duration_ms}` |
| `download.config` | `concurrency`, `limit_kbps` | 读取/更新并发（1–8）与全局限速（KB/s，0 为不限），返回当前配置 |

- 下载由核心在后台进行，同一视频同一清晰度重复 `start` 返回现有任务；默认并发 1，
  可用 `download.config` 在运行时调整。
- 进度事件经事件总线的 `download.progress` / `download.completed` / `download.failed`
  推送，`data` 为任务快照；宿主也可以用 `download.list` 轮询作为兜底。
- 断点续传使用 HTTP Range；上游签名过期导致失败后，`download.resume` 会重新
  `video.playurl` 并重建播放列表。清晰度或编码发生变化时会丢弃旧视频文件重新下载。
- `/d/` 只接受 16 位十六进制任务 ID，不暴露任意文件；播放列表和 fMP4 支持 Range 请求。
- 下载不绕过权益或 DRM；仅保存当前账号实际可解析的 UGC DASH 流。

### 9.4 投屏（DLNA）

`cast.*` 用 SSDP 发现局域网内的 DLNA MediaRenderer（云视听小电视、电视盒子、投影等），
并让电视播放当前内容。UGC 视频默认走**直链模式**：核心用 html5 平台解析出完整 MP4 durl，
该地址不需要 Referer/UA 就能直接下载，电视自己从 B 站 CDN 拉流，核心进程不参与播放，
关掉宿主 App 也不影响。番剧和直播仍走**代理模式**：核心在手机/电脑上开一个监听局域网
地址的 HLS 网关，电视从网关拉流；这种模式必须保持进程存活。

```json
{"jsonrpc":"2.0","id":21,"method":"cast.discover","params":{"timeout_ms":3000}}
```

```json
{"jsonrpc":"2.0","id":22,"method":"cast.play","params":{
  "control_url":"http://192.168.50.204:22466/bilibili/AVTransport/<udn>/control.xml",
  "device_id":"uuid:…","device_name":"我的小电视",
  "bvid":"BV1atse6EEfx","cid":43750131546,"qn":80,
  "title":"…","position_ms":0,"mode":"auto"
}}
```

```json
{"jsonrpc":"2.0","id":23,"method":"cast.stop"}
```

| 方法 | 参数 | 说明 |
| --- | --- | --- |
| `cast.discover` | `timeout_ms` 可选 | 默认 3000，范围 800–8000；返回 `{devices:[…]}` |
| `cast.play` | 见下 | 解析内容并让电视播放；返回 `mode` |
| `cast.stop` | — | 停止电视、关闭网关；`{stopped}` |
| `cast.status` | — | 无会话时 `{active:false}`，否则含 `mode/kind/title/device` |

`cast.play` 的设备字段来自 `cast.discover`：`control_url` 必填，且只能是私有/链路本地
地址上的 HTTP(S) 端点——不接受公网或任意主机名。内容四选一：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `bvid`（+`cid`/`page`/`qn`） | UGC | 直链模式，html5 平台最高 720P |
| `season_id` / `ep_id`（+`qn`/`codec`） | PGC | 代理模式，与 `pgc.play` 参数一致 |
| `room_id`（+`qn`） | 直播 | 代理模式，与 `live.play` 参数一致 |
| `title` / `position_ms` | 否 | DIDL 标题与起播位置（毫秒） |
| `mode` | 否 | `auto`（默认）、`direct`、`proxy` |

`mode` 语义：

| 值 | 行为 |
| --- | --- |
| `auto` | UGC 先尝试直链，失败则回退代理；PGC/直播直接代理 |
| `direct` | 只允许 UGC；没有直链时报错，不回退 |
| `proxy` | 所有内容都走局域网 HLS 网关 |

成功返回（直链模式）：

```json
{"playing":true,"mode":"direct","kind":"video","title":"…",
 "device":{"id":"uuid:…","name":"我的小电视","host":"192.168.50.204"},
 "started_at":1790000000}
```

代理模式额外返回 `session_id` 和 `playback_url`（局域网网关地址，不含上游签名 URL）。

- 同一时间只有一个投屏会话；新的 `cast.play` 会先停止电视并关闭旧网关。
- 直链模式的 CDN 地址约 2 小时过期；过期后需重新 `cast.play`。
- 电视播放期间 `GetTransportInfo` 可能一直报告 `NO_MEDIA_PRESENT`，不要用它判断投屏状态，
  以 `cast.status` 为准。
- 代理模式的 DIDL 媒体类型是 `application/vnd.apple.mpegurl`，直链模式是 `video/mp4`；
  电视若不支持对应格式则该机型无法投屏，不做转码。
- 代理模式下核心退出或宿主进程结束会终止网关，电视随即断流。移动端因此用前台服务
  （Android `mediaPlayback`）保持核心存活；只是回到桌面或划掉 App 界面不影响投屏，
  强行停止 App 会中断。直链模式没有这个限制。

## 10. 番剧、专栏与话题

### 10.1 番剧播放

`pgc.play` 把番剧/影视剧集解析成和 UGC 相同的 HLS 会话，复用 `/p/` 网关。与 `stream.start`
的区别是先读取 `pgc.info` 取得剧集元数据，再用 `pgc.playurl` 拿 DASH，最后按 `qn`/`codec`
选择轨道。只有 DASH 流可播放；FLV/DRM 一律返回 `hls_unavailable`，不做转码或绕过。

```json
{"jsonrpc":"2.0","id":9,"method":"pgc.play","params":{
  "season_id":33378,"ep_id":321808,"qn":80
}}
```

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `season_id` / `ep_id` | 二选一 | 季度或剧集 ID；只传 `ep_id` 时会解析所属季度 |
| `cid` | 否 | 指定剧集 cid；省略时按 `ep_id` 或第一集解析 |
| `qn` | 否 | 目标清晰度，默认 80 |
| `codec` | 否 | `hevc`（默认）或 `avc` |
| `try_look` | 否 | 请求会员试看片段 |
| `account` | 否 | 账号引用；付费内容需要对应权益 |

响应（可播放时）：

```json
{"playable":true,
 "session_id":"<32 位十六进制>",
 "playback_url":"http://127.0.0.1:<port>/p/<session>/master.m3u8",
 "expires_at":1790000000,
 "duration_ms":1493182,
 "is_preview":false,
 "season":{"season_id":33378,"media_id":...,"title":"名侦探柯南","cover":"...","total":1284},
 "episode":{"ep_id":321808,"cid":26835486927,"title":"1","long_title":"云霄飞车杀人事件","duration_ms":1454000},
 "episodes":[{"ep_id":321808,"cid":26835486927,"label":"1","long_title":"云霄飞车杀人事件"}],
 "quality":{"qn":80,"label":"高清 1080P"},
 "qualities":[{"qn":80,"label":"高清 1080P","codecs":["hevc","avc"]}],
 "video":{"id":80,"codecid":12,"width":1440,"height":1080},
 "audio":{"id":30280,"codecs":"mp4a.40.2"}}
```

不可播放时返回 `playable:false` 与 `reason`：

| reason | 含义 |
| --- | --- |
| `hls_unavailable` | 没有可用的 DASH 流 |
| `entitlement_required` | 需要大会员/付费权益 |
| `region_limited` | 地区限制 |
| `auth_required` | 需要登录 |
| `not_found` | 剧集不存在 |
| `upstream_error` | 上游其他错误 |

`is_preview:true` 表示当前 PlayView 返回的是会员试看窗口。会话停止同样使用
`stream.stop`。

### 10.2 发现类方法

| 方法 | 参数 | 说明 |
| --- | --- | --- |
| `video.weekly` | `number` 可选 | 省略返回每周必看期数列表，指定期号返回当期内视频 |
| `article.rank` | `category_id, type, page, page_size` 可选 | 专栏排行榜；上游一次返回完整榜单 |
| `article.view` | `article_id`（或 `id`） | 专栏正文与作者信息 |
| `article.info` | `article_id` | 专栏统计信息 |
| `topic.detail` | `topic_id` | 话题详情与置顶内容 |
| `topic.feed` | `topic_id, offset, page_size` | 话题动态流；`offset` 用响应中的游标 |
| `topic.square` | `page, page_size` | 推荐话题列表 |

这些方法返回上游原始结构（`{code,message,data|result}`），宿主按需取字段。

## 11. gRPC

### 11.1 原始调用

```json
{"jsonrpc":"2.0","id":20,"method":"grpc.call","params":{
  "path":"/bilibili.community.service.dm.v1.DM/DmSegMobile",
  "payload_base64":"CAAS...",
  "headers":{"x-custom":"1"},
  "compress":true,
  "account":"active",
  "meta":false
}}
```

`compress` 省略时按 >64 字节自动 gzip。响应：

```json
{"status":200,"grpc_status":0,"grpc_message":"","message_base64":"..."}
```

- 业务状态在 `grpc_status`（0 成功），不要只看 HTTP `status`。
- 认证头（access_key、buvid、mid、device 元数据）由账号自动生成。
- 底层为 HTTP/2 POST `https://app.bilibili.com<path>`，`application/grpc`，5 字节帧。

### 11.2 类型化方法

| 方法 | 参数 | 说明 |
| --- | --- | --- |
| `danmaku.seg` | `cid, segment_index, type` | 请求编码 + 响应解码为 `{state?, elems:[...]}` |
| `danmaku.seg_raw` | 同上 | 响应仅 `message_base64` |
| `grpc.reply_main` | `oid, type, mode, offset, page_size` | 评论主列表；请求已编码，响应为原始 protobuf |

`danmaku.seg` 的元素字段：`id`、`progress`、`mode`、`fontsize`、`color`、`ctime`、`weight`、
`pool`、`attr`、`like_count`、`colorful`、`type`、`oid`、`dm_from`、`count`、`mid_hash`、
`content`、`action`、`id_str`、`animation`、`extra`。

## 12. 生命周期

1. 宿主 spawn 进程，可选传入 `--data-dir`。
2. 进程初始化匿名 buvid（失败则本地生成），加载 `state.json`。
3. 输出 `stderr` 日志 `bbcore ready ... methods=<n>` 后开始读取 `stdin`。
4. 宿主随时可调用方法；事件通过通知推送。
5. 关闭方式：
   - 关闭 `stdin`（EOF）：等待在途请求结束、保存状态、退出码 0。
   - `core.shutdown`：返回 `{ok:true}` 后停止后台任务并退出。
   - `SIGINT` / `SIGTERM`：同上。

宿主应把 `stderr` 当作日志流消费，避免管道写满阻塞进程。

## 13. 版本与兼容

- `core.version` 返回 `{name, version, protocol}`，当前 `protocol=1`。
- 同一 `protocol` 内只新增方法/字段，不改变既有语义。
- 二进制版本通过 `-ldflags "-X bbcore/internal/service.Version=..."` 注入。

## 14. 完整示例

```json
{"jsonrpc":"2.0","id":1,"method":"core.endpoints","params":{"q":"playurl"}}
{"jsonrpc":"2.0","id":2,"method":"video.info","params":{"bvid":"BV1atse6EEfx"}}
{"jsonrpc":"2.0","id":3,"method":"video.playurl","params":{"bvid":"BV1atse6EEfx","cid":43750131546,"qn":80,"account":"active"}}
{"jsonrpc":"2.0","id":4,"method":"reply.list","params":{"oid":117644908825124,"type":"1","pn":1,"ps":20}}
{"jsonrpc":"2.0","id":5,"method":"danmaku.seg","params":{"cid":43750131546,"segment_index":1}}
{"jsonrpc":"2.0","id":6,"method":"bili.msgFeedUnread","params":{}}
{"jsonrpc":"2.0","id":7,"method":"stream.start","params":{"bvid":"BV1atse6EEfx","qn":80,"codec":"hevc"}}
```
