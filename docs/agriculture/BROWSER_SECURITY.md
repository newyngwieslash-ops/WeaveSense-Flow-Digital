# 农业浏览器安全边界（DT015）

本页记录实现，不代表浏览器、PostgreSQL 或生产已经验收。原影视/数学 profile 未启用
`FLOW_AGRICULTURE_ENABLED` 时保留原有行为；农业部署在启动时加载一次可信配置。

## 部署配置

- `FLOW_AGRICULTURE_EXTERNAL_ORIGIN`：唯一规范 HTTPS origin（可含明确端口），
  无路径、尾斜线、用户信息、query、fragment、通配符或来源列表。
- `FLOW_AGRICULTURE_ALLOW_HTTP_LOOPBACK_DEV=true`：仅显式本地开发允许
  `http://127.0.0.1:<port>` 或 `http://[::1]:<port>`；不允许 localhost/DNS 替代。
- `FLOW_AGRICULTURE_CSRF_KEY_FILE`：现存普通文件，恰好 32 个随机 raw bytes，
  权限 0600（或更严格），拒绝符号链接、目录、宽权限和长度错误。
  Compose 只读挂载到 `/run/secrets/agriculture-csrf.key`。所有平台副本使用相同 key；
  此 key 不复用 Flow/Loom JWT 或 GIS Ed25519/AES 密钥，不进入前端、仓库或日志。

三个配置缺失或无效均拒绝农业启动。模板故意为空，不是已经提供生产秘密。
开发者可在自己拥有的私有目录以 `os.open(..., O_CREAT|O_EXCL, 0o600)` 和
`secrets.token_bytes(32)` 创建文件；不可覆盖已有 key。TLS 终结可以位于可信网关，
但后端 Cookie 的 Secure 只取部署 origin，不从 Host、X-Forwarded-Host 或
X-Forwarded-Proto 猜测。生产 Cookie 保持 HttpOnly、SameSite=Lax、Path=/、无 Domain。

## 请求合同

`GET /api/auth/csrf` 返回现平台 envelope：
`{code:0,data:{token:<64位小写十六进制>},msg:"ok"}`，响应 no-store、Vary Origin/Cookie。
token 不是会话凭据，不能用于登录或查询身份。

已携会话 Cookie 时，token 是完整高熵 Cookie 经固定域分离的 SHA256；不返回原 Cookie。
失效/未知 Cookie 也可获取其绑定 token 后正常尝试登录，不能用 token 证明认证成功。
缺少会话时设置短期 HttpOnly preauth Cookie，结构包含服务端校验的 10 分钟有效期、
32 字节随机 nonce 和共享专用 key 的 HMAC。生产名使用 `__Host-`，开发使用独立名字。
它仅防匿名登录 CSRF，不创建账号、会话或主体。新登录或退出会清除 preauth Cookie；
携会话时绝不回退使用匿名 token。重复同名会话 Cookie 拒绝，避免认证与 CSRF 选择不同主体。

共享 `/api/auth/*`、`/api/agriculture/*` 的非 GET/HEAD 写请求必须有唯一精确 Origin
和唯一 `X-CSRF-Token`。其他 `/api/*` 携会话 Cookie 的写请求同样保护，避免共享管理
API 成为绕过路径；加 Authorization 头不会绕过 Cookie 保护。缺失、null、重复、组合、
协议/端口不同的 Origin 或错误 token 返回 403，reason 分别为 `origin_denied`、`csrf_invalid`。
前端每次写前重新获取 token，不持久化、不自动重放失败写。

农业 CORS 不使用原影视 wildcard/localhost fallback，带 Origin 的请求一律精确匹配配置。
GET 可无 Origin（普通同源 fetch 的实际行为），但不应有业务写副作用；CSRF GET 唯一
状态是匿名防伪 nonce，不修改业务。独立审计发现原 LinuxDO state+PKCE 仅绑定全局
数据库记录，没有绑定发起浏览器；仅单次 state 不能阻止攻击者转发自己的 callback URL。
农业 profile 因此关闭 `/api/auth/linuxdo/start`、`/api/auth/linuxdo/callback` 和
`/oauth/linuxdo/callback`，返回 503 / `oauth_browser_binding_unavailable`，不消费 state、
不设置会话、不跳转外部身份服务；认证设置也不显示该提供方。原影视 profile 行为及
OAuth 测试保留。正确浏览器绑定与真实渠道验收前不得重新启用；本轮不申请第三方凭据。
`/internal` 服务回调仍由独立签名认证，浏览器不可借路径获得授权。

## 失效与并发边界

真实会话每请求从数据库验证 token、期限及用户状态；退出/重置的已提交撤销在后续
请求生效，旧 Cookie 不能重新认证。新 Service/数据库连接仍读取相同持久事实。
CSRF key 多实例共享后匿名 token 可跨实例/重启；轮换后旧匿名 token 拒绝，重新获取即可。
会话 CSRF 依赖实际会话 Cookie，不因匿名 key 轮换独立失效。

农业关键写入重新查当前成员/角色/授权版本；降权后的旧页面请求、旧授权 decision
不能绕过该检查。服务票据签名最长 30 秒；角色变更须通过实时 authority/epoch 检查拒绝。
退出并不等于已签服务票据立即失效，也不承诺终止已开始计算或撤回已发出字节。
浏览器跨标签提示只触发重验，不传递主体或授予权限；后台暂停标签在恢复时重新验证。

验证证据由实际执行日志记录：Go 安全配置/请求负例、真实 SQLite 会话与第二服务、
共享 key 双实例，以及单独的真实浏览器和 PostgreSQL 降权验收。fixture 不是生产部署。

匿名 nonce 的签发 TTL 固定 600 秒；接收副本对未来上界允许最多 5 秒时钟偏差，
但 `expires <= 接收方当前时间` 一律拒绝，不延长已过期票据。运维仍须校时；测试以
注入时钟覆盖慢 5/6 秒、到期前及到期边界，无 sleep 依赖。

## 工作区权限输入快照

`GET /api/agriculture/workspaces` 中每个工作区增加 `authorityFingerprint`，为 64 位小写
十六进制，只用于 UI 后台重验时判断是否需要失效当前权限上下文。创建组织的立即响应
不包含此字段；客户端随后重新获取工作区列表。后端不接收该字段作为授权、版本或审批凭证。

摘要在原 `withWorkspace` 短事务中生成，复用 actor/member/workspace 活跃校验与规范角色。
固定版本 `platform-authority-inputs-v1` 纳入 actor、tenant、workspace ID、组织状态、
owner/治理布尔值、排序角色、实际 `authorizationPolicyVersion`，以及本工作区本人
active 且未过期资源绑定的 kind/id、严格解析并排序的 capabilities、expiry、receipt hash/revision。
能力未知、重复、格式损坏或数据库查询失败均 fail closed，不伪装为空权限快照。

不纳入 audit epoch、绑定 auth_version/数据库ID/更新时间、显示名称、其他成员或邀请。
因此单纯创建邀请不再周期性清除未提交草稿；本人角色、资源绑定撤销/到期或 receipt
改变则产生不同摘要。绑定/能力返回顺序不改变摘要。角色/成员失效后原真实写仍拒绝。

这不是 GIS 实时有效授权证明：列表刷新不批量请求 GIS ownership，未把 GIS 归属、未来
外业 assignment 或其他成员列表变化冒充为该摘要的覆盖范围。组织成员投影由 UI 独立
比较刷新，并只重置变化行；不能将其他成员变化混入本人摘要后又清除整个组织表单。
