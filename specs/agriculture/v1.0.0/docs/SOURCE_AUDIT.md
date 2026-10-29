# 来源审计与复用决策

审查日期：2026-10-29（Asia/Shanghai）。审查对象：公开GitHub指定提交与组件官方资料。审查方式：仓库元数据、关键源码/配置/测试文字与官方网页核对；未执行两个上游的全量构建、真实环境集成或性能压测。

## 1 版本与证据范围

Flow main固定为`206c82af7ae007f45da4c582f373fbabf027f1b8`，提交时间2026-10-26 11:27 UTC；AgriScope main固定为`571a2dcd9abf8b8a22b66c750ee710b61d482051`，提交时间2026-07-24 01:46 UTC。本轮通过GitHub commits/main重新核对，未发生基线漂移。[S01][S02]

仓库已有实现不自动等于本产品已完成集成。当前规格中的80项FR、20项NFR、144项任务和验收状态均面向新产品，不能从上游README直接复制done状态。

## 2 已核实事实与工程处理

| 事实 | 证据 | 工程处理 |
| --- | --- | --- |
| Flow已有Go平台与Loom职责分离 | S03 | 新增GIS服务延续单一数据所有者与API/事件集成 |
| Agent有范围/上下文/工具/执行/用量接口，subject仍为画布/工作区/专题 | S04、S40 | 增农业subject或受约束适配器，不能仅改提示词名称 |
| Flow已有MCP与预算代码 | S37、S38 | 复用并扩展农业长任务、许可、配额和对账 |
| Flow RLS空范围被解释为system，真实Flow路由也挂中间件 | S05、S06 | 普通应用改默认拒绝，系统任务显式授权；这不是已复现跨租户泄漏的报告 |
| AgriScope身份可由前端user_code选择，端点提交操作编码再做角色判断 | S07、S08、S09 | 用平台可信身份替代，GIS端重新授权，隐藏前端切换器不充分 |
| bbox读取路径未包含租户/项目范围过滤 | S10 | 改查询/表约束/RLS及所有读路径，加入跨租户负例 |
| 部分影像计算在请求内to_thread执行，部署主要是DB/API/Web/本地卷 | S11、S16 | 拆独立持久worker、对象存储与可恢复任务 |
| NDVI流程真实读取/对齐栅格并比较两期差值 | S12 | 保留算子，增加像元质量和农学适用门禁，不声称已具备病害/产量预测 |
| Cesium图斑有固定示意拉伸 | S13 | 明确示意状态，真实模型和设备状态另建 |
| 病虫害create_assessment登记外部预测与置信度待复核 | S14 | 可作为模型结果接口，不能把登记流程写成已完成推理引擎 |
| 作者记录392测试通过并保留UAT/规模测试等后续项 | S15 | 属作者记录，本规格未复跑；W00和发布阶段重新执行所需测试 |
| root README与Flow新增能力存在叙述差异，部分待验收条目可能过时 | S03、S37、S41 | 实现以当前代码及执行证据为准，文档需要同步修正 |

## 3 许可证与数据授权

AgriScope主代码Apache-2.0、Flow主代码MIT，允许依照相应条款用于商业项目；导入时保留版权、LICENSE及适用NOTICE、修改声明。[S19][S20][S42]

Flow包含tldraw 5.2.5，仓库已明确根MIT不覆盖所有依赖。tldraw官方将默认开发许可与生产许可区分。本规格选择农业生产构建不依赖其绘图路径；若用户选择启用，须取得适当授权，不能绕过许可验证。[S17][S18][S21]

OpenLayers为BSD-2-Clause，CesiumJS为Apache-2.0，GDAL采用MIT-style许可；Cesium ion、底图/影像和各驱动依赖另行核查。PostGIS为GPLv2，其官方说明正常作为数据库使用不会通常要求上层应用采用GPL；修改分发PostGIS本体的义务独立。[S22]—[S25]

AgriScope附带公开行政/OSM等快照和外部瓦片访问配置。它们不能直接作为客户合法经营边界、法定地块或已获得商业底图托管权的证明。正式接入应记录来源/用途/署名/期限/再分发条件与费用。[S35][S36]

Sentinel及Landsat可作为初期遥感来源，仍需按具体产品许可保留来源标识，并独立评估访问服务和云资源成本。[S33][S34] 本次不是全量传递依赖/模型权重/字体/底图许可审计；W00以实际发布构建生成SBOM和权利台账。

## 4 官方标准对实现的约束

- PostgreSQL RLS默认策略、owner/BYPASSRLS与FORCE行为须结合实际角色验证，不能只检查SQL文件存在。[S26]
- GeoJSON对外使用WGS84及经度纬度语义；项目内部投影、GCJ/BD类服务坐标及原始资料不允许无标识混用。[S27]
- STAC用于数据目录的空间/时间/资产描述，不替代对象访问授权和许可证校验。[S28]
- COG分块、概览和HTTP Range适合按需读图；只有文件后缀.tif不能证明已是COG。[S29]
- Sentinel场景分类、Landsat QA需按产品/传感器解释，不能仅用整景云量代替地块像元质量；类别mask不能用连续值双线性插值。[S30][S31]
- S3预签名URL可以在有效期内多次使用，应用登出不等于即时撤销已签地址。敏感资产采用每次鉴权代理，普通直签记录短TTL与残余窗口。[S32]
- 数字孪生的能力需要真实对象映射和持续数据关系，仿真与闭环可分阶段增加；OGC所引文件是讨论材料，不能宣传为本产品已通过的标准认证。[S43]

## 5 开发Agent需要重新验证的内容

W00记录实际本地分支、上游SHA、锁文件、应用启动、迁移入口、现有AGENTS、回归命令和许可清单。随后的工作包按风险验证真实PostGIS、遥感算子、跨服务权限、支付回调、持久任务与备份恢复。任何“通过”都要附环境、日期、命令/操作及证据。

性能目标和产品默认值来自本规格的工程决策，不是上游或官方性能保证。付费渠道、云部署与真实设备条件由用户授权和试点资料决定。

## 6 来源目录

### S01 Flow审查提交

链接：[Flow审查提交](https://github.com/newyngwieslash-ops/WeaveSense-Flow/commit/206c82af7ae007f45da4c582f373fbabf027f1b8)。  
用于支持：主干基线与变更边界。

### S02 AgriScope审查提交

链接：[AgriScope审查提交](https://github.com/autool/AgriScope/commit/571a2dcd9abf8b8a22b66c750ee710b61d482051)。  
用于支持：GIS来源基线。

### S03 Flow架构

链接：[Flow架构](https://github.com/newyngwieslash-ops/WeaveSense-Flow/blob/206c82af7ae007f45da4c582f373fbabf027f1b8/docs/flow/architecture.md)。  
用于支持：平台/知识服务所有权与API加Outbox原则。

### S04 Flow Agent运行链

链接：[Flow Agent运行链](https://github.com/newyngwieslash-ops/WeaveSense-Flow/blob/206c82af7ae007f45da4c582f373fbabf027f1b8/backend/internal/flow/runchain.go)。  
用于支持：五类可扩展接口。

### S05 Flow RLS策略

链接：[Flow RLS策略](https://github.com/newyngwieslash-ops/WeaveSense-Flow/blob/206c82af7ae007f45da4c582f373fbabf027f1b8/backend/internal/database/flow_rls.go)。  
用于支持：空租户上下文视为system的现行设计。

### S06 Flow生产路由

链接：[Flow生产路由](https://github.com/newyngwieslash-ops/WeaveSense-Flow/blob/206c82af7ae007f45da4c582f373fbabf027f1b8/backend/cmd/server/main.go)。  
用于支持：TenantMiddleware真实挂载。

### S07 AgriScope用户选择

链接：[AgriScope用户选择](https://github.com/autool/AgriScope/blob/571a2dcd9abf8b8a22b66c750ee710b61d482051/frontend/src/store/userStore.ts)。  
用于支持：前端user_code与localStorage。

### S08 AgriScope能力校验

链接：[AgriScope能力校验](https://github.com/autool/AgriScope/blob/571a2dcd9abf8b8a22b66c750ee710b61d482051/backend/app/services/project_user_service.py)。  
用于支持：按提交编码查询角色。

### S09 AgriScope长势API

链接：[AgriScope长势API](https://github.com/autool/AgriScope/blob/571a2dcd9abf8b8a22b66c750ee710b61d482051/backend/app/api/v1/endpoints/growth_monitoring.py)。  
用于支持：operator_code/requester_code接口。

### S10 AgriScope地块查询

链接：[AgriScope地块查询](https://github.com/autool/AgriScope/blob/571a2dcd9abf8b8a22b66c750ee710b61d482051/backend/app/dao/plot_dao.py)。  
用于支持：bbox路径的范围过滤需要改造。

### S11 AgriScope计算服务

链接：[AgriScope计算服务](https://github.com/autool/AgriScope/blob/571a2dcd9abf8b8a22b66c750ee710b61d482051/backend/app/services/growth_monitoring_service.py)。  
用于支持：请求内to_thread执行。

### S12 AgriScope NDVI引擎

链接：[AgriScope NDVI引擎](https://github.com/autool/AgriScope/blob/571a2dcd9abf8b8a22b66c750ee710b61d482051/backend/app/services/growth_monitoring_engine.py)。  
用于支持：真实栅格对齐、差值阈值与分类。

### S13 AgriScope三维视图

链接：[AgriScope三维视图](https://github.com/autool/AgriScope/blob/571a2dcd9abf8b8a22b66c750ee710b61d482051/frontend/src/components/CesiumViewer.vue)。  
用于支持：示意拉伸与GeoJSON可视化。

### S14 AgriScope病虫害流程

链接：[AgriScope病虫害流程](https://github.com/autool/AgriScope/blob/571a2dcd9abf8b8a22b66c750ee710b61d482051/backend/app/services/monitoring_network_service.py)。  
用于支持：预测结果登记与人工复核。

### S15 AgriScope开发进度

链接：[AgriScope开发进度](https://github.com/autool/AgriScope/blob/571a2dcd9abf8b8a22b66c750ee710b61d482051/docs/PROJECT_PROGRESS.md)。  
用于支持：作者记录的验证与未完成UAT/规模验证。

### S16 AgriScope部署

链接：[AgriScope部署](https://github.com/autool/AgriScope/blob/571a2dcd9abf8b8a22b66c750ee710b61d482051/docker-compose.yml)。  
用于支持：原型部署服务与本地卷。

### S17 Flow第三方许可

链接：[Flow第三方许可](https://github.com/newyngwieslash-ops/WeaveSense-Flow/blob/206c82af7ae007f45da4c582f373fbabf027f1b8/THIRD_PARTY_UPSTREAMS.md)。  
用于支持：根MIT不覆盖所有依赖，tldraw单独处理。

### S18 Flow前端依赖

链接：[Flow前端依赖](https://github.com/newyngwieslash-ops/WeaveSense-Flow/blob/206c82af7ae007f45da4c582f373fbabf027f1b8/web/package.json)。  
用于支持：React与tldraw 5.2.5依赖。

### S19 AgriScope许可证

链接：[AgriScope许可证](https://github.com/autool/AgriScope/blob/571a2dcd9abf8b8a22b66c750ee710b61d482051/LICENSE)。  
用于支持：Apache-2.0。

### S20 Flow许可证

链接：[Flow许可证](https://github.com/newyngwieslash-ops/WeaveSense-Flow/blob/206c82af7ae007f45da4c582f373fbabf027f1b8/LICENSE)。  
用于支持：MIT及上游版权声明。

### S21 tldraw官方许可

链接：[tldraw官方许可](https://tldraw.dev/community/license)。  
用于支持：开发与商业生产许可不同。

### S22 PostGIS官方GPL说明

链接：[PostGIS官方GPL说明](https://postgis.net/documentation/faq/gpl-license/)。  
用于支持：普通数据库调用与修改分发本体的区别。

### S23 OpenLayers官方

链接：[OpenLayers官方](https://openlayers.org/)。  
用于支持：二维图层能力与BSD-2-Clause。

### S24 CesiumJS官方

链接：[CesiumJS官方](https://github.com/CesiumGS/cesium/blob/main/README.md)。  
用于支持：Apache-2.0与可选Cesium ion服务。

### S25 GDAL官方

链接：[GDAL官方](https://gdal.org/en/stable/)。  
用于支持：栅格矢量处理与MIT-style许可。

### S26 PostgreSQL RLS官方

链接：[PostgreSQL RLS官方](https://www.postgresql.org/docs/current/ddl-rowsecurity.html)。  
用于支持：默认拒绝、所有者与BYPASSRLS边界。

### S27 GeoJSON RFC7946

链接：[GeoJSON RFC7946](https://www.rfc-editor.org/rfc/rfc7946)。  
用于支持：WGS84与GeoJSON交换规范。

### S28 STAC规范说明

链接：[STAC规范说明](https://stacspec.org/en/about/stac-spec/)。  
用于支持：Item/Collection/API、时间及资产元数据。

### S29 OGC COG标准

链接：[OGC COG标准](https://www.ogc.org/standards/ogc-cloud-optimized-geotiff/)。  
用于支持：分块、概览和HTTP Range。

### S30 Sentinel-2 L2A说明

链接：[Sentinel-2 L2A说明](https://sentinels.copernicus.eu/-/collection-1-level-2a)。  
用于支持：表面反射率、场景分类与云影信息。

### S31 Landsat QA说明

链接：[Landsat QA说明](https://www.usgs.gov/landsat-missions/landsat-collection-2-quality-assessment-bands)。  
用于支持：QA_PIXEL、QA_RADSAT及传感器差异。

### S32 S3预签名官方文档

链接：[S3预签名官方文档](https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-presigned-url.html)。  
用于支持：链接可重用至过期，与应用登录撤销不同。

### S33 Sentinel数据许可

链接：[Sentinel数据许可](https://cds.climate.copernicus.eu/licences/ec-sentinel)。  
用于支持：开放使用与来源标识。

### S34 Landsat使用说明

链接：[Landsat使用说明](https://www.usgs.gov/faqs/are-there-any-restrictions-use-or-redistribution-landsat-data)。  
用于支持：使用再分发及来源建议。

### S35 AgriScope NOTICE

链接：[AgriScope NOTICE](https://github.com/autool/AgriScope/blob/571a2dcd9abf8b8a22b66c750ee710b61d482051/NOTICE)。  
用于支持：附带数据与第三方来源。

### S36 AgriScope底图代理

链接：[AgriScope底图代理](https://github.com/autool/AgriScope/blob/571a2dcd9abf8b8a22b66c750ee710b61d482051/frontend/nginx.conf)。  
用于支持：OSM镜像/Esri服务需独立核查。

### S37 Flow MCP实现

链接：[Flow MCP实现](https://github.com/newyngwieslash-ops/WeaveSense-Flow/blob/206c82af7ae007f45da4c582f373fbabf027f1b8/backend/internal/flow/mcp.go)。  
用于支持：工具白名单、Schema变化与范围缓存。

### S38 Flow预算实现

链接：[Flow预算实现](https://github.com/newyngwieslash-ops/WeaveSense-Flow/blob/206c82af7ae007f45da4c582f373fbabf027f1b8/backend/internal/flow/billing.go)。  
用于支持：预算预留、幂等及消费结算基础。

### S39 Flow行业包

链接：[Flow行业包](https://github.com/newyngwieslash-ops/WeaveSense-Flow/blob/206c82af7ae007f45da4c582f373fbabf027f1b8/industry-packs/README.md)。  
用于支持：嵌入技能包、版本及评测边界。

### S40 Flow Agent业务对象

链接：[Flow Agent业务对象](https://github.com/newyngwieslash-ops/WeaveSense-Flow/blob/206c82af7ae007f45da4c582f373fbabf027f1b8/backend/internal/flow/subject.go)。  
用于支持：当前canvas/workspace/topic，农业需适配。

### S41 Flow真实验收记录

链接：[Flow真实验收记录](https://github.com/newyngwieslash-ops/WeaveSense-Flow/blob/206c82af7ae007f45da4c582f373fbabf027f1b8/docs/flow/pending-acceptance.md)。  
用于支持：真实连接器等待验收，条目须结合当前代码。

### S42 Apache-2.0官方条款

链接：[Apache-2.0官方条款](https://www.apache.org/licenses/LICENSE-2.0)。  
用于支持：分发、NOTICE和修改声明等义务。

### S43 OGC数字孪生讨论文件

链接：[OGC数字孪生讨论文件](https://docs.ogc.org/dp/24-025.html)。  
用于支持：GIS、物理映射、动态数据与反馈；该文件不是强制标准。

