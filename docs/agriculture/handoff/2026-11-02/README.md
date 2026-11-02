# 本地继续开发交接入口

本目录是 2026-11-02 的项目审计和本地接续资料，不代表 PRD 已完整实现或 GitHub 推送已成功。

## 阅读顺序

1. [当前实现与 PRD 差距](PRD-IMPLEMENTATION-AUDIT.md)：已完成范围、代码复用边界、发布与外部条件。
2. [全部剩余任务](REMAINING-TASKS.md)：审计快照中的 119 项未闭合任务，保留原始依赖、实施、交付、验收及验证要求。
3. [本地拉取与恢复指南](LOCAL-SETUP-AND-RESUME.md)：辨别完整产品分支，检查历史，配置开发环境，以及完整 Git bundle 回退路径。
4. [Zcode / Codex 完整接续提示词](LOCAL-AGENT-HANDOFF-PROMPT.md)：在完整仓库根目录开启会话后提交全文。

机器可读文件：[144 项任务审查](TASK-AUDIT.json)、[100 条需求追溯](REQUIREMENT-TRACEABILITY.json)、[审计完整性验证](AUDIT-VALIDATION.json)。

## 快照与权威记录

- 本次代码审计基于 `7c6e0229e3e708ab540f338a7e01cb0f24d25a12`，任务快照为 25 项完成、2 项进行中、117 项未开始。
- 审计时 DT-026 / DT-041 的 CI 验收正在执行。后续结果若有变化，以有实际源绑定证据的 `specs/agriculture/v1.0.0/tasks.json` 和其 `docs/implementation/STATUS.md` 为准；不得仅凭这份快照或 CI 绿色标志修改状态。
- `ci/agriculture-w00` 只是缩减测试分支。完整产品目标分支是 `feat/agriculture-v1`，必须核验它已包含上述实现提交和完整目录；名称相同不代表推送已经完成。
- GitHub 发布回执及附带的 `DELIVERY-MANIFEST.json`（如交付）记录最终提交、树和恢复包校验值。此目录不把包含自身的提交写成自指 SHA。
- 新会话先保留工作区改动并重新读取当前台账，按依赖推进；没有真实数据、预算或设备授权时只阻塞受影响任务，不伪造验收。

这份交接资料包本身不包含全部源码。若完整产品分支尚不可用，应使用另行提供且经过校验的完整历史恢复包，而不是把主分支初始基线或 CI 分支当成产品。
