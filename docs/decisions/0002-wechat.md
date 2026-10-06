# 0002 微信 iLink Bot 接入验证（G0）

- 状态：已确认（2026-10-06 G0 通过），作为 G2a.1/G2a.2 的输入
- 日期：2026-10-06
- 关联：`docs/decisions/0001-scope.md`、`docs/phase-01-weixin-agent.md` §2「微信接入」
- 免责：以下是对**参考实现**的核对，**尚未**用本项目与用户账号实测。真实扫码见 G2a.1，真实收发见 G2a.2。

## 1. 参考实现锁定

| 项 | 值 |
| --- | --- |
| 仓库 | `NousResearch/hermes-agent` |
| 文件 | `gateway/platforms/weixin.py` |
| blob SHA（`main`，2026-10-06） | `4d83a0b983716044ac9a9336d55528ec5ebe9815` |
| 大小 | 71,948 字节 / 1,243 行 |
| 最后改动该文件的提交 | `9bcbe7b5df9a5c1eef7063ce06c4c2cdaae2f82e`（2026-09-28，i18n 批量改动） |
| 最后的微信专属修复 | `73dc249ccada560f64a694154269d1dcabfdad67`（2026-09-22，`fix(weixin): persist the long-poll cursor off the event loop, only when it moves`） |
| 官方文档 | https://hermes-agent.nousresearch.com/docs/zh-Hans/user-guide/messaging/weixin |

**`main` 是可变分支，本节只是核对快照，不得当作永久协议规范。** 字段、鉴权、状态或错误处理若与线上不符，以实测为准并在 G2a 更新本文件。实现时在代码注释里引用上面的 blob SHA。

## 2. 已核对接口契约

### 2.1 常量

| 常量 | 值 |
| --- | --- |
| iLink Base URL | `https://ilinkai.weixin.qq.com` |
| 微信 CDN Base URL | `https://novac2c.cdn.weixin.qq.com/c2c`（首版不用） |
| `ILINK_APP_ID` | `bot` |
| `CHANNEL_VERSION` | `2.2.0` |
| `ILINK_APP_CLIENT_VERSION` | `131584` = `(2<<16)|(2<<8)|0` |

### 2.2 端点

| 端点 | 方法 | 用途 | 首版 |
| --- | --- | --- | --- |
| `ilink/bot/get_bot_qrcode?bot_type=3` | GET | 请求登录二维码 | ✅ G2a.1 |
| `ilink/bot/get_qrcode_status?qrcode={value}` | GET | 轮询扫码状态 | ✅ G2a.1 |
| `ilink/bot/getupdates` | POST | 长轮询收消息 | ✅ G2a.2 |
| `ilink/bot/sendmessage` | POST | 发送回复 | ✅ G2a.2 |
| `ilink/bot/getconfig` | POST | 取 `typing_ticket` | ❌ 不用 |
| `ilink/bot/sendtyping` | POST | 发送“正在输入” | ❌ 不用 |
| `ilink/bot/getuploadurl` | POST | 媒体上传地址 | ❌ 不用 |

超时（毫秒）：长轮询 `35000`、API `15000`、config `10000`、二维码 `35000`。

### 2.3 鉴权与请求头

POST（`_headers`）：

- `Content-Type: application/json`
- `AuthorizationType: ilink_bot_token`
- `Content-Length: <len>`
- `X-WECHAT-UIN: <base64(随机 4 字节大端整数)>`
- `iLink-App-Id: bot`
- `iLink-App-ClientVersion: 131584`
- `Authorization: Bearer {token}` —— **仅当 token 非空**

GET（`_api_get`）：**只带** `iLink-App-Id` 与 `iLink-App-ClientVersion`，不带 token。因此**二维码两个端点是免鉴权的**，登录 token 在扫码确认后才拿到。

所有 POST body 为 `{**payload, "base_info": {"channel_version": "2.2.0"}}`，JSON 序列化用 `ensure_ascii=False, separators=(",",":")`。

### 2.4 扫描登录状态机（`get_qrcode_status`）

`status` 取值与本项目映射：

| iLink status | 含义 | 本项目动作 |
| --- | --- | --- |
| `wait` | 未扫码 | 保持等待（打印进度） |
| `scaned` | 已扫码，待手机确认 | 提示“请在微信确认” |
| `scaned_but_redirect` | 需要切换登录域 | 用响应里的 `redirect_host` 改成 `https://{redirect_host}` 继续轮询 |
| `expired` | 二维码过期 | 重新请求二维码（参考实现最多刷新 3 次，超过则失败） |
| `confirmed` | 确认成功 | 保存凭据并结束 |

`confirmed` 响应字段：`ilink_bot_id` → 账号 ID，`bot_token` → token，`baseurl` → 服务地址（缺省回退 Base URL），`ilink_user_id` → 用户 ID。**账号 ID 或 token 缺失 = 失败，不得保存半份凭据。**

请求二维码响应字段：`qrcode`（十六进制 token）与 `qrcode_img_content`（liteapp URL）。**微信必须扫 `qrcode_img_content` 生成的码，不能扫裸 `qrcode` token。** 参考实现轮询间隔 1s，登录总超时 480s；终端用 `qrcode` 库 ASCII 渲染，渲染失败时至少打印 URL。

### 2.5 收消息（`getupdates`）

请求：`{"get_updates_buf": <游标字符串>}`（外加 `base_info`）。

响应字段：`ret`、`errcode`、`errmsg`/`msg`、`longpolling_timeout_ms`（>0 时更新下次轮询超时）、`msgs[]`、`get_updates_buf`（新游标）。

- **长轮询自然超时**返回合成空响应 `{"ret":0,"msgs":[],"get_updates_buf":<原游标>}` —— 这不是错误，**不计业务失败、不触发退避**。
- 每条消息字段：`from_user_id`、`to_user_id`、`message_id`、`msg_type`、`room_id`/`chat_room_id`、`context_token`、`item_list`。
- 条目类型：`ITEM_TEXT=1`、`ITEM_IMAGE=2`、`ITEM_VOICE=3`、`ITEM_FILE=4`、`ITEM_VIDEO=5`。文本在 `item_list[i].text_item.text`。

### 2.6 回复（`sendmessage`）

请求：`{"msg": message}`，其中 `message` =

```json
{
  "from_user_id": "",
  "to_user_id": "<对话方>",
  "client_id": "<幂等/关联 ID>",
  "message_type": 2,
  "message_state": 2,
  "item_list": [{"type": 1, "text_item": {"text": "..."}}],
  "context_token": "<对话方最近一次 token，仅非空时带上>"
}
```

`message_type=2`（MSG_TYPE_BOT）、`message_state=2`（MSG_STATE_FINISH）。响应读 `ret`/`errcode`/`errmsg`。

**每个出站回复必须回显该对话方最近一次的 `context_token`。**

### 2.7 成功判定与错误处理

成功：`ret ∈ {0, nil}` 且 `errcode ∈ {0, nil}`。

| 错误码/条件 | 含义 | 参考实现动作 | 本项目计划 |
| --- | --- | --- | --- |
| `-14` | 会话过期 | 轮询中暂停 600s；发送时清 token 重发一次 | 同；并向用户提示重新扫码/重连 |
| `-2` + errmsg ∈ {`unknown error`,`prepare failed`} | “会话未就绪”，**不是**限流 | 发送失败是确定性的，不重试，提示“用户需先给 bot 发消息或重新配对” | 同，记 `delivery_unknown` 类终止态 |
| `-2`（其他 errmsg） | iLink 频率限制 | 有界退避重试（`delay*attempt`，默认 4 次），发送侧 3× 延迟 | 同，计入限流退避指标 |
| 连续失败 3 次 | 网络/服务异常 | 退避 2s，满一批后 30s 并**回收轮询会话** | 同；因本机 TUN 代理（见 `0001-scope.md` §4）需要该策略 |
| HTTP 非 2xx | 传输错误 | 抛错并记录前 200 字节 | 记录状态与分类，不记录正文敏感内容 |

### 2.8 游标与上下文凭据的持久化

- 游标：`{account_id}.sync.json` → `{"get_updates_buf": "..."}`，原子写。启动时读回。
- **只在响应里游标为真且与当前不同才落盘**（空轮询会回显同一游标）。
- **顺序保证：先派发本批消息，再落盘游标。** 参考实现用注释明确说明：离线写是 `await`，若断线取消写盘，游标不会越过未投递的消息。
- `context_token`：`{account_id}.context-tokens.json`，扁平 `{user_id: token}` 映射，键 `account_id:user_id`；收到消息时写入，回复时读取，会话过期时清除。

### 2.9 去重

参考实现用 `MessageDeduplicator`，TTL 300s。本项目按计划用 **渠道实例 + 外部消息 ID** 组成去重键并落库（G2b），命名空间必须含渠道实例，避免与未来飞书/QQ 用户 ID 混同。

## 3. 首版边界（明确不做）

- 只做**文本私聊**；媒体（图片/语音/文件/视频）与 CDN 上传不纳入首版，`item_list` 只处理 `type==1`。
- 不做“正在输入”（`getconfig`/`sendtyping`）。
- 群聊字段 `room_id`/`chat_room_id` 读取但**不启用**（首版仅私聊，不以昵称充当身份键）。
- **不假定存在“创建联系人”API**；扫码后 bot 对象由平台创建/绑定，验收标准是“用户能在终端看到并与 bot 私聊”。
- 单 bot 账号只运行一个接收循环。

## 4. 模拟样例设计（fixture）

用可注入的 HTTP 客户端 + 录制/构造的响应验证，不消耗真实账号。文件建议放 `internal/channel/wechat/testdata/`。

### 4.1 G2a.1 登录 fixture

| 样例 | 构造 | 断言 |
| --- | --- | --- |
| `login_success` | QR 返回 `qrcode`+`qrcode_img_content`；status 序列 `wait,wait,scaned,confirmed{ilink_bot_id,bot_token,baseurl,ilink_user_id}` | 终态成功；凭据落盘且权限 0600；日志/trace 无 token 与二维码内容 |
| `login_expired_then_refresh` | `expired` ×1 → 重新取码 → `confirmed` | 自动刷新并成功 |
| `login_expired_exhausted` | `expired` ×4 | 明确失败，不无限刷新 |
| `login_timeout` | 一直 `wait` 直到登录 deadline | 超时终止，不写凭据 |
| `login_cancelled` | 等待中取消 | 无凭据写入，保留已有凭据 |
| `login_network_error` | status 接口 500 / 连接重置 | 重试后仍失败则终止，错误分类清晰 |
| `login_scaned_but_redirect` | 先 `scaned_but_redirect{redirect_host}` 再 `confirmed` | 后续请求打到新域名 |
| `login_confirmed_incomplete` | `confirmed` 缺 `bot_token` | 失败，**不**保存半份凭据 |
| `existing_credential_protection` | 已有有效凭据时重新登录中途失败 | 原凭据不被破坏（原子写） |

### 4.2 G2a.2 收发 fixture

| 样例 | 构造 | 断言 |
| --- | --- | --- |
| `poll_empty` | 连续 `{"ret":0,"msgs":[],"get_updates_buf":同值}` | 不计业务失败、不触发退避、游标不重复写盘 |
| `poll_single_text` | `msgs` 含一条 `type=1` 文本 | 标准化字段正确；`context_token` 被记录 |
| `cursor_moves` | `get_updates_buf` 变化 | 落盘新游标，且**在派发之后** |
| `duplicate_delivery` | 同 `message_id` 投递两次 | 只生成一个逻辑任务 |
| `poll_session_expired` | `ret=-14` | 暂停，不忙轮询；向上报“需重连” |
| `poll_rate_limited` | `errcode=-2`，errmsg 非 stale | 有界退避，重试次数受限 |
| `poll_stale_session` | `errcode=-2` + `prepare failed` | 按“会话未就绪”处理，不当作限流 |
| `poll_failures_recycle` | 连续 3 次网络错误 | 2s→30s 退避并回收轮询会话 |
| `send_ok` | `sendmessage` → `ret=0` | 记录发送成功与渠道返回 ID |
| `send_session_expired` | 先 `-14`，重发（去 token）成功 | 只重发一次，清掉缓存 token |
| `send_never_ready` | 去 token 后仍 stale `-2` | 确定性失败，进 `reply_failed`，提示需用户先发消息 |
| `crash_after_ingest` | 入站落盘后进程崩溃，游标未推进 | 重启重放命中去重键，**不重复执行 Agent** |

## 5. 待补齐的真实证据（不阻塞离线实现）

- G2a.1：真实终端二维码 → 用户扫码 → 手机确认 → 保存 → 重启恢复。
- G2a.2：至少 10 条真实文本消息闭环 + 脱敏回执与 trace 关联。
- 账号可用性、实际回复限制、异常返回：在接入单元实测；未取得真实收发证据前只标“模拟通过”。
