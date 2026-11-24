# bbcore 协议说明（v1）

bbcore 是一个通过 **stdin/stdout** 与宿主进程通信的接口进程。协议基于 **JSON-RPC 2.0**，
传输层使用 NDJSON（每行一条消息）；输入侧同时接受 LSP 风格的 `Content-Length` 分帧。

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

### 1.3 并发

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
{"jsonrpc":"2.0","method":"event.live.message","params":{"room_id":6,"cmd":"DANMU_MSG","data":{...}}}
```

宿主应始终处理无 `id` 的消息。

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

随后推送：

| 方法 | 负载 |
| --- | --- |
| `event.live.connected` | `{room_id}`（WebSocket 认证成功；重连成功也会再次推送） |
| `event.live.disconnected` | `{room_id, reason}`（连接断开，自动重连前） |
| `event.live.popularity` | `{room_id, value}`（每 30 秒心跳回包） |
| `event.live.message` | `{room_id, cmd, data, danmaku?}` |

`data` 为上游原始消息对象。`DANMU_MSG` 系列额外附带摘要：

```json
{"room_id":6,"cmd":"DANMU_MSG","data":{...},
 "danmaku":{"text":"哈哈哈","uid":123,"uname":"用户","id":"2248...","dm_type":1,
            "fontsize":25,"color":16777215,"mode":1}}
```

同一房间重复 `live.connect` 返回现有连接信息。`live.disconnect {room_id}` 停止订阅；
`live.rooms` 列出当前订阅；进程退出或 `core.shutdown` 时全部关闭。

重连策略：1s 起指数退避，上限 30s，直到 `disconnect` 或进程结束。
协议：16 字节头（总长、头长、protover、opcode、sequence），支持 zlib（protover=2）
与 brotli（protover=3）压缩包，嵌套解压深度上限 4。

## 9. gRPC

### 9.1 原始调用

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

### 9.2 类型化方法

| 方法 | 参数 | 说明 |
| --- | --- | --- |
| `danmaku.seg` | `cid, segment_index, type` | 请求编码 + 响应解码为 `{state?, elems:[...]}` |
| `danmaku.seg_raw` | 同上 | 响应仅 `message_base64` |
| `grpc.reply_main` | `oid, type, mode, offset, page_size` | 评论主列表；请求已编码，响应为原始 protobuf |

`danmaku.seg` 的元素字段：`id`、`progress`、`mode`、`fontsize`、`color`、`ctime`、`weight`、
`pool`、`attr`、`like_count`、`colorful`、`type`、`oid`、`dm_from`、`count`、`mid_hash`、
`content`、`action`、`id_str`、`animation`、`extra`。

## 10. 生命周期

1. 宿主 spawn 进程，可选传入 `--data-dir`。
2. 进程初始化匿名 buvid（失败则本地生成），加载 `state.json`。
3. 输出 `stderr` 日志 `bbcore ready ... methods=<n>` 后开始读取 `stdin`。
4. 宿主随时可调用方法；事件通过通知推送。
5. 关闭方式：
   - 关闭 `stdin`（EOF）：等待在途请求结束、保存状态、退出码 0。
   - `core.shutdown`：返回 `{ok:true}` 后停止后台任务并退出。
   - `SIGINT` / `SIGTERM`：同上。

宿主应把 `stderr` 当作日志流消费，避免管道写满阻塞进程。

## 11. 版本与兼容

- `core.version` 返回 `{name, version, protocol}`，当前 `protocol=1`。
- 同一 `protocol` 内只新增方法/字段，不改变既有语义。
- 二进制版本通过 `-ldflags "-X bbcore/internal/service.Version=..."` 注入。

## 12. 完整示例

```json
{"jsonrpc":"2.0","id":1,"method":"core.endpoints","params":{"q":"playurl"}}
{"jsonrpc":"2.0","id":2,"method":"video.info","params":{"bvid":"BV1atse6EEfx"}}
{"jsonrpc":"2.0","id":3,"method":"video.playurl","params":{"bvid":"BV1atse6EEfx","cid":43750131546,"qn":80,"account":"active"}}
{"jsonrpc":"2.0","id":4,"method":"reply.list","params":{"oid":117644908825124,"type":"1","pn":1,"ps":20}}
{"jsonrpc":"2.0","id":5,"method":"danmaku.seg","params":{"cid":43750131546,"segment_index":1}}
{"jsonrpc":"2.0","id":6,"method":"bili.msgFeedUnread","params":{}}
```
