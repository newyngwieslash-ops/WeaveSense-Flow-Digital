# W00 恢复后的首包验收边界

日期：2026-10-29。此文汇总已有实际结果，不将恢复环境中的新运行写成旧 CI，也不重写历史源码。

## 原始完成标准与证据

DT-008 要求固定基线的可执行构建、迁移及针对性回归、真实失败记录和下一任务；不要求提前完成 W01/W02 的 G0，也不要求最终商业镜像已获得全部许可。

| 范围 | 已执行证据 | 不包含的结论 |
| --- | --- | --- |
| 固定来源与集成审计 | `BASELINE_RECOVERY_MANIFEST.json`、`BASELINE_RECOVERY_AUDIT.md`：Flow 历史祖先、来源文件逐项比对、许可证和已有规则保留、实际平台/Agent/MCP/GIS/数据库路径检查 | 未声称当前上游 HEAD 与审查 SHA 相同 |
| 初始依赖、许可及农业产物 | `dependencies/recovery-validation.json`；真实 React/Vue 构建可达图、984 份通知原文、4 项 SBOM 与 22 项来源权利测试 | 最终发行镜像、图标出处、缺失通知和原生编解码器仍是发布门禁 |
| 新卷基础设施及迁移 | `RECOVERED_CI_EVIDENCE.md` 与 `evidence/recovery-ci-20261029.json` 中 c20 的原始 W00 运行 | 仅 postgres/redis/geo-readiness 和固定 Flow/Loom 迁移；不是完整业务服务器启动或后续农业迁移 82/83 |
| 小型黄金样本 | `evidence/w00/golden-recovery-20261029.json`：10 项几何、真实 GeoTIFF 像元及确定性样本测试 | 自制 fixture，不是假称获准现场影像或农学效果 |
| 机器契约 | `evidence/w00/contract-recovery-result.json`：69 项 schema、版本、scope 负例 | 不是真实业务接口、签名验证或全部领域集成 |
| 恢复后的补充构建与回归 | `evidence/w01/dt014-navigation.json`：两端真实类型检查/生产构建、59 项导航测试；`evidence/native-environment-isolation-20261029.json`：真实新 PG 集群的 GIS 123 项、平台 5 个主测试及子测试、旧 Flow 21 项回归 | 这些后续结果不被倒填为初始 W00 CI；不是 DT-014 浏览器闭环或 Docker 镜像验收 |

各任务冻结提交、逐文件 SHA256、实际命令/退出码及证据摘要由规格目录 `docs/implementation/RECOVERY_STATUS.json` 保存。历史 CI 使用其执行时源码，并通过精确摘要映射到恢复提交；后续源码变更不会篡改旧证据。

## 实际失败与修复记录

- 环境重置导致原本地未发布历史不可直接取得；采用独立恢复目录，保留原始规格、固定来源历史、官方 CI 原件和重建文件的明确区分，不声称恢复了原 bcd 提交字节。
- 新网关的 Nginx 正则、可写临时目录、非默认端口 Host 转发问题均按真实 HTTP 失败修复；50 项最终 HTTP 检查通过，但浏览器仍未因而算通过。
- 原生测试工具发现继承 `PGHOSTADDR` 等环境可能改变连接目标；采用受限环境并在明确 loopback 的污染负例下重跑真实 PostgreSQL。旧报告保留，新报告独立存档。
- 当前执行器 Chromium 启动因 Unix socket 权限失败；没有关闭浏览器沙箱、删除断言或用 HTTP 结果替代浏览器。

## 下一项与持续门禁

W00 窄范围完成不等于 G0 通过。下一项实际开放任务仍为 DT-014 的真实同域浏览器深链接、退出/重登录和跨应用返回验证；DT-015 按其依赖推进。后续完整 GIS 资源归属、全量 RLS、真实农场、影像、支付、模型与商业发布验收仍待实施或外部条件，不能由本报告清零。

恢复证据检查和任务 JSON/Markdown 同步必须通过后，才把 DT-008 的恢复核验记录改为 verified。本文不是独立的任务完成开关。
