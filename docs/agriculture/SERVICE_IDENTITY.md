# 平台与GIS受信调用（DT012实施合同）

状态：实施中，尚未验收。DT011证据不能代替本合同的签名、跨语言、重放及真实HTTP验证。

## 固定协议

- 使用Ed25519紧凑JWS，固定header字段`alg=EdDSA`、`typ=weavesense-agri-delegation+jws`、`kid`；不接受其它算法、额外header、token提供的jku/x5u或自动取钥。密钥由运行配置提供，测试固定私钥只存在明确测试目录；不生成生产默认秘密。
- 紧凑JWS最多192KiB、解码claims最多128KiB、解码header最多2KiB；严格base64url，无填充或非规范编码，超限明确拒绝，不截断资源范围。
- 业务claims沿用delegation.schema.json，schema_version=1.0，issuer/subject=flow-platform，audience=agri-gis。actor代表真实用户，ID允许平台原始数字开头不透明ID；tenant/workspace/resource规则不随之放宽。scopes首轮精确一个DT011动作。
- 增加policy_version、farm_ids和resource_versions，直接来自重新验证后的DT011决策，不能从浏览器claims透传。resource_versions逐项为`{resource:{kind,id},revision}`。农场集合与资源集合去重、规范排序、完整匹配。
- UTC RFC3339秒级issued_at/expires_at；TTL最多30秒且不超过决策/授权到期。验证未来签发偏差最多5秒，但过期不宽限；配置密钥有效期和撤销状态也必须成立。短期不代替当前权限复核。
- 请求精确绑定method及规范原始path；禁止任何非空query、百分号编码、点段、重复斜杠或路径归一化歧义。写方法POST/PUT/PATCH/DELETE绑定operation_id和规范JSON载荷SHA256。GET/HEAD要求空body。内部票据只由Go传给GIS，不返回浏览器、模型上下文或日志。
- canonicalization_version冻结为`rfc8785-safe-v1`，调用经过固定来源审查的JCS实现，不自行实现浮点格式或Unicode排序。请求JSON顶层object，最多1MiB、深度32、单对象最多256键、总节点最多10000；严格UTF8/JSON语法、无重复解码键、无孤立surrogate、无非finite值。数值在转换binary64前按原始十进制token限制绝对值不超过9007199254740991，三种等价整数写法不能绕过；允许范围内有限GIS小数，账本/精确decimal仍用整数或字符串。保留Unicode原样，不规范化。

## 双向服务边界

平台签发函数只接收可信主体及重新授权结果，生产调用链为会话认证→DT011授权→内部签发→受控HTTP→GIS，不新增公共mint接口。GIS再次验证签名、请求、当前主体/epoch、全部资源归属、revision及farm集合，能力还受固定路由允许动作限制。

业务路由必须显式提供资源提取器：从该handler实际使用的路径参数/已验载荷提取完整资源集合，不从票据claims反推请求目标。提取集合规范化后必须与签名resource_scope完全相等；缺少提取器属于配置错误，不提供自动fallback。即使签名及精确请求路径均有效，路径指向B而授权只覆盖A也拒绝。该边界不代替后续真实领域数据查询与对象归属检查。

归属解析先于用户最终授权，需要独立只读服务能力`ownership.resolve`：固定接口、已验会话推导tenant/workspace、最多200明确引用，只返回归属/版本/状态/最小farm集合，不返回几何/影像/名称或秘密。该服务凭据不能用于普通业务读写。

GIS向平台查询当前主体/epoch使用独立服务凭据、受众与动作，仅查询平台当前user/tenant/member/workspace状态并比较epoch；不得复用aud=agri-gis的业务票据，亦不再次调用Authorize或OwnershipResolver形成循环。超时/缺依赖拒绝，不使用有撤权延迟的缓存冒充即时失效。

两种服务请求使用与业务delegation不同的固定JWS typ，专用允许调用方和audience，精确绑定method/path/body/tenant/workspace；不能由客户端任意选择issuer、subject或扩充作用域。各方向密钥独立、可轮换及明确撤销。

签名请求不自动认证未签名响应的服务器。非本机GIS与平台回调必须HTTPS且验证证书，禁止重定向；HTTP仅允许无zone的字面loopback IP本机开发例外，不把localhost域名或普通私网地址当服务器身份。Go仍额外执行平台现有精确私网allowlist和连接时DNS策略；不修改全局出站策略，不关闭TLS验证。部署模板只含不可用占位值，实际密钥从受控挂载配置提供。

服务assertion固定typ为`weavesense-agri-service+jws`，机器合同见`service-request.schema.json`。两条POST服务路由只读：ownership.resolve正文为`{tenant_id,workspace_id,resources}`，响应`{resources:[归属DTO]}`；authority.check正文为`{actor_user_id,tenant_id,workspace_id,authorization_version}`，响应仅`{active:true,authorization_version}`。它们的jti用于关联，不占用业务一次性nonce，也不构成写执行授权；业务delegation不能当成service assertion使用。

## 重放、装配与验收边界

敏感写请求在所有验证之后，由GIS拥有的PostgreSQL唯一nonce记录原子抢占jti；并发/重复拒绝，存储不可用拒绝。验签或授权失败不消耗nonce；已成功抢占后进程崩溃仍不能重用旧票据，需新的短期票据。新票据不免除相同operation_id的后续业务幂等，不能声称业务exactly-once。

nonce数据库等待可能跨过有效期，故抢占后交付handler前再次检查过期；已提交nonce不因过期异常释放。当前权限回调不缓存，但也不声称跨数据库原子取消已经授权并开始执行的操作；后续长任务仍需独立续权、撤销与fencing机制。

农业生产profile不注册未改造旧GIS路由。未配置真实OwnershipProvider的内容请求503；测试fixture不能由普通环境开关在生产启用。真实Go→FastAPI HTTP测试以测试进程装配的fixture对象证明签发/验证链和负例，生产前端不获得票据。真实农场领域、全部旧端点身份迁移、全GIS RLS分别留给W03、DT013、DT017；完整AG-AC-006仍未验收。
