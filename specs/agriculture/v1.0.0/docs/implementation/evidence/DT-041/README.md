# DT041 — 在建集成证据

状态 `in_progress`。`local-integration/catalog-postgres.json` 为 9 项实际 PostgreSQL/PostGIS 目录、权利、不可变实体引用及私有存储检查，含读取实际字节过程中撤回来源、读后重新检查拒绝。精确源码哈希见报告。

## 11:55 UTC 候选冻结前复验

- `final-python.json`：109项精确检查全部通过、零skip；21个实际运行源哈希独立核验与当前文件一致。包含锁等待/commit前过期的真实回滚、固定namespace兼容/不fallback及11个owned下载fixture安全负例。
- `final-platform-http.json`：真实双PG/Redis/race，平台HTTP7个根/子项及原Vue7检查通过；显式父farm能力继承、no-farm严格direct、恶意farm_manager自授权限拒绝。执行后仅`asset_catalog_download_chain_test.go`增加了后述测试；业务源码哈希无变化，不能声称整个旧报告等于新候选树。
- `final-download.json`与`final-download-provenance.json`：实际Go↔GIS HTTP、私有真实字节、双PG/Redis、race，精确10项通过且源码稳定。当前/历史实体、父farm显式download正负例、来源/授权读取中撤回、真实logout读取中不给bytes、private no-store均通过。
- `broad-go-short.json`：463项组件通过，22个依赖数据库/浏览器的集成项明确skip，不计作真实集成验收。CI专用required模式仍缺依赖即失败、严格零skip；新增root曾在普通无依赖短测错误强制失败，已按此边界修复，未降低CI条件。

新CI资产gate要求精确18个Go根/子项+23浏览器检查，并重算实际runner/nginx/React/Vue产物与provenance哈希。两原始清单经独立审查后显式刷新，4扫描器单测+39旧身份/query检查通过。真正远端CI/浏览器仍待执行；本目录不增加done数。

最终lint收尾仅13个Python源的格式/import排序与一处未使用测试import；非import AST逐文件完全相同，证据`final-format-proof.json`。上述真实PG报告保留原hash，最终CI将验证格式化后的确切源码，不把格式前hash伪称为候选hash。

该报告不是平台 Go 授权或浏览器验收，也不是外部许可、真实客户数据或商用发布证明。metadata 登记不产生 ready 实体，未知来源保持待审核；DT042 实体发布没有被此任务代替。

当前继续接入独立资产对象签名、历史版本与当前授权版本区分、平台操作链、资源重新签收授权、目录 UI 及完整隔离矩阵。最终真实 CI、签名 HTTP、浏览器和回归门禁尚待冻结与执行。
