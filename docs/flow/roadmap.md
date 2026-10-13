# Roadmap P0–P5

Status: P0 delivered (main 6453c151). P1 core slice in PR `p1-tenants-rss` — see `p1-implementation.md` for scope and gaps.

Delivered as runnable vertical slices; directories, interface definitions or passing unit tests alone do not count as feature completion.

| Phase | Deliverables | Exit criteria |
|---|---|---|
| P0 基线 | 目标仓库 上游版本 决策 模板 核查 | origin 正确 无旧机器路径 有可复核提交 |
| P1 租户与 RSS | 双租户 空间 内部鉴权 手动与定时采集 | A B 隔离 去重正确 关网页仍采集 |
| P2 用户 Agent | 情报作用域 服务端会话 证据及监控工具 | 多轮正确 确认后真执行 跨设备恢复 |
| P3 行业分析 | 行业包 核验 简报 报告计划 | 真材料 可核验引用 部分失败可解释 |
| P4 MCP 与来源 | 受管 MCP Telegram 微信 按需提取 | 每个 connector 实采验收 权限预算生效 |
| P5 SaaS 交付 | 配额账务 运维 迁移 发布 Git 交付 | 恢复 对账 权限回归 远端 SHA 核实 |

Estimate (2 Go/TS engineers + test support): first RSS + Q&A loop ≈ 2–3 weeks; text-first pilot with managed MCP ≈ 8–12 weeks. Complex account collection, audio/video, multi-runtime concurrency, self-serve MCP marketplace are estimated separately.

**First milestone:** two-tenant scheduled RSS collection, evidence-backed multi-turn Q&A, and a monitor created through conversation that actually runs.

## Acceptance matrix (A01–A21, all must pass before launch)

| ID | Scenario | Evidence of success | Phase |
|---|---|---|---|
| A01 | 两租户相同关键词和来源 | 不冲突 不共享私有正文 | P1 |
| A02 | 猜测其他租户文档 Run 报告对象 ID | API 工具 SSE 导出均拒绝 | P1 |
| A03 | 相同幂等键重放 | 不新增逻辑操作 不重复原有 Job 同 call 仅一次账务效果 | P1 |
| A04 | 同键不同参数 | 409 原结果不被覆盖 | P1 |
| A05 | 入队后进程退出 | 重启恢复 不漏任务 | P1 |
| A06 | 内容入库前崩溃 | 游标不提前推进 | P1 |
| A07 | 旧 worker 晚返回 | 过期 fencing 被拒绝 | P1 |
| A08 | 网页和 SSE 关闭 | 后台继续 结果可回放 | P1 |
| A09 | 多轮指代追问 | 输入带前文和证据范围 | P2/P3 |
| A10 | 动作批准 拒绝 过期 | 仅批准后真实执行 | P2/P3 |
| A11 | 正文截断 来源部分失败 | 报告说明覆盖 不编造 | P2/P3 |
| A12 | 正文更新 | 新修订产生 旧引用可读 | P2/P3 |
| A13 | 429 凭据失效 | 退避或暂停 不无限重试 | P4 |
| A14 | MCP 工具或 schema 变化 | 未授权能力不执行 | P4 |
| A15 | 子任务取消 runtime busy | 排队 取消传播 费用可查 | P4 |
| A16 | 外部成功 ACK 丢失 | 对账或查询 不盲重试 | P4 |
| A17 | 预算不足 连接撤销 | 新成本任务阻止 旧证据可读 | P5 |
| A18 | 数据库和对象恢复 | 游标 引用 计划 费用一致 | P5 |
| A19 | 平台特定采集 | 每个启用平台有真实小样本 | P4 |
| A20 | 最终 Git 交付 | 目标 分支 远端 SHA 一致 | P5 |
| A21 | 等待时重启 完成事件重复投递 | 自动恢复一次 不重复建 Job 或计费 | P2 (auto-continue) |

A03 permits separate billing for legitimate batch children / independent model steps; it forbids duplicate effects from replays. A21 is mandatory for auto-continue mode. RSS fixtures do not prove Telegram media, WeChat or Douyin; paid model/API acceptance uses an explicit small budget with source, time, environment and receipts recorded.

Real-sample connector acceptance is deferred to the last step; see [pending-acceptance.md](pending-acceptance.md).
