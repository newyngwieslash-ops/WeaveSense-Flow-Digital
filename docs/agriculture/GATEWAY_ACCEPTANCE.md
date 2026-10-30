# DT-014 同域网关验证

## 已执行的真实验证

2026-10-30，CI scaffold 提交 `512e963e97cc8282cfcd9c59045cf78ade0a331e` 的
[gateway run 41007114373](https://github.com/newyngwieslash-ops/WeaveSense-Flow-Digital/actions/runs/41007114373)
已完成真实 Go 构建、React/Vue 农业生产构建、Nginx/Chromium 浏览器闭环，以及独立 Docker Web 镜像构建/静态检查。

原始安全 JSON 与核对记录位于 `evidence/w01/ci-512e963-20261030/`：

- 浏览器 15 项检查全部通过：真实测试账号注册及组织创建、精确深链接登录回跳、注册取消/返回、刷新新文档、跨应用导航/Back 的新工作区请求、退出/重新登录、外部工作区拒绝、未知路由 404。
- 71 个观察到的网络请求中，内部凭证、跨域请求及浏览器异常违规记录为 0；不是只执行了测试 helper。
- 独立 Docker smoke 的 16 项检查通过，包括静态路由和上游不可用 502；该 smoke 不声称经过认证的容器端到端测试。
- 会话 44 项、组织浏览器 62 项、平台 PostgreSQL 5 主测试及一个子测试、旧 Flow 21 项、身份 Go 15/Python 75/actor 48 项回归通过。以各原件为准，不把同名测试数相加为需求完成率。

## 来源绑定

CI 从固定 Flow/Agri 来源组装产品目录，211 个 overlay 文件的 SHA256 已逐项与本地冻结提交
`53ddad0536351b77a2acb2eb26d0839f4644c26c` 核对，全部匹配。
浏览器原件绑定的 provenance 摘要为
`c535b54d8ff4ae6b90f932ebf9f714658617ba18059b4da4df0548fa95833d05`。

组装目录并非完整产品 Git checkout；原件 `checkout_head` 为空、`working_tree_changes_included=false`
不能推导完整产品历史已发布。这里使用固定来源、211 文件精确映射与运行提交建立来源关系。
浏览器所用官方 Ubuntu Nginx 1.24.0 与 Docker Nginx 1.27 分开记录，不声称二进制相同。

## 首次回归失败及修复

同批 W00 run `41007114333` 的旧 Compose 测试把新增 Web 的唯一回环端口也一概拒绝：
93 项通过、1 项失败，依赖的两个 Compose job 跳过。因此原 `reconciliation.json` 保留
`whole_batch_passed=false`，不得改写为全绿。

修复提交 `856af8f3682b9ee2abe20c7775c6d3871fc68746` 只改安全策略测试，精确允许农业
Web 及既有 readiness 的回环模板；数据库、Go/GIS 等服务的直接 host 端口仍禁止。
新增公网、错误目标/环境变量/默认端口、额外映射、长语法及私有服务暴露负例，相关本地测试
102 项通过。修复后的 W00 run `41008181499`（CI 提交
`91c76b89dc1bc0f8299f75c065c250b1e9cfb2a0`）contracts、core、migrations 三个 job
均终态 success；只有该测试文件改变，211 项运行源码未变。新运行证据另行记录，原失败证据保留。

## 验收边界

真实浏览器测试使用明确的本地 SQLite fixture，不替代 PostgreSQL RLS、真实地块/影像、W03
领域映射或商业试点。没有生产部署、镜像推送、付费、外部消息或设备操作。
DT-015 的 Cookie/CSRF/Origin 强化仍是下一任务，不能由本次网关验收提前计为完成。

任务状态仅在必要回归和证据冻结后由规格 `tasks.json`、`RECOVERY_STATUS.json` 与
`STATUS.md` 同步更新；本报告不独立改变完成状态。
