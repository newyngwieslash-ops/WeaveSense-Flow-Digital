# DT021：范围化读缓存与作业事件流

## 已实现的边界

`ScopedProjectionCache` 包装现有 `platform.BoundedReadCache`，不是第二套队列。只接受已知 `.read` 能力及 `search/map/tool` 三类服务端投影。参数经既有严格 canonical JSON 校验；键包含实际 actor、tenant、workspace、action、资源及其版本、农场范围、权限 epoch、policy、投影版本、结果版本、类别和完整参数。容量256项、8MiB、4个并发回源，TTL30秒、单值1MiB。返回副本避免调用方修改缓存。

每次命中、加载完成以及返回前重新获取可信主体、执行真实 Service.Authorize，并核对 provider 独立查询得到的范围/表示版本。可能等待的 Describe 之后再次检查会话与 Reauthorize；撤权期间完成的回源不返回原结果。缓存的权限版本不是服务端鉴权替代品。

`GET /api/agri/v1/workspaces/:workspace_id/jobs/:job_id/events` 经平台现有 cookie session 和真实 `agri.analysis.read`/job 授权。`JobEventProvider` 是固定服务端适配器，必须独立查明领域任务、主体可见投影与持久事件；不从票据/浏览器复制归属作为证明。生产未配置 W06 provider 时返回503，不生成演示事件。旧影视入口不因此开放农业数据。

## 游标及撤权

Last-Event-ID 是 purpose-separated HMAC 游标：完整授权范围散列、稳定 stream generation、投影、固定过滤器、单调序号、5分钟到期。客户端游标不能决定查询租户。缺失为初始读取；伪造/跨主体/跨租户/跨任务/权限变化/过期拒绝。追加事件不改变 generation，因此可以正常断线恢复；重建/清理造成旧窗口无效时适配器必须返回冲突，客户端重新取快照。每实例必须注入同一独立32字节共享密钥，不能临时随机生成然后声称多副本可重连。缺 provider/key 构造失败；没有新增生产秘密默认值。

每次发送整个敏感 batch 前重新核验当前会话、权限、领域归属与表示版本；任何变化终止旧连接，不继续发敏感事件。单批32条、每条16KiB、严格JSON对象和递增序号，整批先验证再输出。连接30秒主动结束、最多32个并发连接、默认1秒轮询；上下文取消传入 provider。要求 provider 遵守取消且使用短事务；平台不在网络等待/发送期间持有数据库事务。写入deadline2秒，当前生产Gin支持底层ResponseController；不支持deadline的wrapper直接拒绝，不宣称其限时有效。重连仍重新鉴权。服务端已送达客户端的数据不能追回。

私有响应 no-store、Vary Cookie，不进入公共缓存。不得把该组件称为已上线的知识检索、地图引擎、Agent工具或持久调度器：这些领域适配器仍待对应工作包。

## 证据边界

专用测试使用真实 SQLite 账号/组织/成员/授权记录、真实 auth.Login cookie、真实HTTP及持久SQLite测试事件表。覆盖双租户同任务ID、多主体投影、重连、过期/伪造游标、追加序号、已建立订阅撤权/logout、批读取后撤权、断连取消、缓存命中与回源竞态。GIS ownership 在这些组件测试中是明确测试适配器，不是生产农场；不能据此声称 PostgreSQL RLS 或 W06 实际队列已经完成。
