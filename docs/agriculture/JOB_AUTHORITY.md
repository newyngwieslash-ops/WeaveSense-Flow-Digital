# DT-022：后台作业身份与范围侧表

这是作业身份契约与执行检查组件，**不是新增队列或已交付的 W06 调度器**。
上游影像 mosaic/registration/fusion 行是已完成的同步结果，不能冒充持久租约。

## 创建与续验

- Go `Service.NewJobAuthority` 只供可信平台组合代码调用。Actor 来自已验证调用者；
  输入参数只参与规范化 SHA-256，不提供身份。创建时执行真实 `Authorize`。
- `agriculture-job-authority/1` 固定记录 job/operation、actor、tenant、workspace、
  action、完整资源与 revision、farm IDs、授权版本、策略版本、输入 hash、创建时间。
- GIS `agriculture_job_authorities` 保存规范 JSON/hash；不可 UPDATE/DELETE；
  同 job 重试只有完全相同 hash 才幂等成功。不保存短期票据或私钥。
- 请求级 Ed25519 delegation 仍最多 30 秒。长作业每次由可信调度器持有的持久快照
  调用 `CheckJobExecution`，重新检查当前授权并签发新的精确 method/path/body 票据。
  不能让浏览器或未认证 worker 提交任意 actor/snapshot 来调用此组合方法。

## GIS 执行检查

固定内部路径：

- `POST /internal/agri/v1/jobs/authority/accept`
- `POST /internal/agri/v1/jobs/authority/checkpoint`
- GIS→平台独立服务身份：`POST /internal/agri/v1/jobs/authority/check`，
  capability 固定 `job.authority.check`，不能用于用户数据操作。

checkpoint body 只有 job ID、snapshot hash、worker ID、attempt、fencing、phase、
input hash；不接受 tenant/user/operator/role。先验真实平台票据，再使用其可信 scope
读取 GIS 持久快照，逐字段比对；重新检查独立资源归属/状态/revision。
`JobExecutionProvider` 必须从实际队列核对当前 worker/attempt/fencing/phase/state。
调用平台前后均检查租约，所有远程调用均在数据库事务外。

平台 callback 执行 **新的 Authorize**，不是只比授权 epoch；因此自然到期的授权也会拒绝。
epoch、policy、资源 revision 或完整归属变化采取保守拒绝；重试不得扩大范围。
成员撤销、资源撤回、旧租约、过期签名或缺少 provider 均不能取得成功检查。

成功只返回有期限的检查元数据，记录到 append-only checks 表；它不是结果写入权限
令牌。W06 的具体生产适配器仍须在实际发布事务中执行原子 fencing/CAS，并在每次
claim/read_input/publish 调用本检查。**不能宣称跨数据库检查与发布天然原子，不能
在检查后用未保护的普通写覆盖结果。** 30 秒是凭据上限，不是缓存授权的许可。

## 数据库与默认部署

- app：authority 表 SELECT/INSERT；checks 表 SELECT/INSERT。
- worker：authority 表只 SELECT；checks 表 SELECT/INSERT。
- 两表强制 RLS，tenant/workspace 缺失默认拒绝；禁止 UPDATE、DELETE、TRUNCATE、
  REFERENCES、TRIGGER。普通 ORM 的既有 99 张领域表权限不扩大。
- 运行连接须通过已有独立角色准入；侧表另行校验权限、RLS、trigger。
- 默认应用注册内部路由但执行 provider 未配置，合法身份仍得到 503；生产业务队列
  与农场/影像 provider 缺失不会伪装为任务成功。

## 验证口径

`native-postgres-test.py --phase jobs --go ... --python ...` 使用全新本地 PG17/PostGIS
集群，独立 migrate/app/worker；测试结束停止集群并删除 bootstrap 密码。
`test_real_go_python_job_authority_chain` 要求实际 Go 测试 root+四子项全部通过且不跳过：
Go SQLite 真实授权 → 签名 HTTP → FastAPI 验签 → PG sidecar/fixture lease →
反向平台 HTTP 当前授权。平台不是 PG；GIS lease 是明确的 test-only 持久 fixture，
不是 W06 调度器，不涉及真实影像计算或生产任务验收。

覆盖：创建、claim/read_input/publish、同快照新 attempt/fence、旧 fence、篡改 tenant、
替换 asset、过期票据、资源撤回、平台降权、失败无新增成功检查、缺 scope、连接池复用、
不可变快照及 worker 最小权限。纯 Go 另覆盖数小时后重新检查、自然 grant expiry
（无 epoch 变更）以及资源 revision 变化。
