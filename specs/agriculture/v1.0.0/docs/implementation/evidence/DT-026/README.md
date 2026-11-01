# DT026 — 在建集成证据

状态 `in_progress`，不增加 done 数。当前证据是实际本地 PostgreSQL/PostGIS、Redis 与 HTTP 的 fixture 验证；真实 Chromium/部署 CI 的最终冻结候选尚未验收。

## 11:55 UTC 候选冻结前复验

`final-platform-matrix.json` 为当前49平台路由/378请求/428精确Go记录，真实双PG、Redis、race、源码稳定且无业务副作用。`final-gis-isolation.json` 为286个精确GIS检查、113源文件稳定；39GIS路由。`final-vue-build.json` 为120项组件/692断言、215模块实际构建，33源文件与测试副本一致。新增业务均保持独立真实浏览器门禁；本地无可用安全Chromium路径，不以组件代替。

CI新增parcel精确9Go记录+21浏览器检查、52Python/PostGIS检查；旧farm/identity/迁移/隔离门禁完整保留。最后汇总见DT041目录，正式任务状态仍等待远端冻结源码验收。

`local-integration/parcel-http.json`：8 个精确 Go 根/子项，race、完整认证模式、真实登录 cookie/CSRF、真实响应进入 Vue 解析器 8 检查；对应源码哈希及原生运行位置见报告。

`gis-isolation.json`：286 项实际 PostGIS 隔离测试零失败/零跳过，39 个 GIS 路由，执行期间源稳定。旧测试未删除，新增地块、资产目录、typed 资产对象签名与缺依赖拒绝；独立停库证明见 `gis-shutdown.json`。

`platform-matrix.json`：349 个精确 Go 根/子项，40 路由、308 请求的业务全行快照未改变，真实 Redis 和双 PostgreSQL，race 开启、非组件模式。DT041 新路由仍在接入，最终矩阵将进一步扩充，不能把本次范围当作未来路由证明。

`vue-build.json`：115 个组件测试、641 断言和实际双目录入口构建。组件及构建不替代真实浏览器授权闭环。

持续集成后源码可能变化；报告保留各次输入哈希，不声称这些本地结果对应之后的全部修改。最终冻结必须复验精确门禁、清单、迁移、既有功能与真实浏览器。
