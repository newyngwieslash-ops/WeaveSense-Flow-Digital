# WeaveSense DigitalTwin 当前实现与PRD差距审计

**结论：尚未完整实现PRD，尚不能称为可商用的完整农业数字孪生SaaS。** 已有实质性安全基础和农场功能；稳定地块、资产目录已有候选实现。其余领域闭环、地图、影像接入、持久作业、遥感、外业、知识、农业Agent、报告、商业计量、运维、真实试点及S4/S5仍有大量集成和验收工作。

本审计固定在源码`7c6e0229e3e708ab540f338a7e01cb0f24d25a12`，日期2026-11-02。正式任务数为25/144（17.4%），这是任务计数，**不是代码量、工作量或产品完成百分比**。后续推送和CI结果以独立发布记录为准。

台账截止：`2026-11-02T05:51:50Z`。本轮CI候选提交：`52d6e3f43899a1f6412f5e4c5be0e5bc428ae1ec`（独立scaffold树，不是完整产品源码树）；截止时identity run `41396200447`仍在运行，不能计为通过。后续验收必须另有源绑定实际证据，再更新权威台账。

## 1 已完成与未完成的准确口径

|类别|数量|含义|
|---|---:|---|
|原范围正式done|25|DT-001至DT-025；保留每项冻结源与执行证据，不能外推完整需求通过|
|in_progress|2|DT-026、DT-041候选实现，待真实浏览器及完整CI验收闭合|
|not_started|117|仍须按原任务实施、集成、复核和验收；部分拥有可复用上游模块|
|未闭合任务总数|119|完整内容见REMAINING-TASKS.md；不是只剩补测试|
|功能需求|80|requirements.json逐项仍为not_run；局部任务测试并非零，但总验收未回填passed|
|非功能需求|20|逐项仍为not_run，必须以真实测量和范围化证据闭合|
|阶段门禁|6|现有STATUS记录G0至G5均未通过，不能自行推断G0通过|

S0的24项任务均已按各自范围done，S1为1/49、S2为0/28、S3为0/27、S4为0/8、S5为0/8。S0任务通过与G0整体签收是两层记录；下一会话应按门禁清单核对缺项和签收，不应将历史测试全部重做，也不能只改文字为passed。

只有AG-FR-002的全部映射任务已done；该需求自身verification_status仍为not_run。所有其他99条FR/NFR至少包含一个未完成映射任务。这是映射检查，不是自动需求验收。

这里的100条not_run表示正式需求验收记录尚未闭合，**不表示100条需求均完全没有实现或已测试失败**。具体已实现子范围和复用能力见下文；不能将局部结果外推为整条需求通过，也不能反向抹去已完成工作。

## 2 已有实质性成果及证据

- W00 DT-001至008：固定上游、受控导入、许可/来源、农业profile、契约与可复现基线。生产许可仍需对实际启用来源逐项核查。
- W01 DT-009至016：平台身份、签名委托、可信GIS主体、同域入口、CSRF、会话、真实签名浏览器链。农业未适配旧入口保持关闭。
- W02 DT-017至024：GIS RLS、Flow空scope默认拒绝、范围查询、受控对象、缓存/SSE撤权、作业身份组件、安全解析隔离、真实隔离矩阵。DT-022不是W06队列；DT-023不是完整上传业务。
- W03 DT-025：农场创建、区域配置、显式授权、修改、归档和撤权已有真实CI闭环。最近完成证据源为f281a8d4，CI scaffold为79f25d19；对应原始文件在`specs/agriculture/v1.0.0/docs/implementation/evidence/DT-025/`。
- DT-026：稳定parcel、不可变revision、历史读取、并发冲突、平台/GIS/Vue路径及测试已存在。仍须核对本轮真实CI，不能仅凭已有本地测试计done。
- DT-041：资产目录、来源权利、版本、实体引用与当前授权的读取防护已存在。metadata登记不会令资产ready；实际分片上传/可信实体发布由DT-042完成。

本次实际执行`python scripts/agriculture/spec_status.py --check --require-evidence`退出0，结果为结构及状态同步有效。它验证证据文件和冻结源关联，不是重新跑了历史浏览器、数据库或全部产品测试。完整每任务证据路径及source_commit见TASK-AUDIT.json。

## 3 代码盘点与剩余功能

判定方式：结合原任务、农业生产入口、实际服务边界和已存证据。旧AgriScope完整模块以及Flow/Loom通用能力可复用，但不能因文件存在、测试存在或页面存在就把农业PRD任务标完成。

### W03 农业领域

**现有可复用代码**
- services/agri-gis/backend/app/agriculture/farms_store.py
- services/agri-gis/backend/app/agriculture/parcels_store.py
- services/agri-gis/backend/app/agriculture/farms_geometry.py
- backend/internal/agriculture/parcel_types.go
- services/agri-gis/backend/app/services/workbench_service.py
- services/agri-gis/backend/app/dao/workbench_dao.py
- services/agri-gis/backend/app/services/plot_attribute_field_service.py
- services/agri-gis/backend/app/services/vector_export_service.py
- services/agri-gis/frontend/agriculture/parcels/ParcelWorkspace.vue
**已存在的具体能力**
- 正式农场模型、GIS权威写入、平台签名授权及Vue已接通；DT025历史验收定位 STATUS.md / evidence/DT-025/README.md。
- 稳定parcel身份、append-only revision、history/as-of、expected_revision、事务receipt/outbox及冲突UI已实现为DT026候选。
- 旧workbench_service.split_plot/merge_plots及DAO ST_Split/ST_Union、字段服务、导出渲染器可移植；不能直接把旧plot_code当parcel_id。
- farms_geometry.measure_geometry已有真实PostGIS spheroid面积、显式轴序、decimal，当前只接受EPSG:4326/CRS84。
**仍需实现或集成**
- DT-025 实现农场档案和区域配置：原任务done；保留既有验收并对后续改动回归，不重新计为缺失功能。
- DT-026 实现稳定地块与不可变边界修订：候选实现已存在，但最终远程真实CI/浏览器验收未闭环；复核最终冻结源码映射后才能done。
- DT-027 建立种植季作物与品种档案：新增农场season及parcel_revision绑定的作物/品种/播种收获和来源；跨年、多茬、可配置重叠策略并发安全；未知值保持缺失；标准/租户字典分离。
- DT-028 实现地块分割合并和关联血缘：将旧拓扑算子接入新parcel命令；新身份、父子lineage、有效期、面积容差及种植季/巡查/分析引用迁移；不能改写历史。
- DT-029 实现地块属性与版本化自定义字段：将旧字段能力迁入tenant/farm及稳定schema版本；停用保留历史，变更使质检/报告失效，校验与批量导入一致。
- DT-030 实现受控矢量导入与导出：在受控ingestion上补预检-原子发布的GeoJSON/GPKG/Shapefile ZIP流程和按授权revision导出；逐行错误及read-back摘要。
- DT-031 统一坐标转换轴序及面积单位：扩大有效CRS转换并保留源/目标/轴序/单位/算法；提取统一面积换算和显示；同步Go parcel_types revise白名单；独立参考相对误差<=0.5%。
- DT-032 迁移旧项目地块与历史业务：旧项目/任务→农场/parcel/season显式映射及dry-run；旧ID映射、未知字段、历史附件/审核版本，重复迁移和失败回滚对账。
**证据与范围限制**
- 本次只静态审计，未重跑DT025证据或候选测试。
- DT026本地通过记录不能替代最终CI；DT027-032 not_started不表示旧源码完全不存在。

### W04 地图工作台

**现有可复用代码**
- services/agri-gis/frontend/agriculture/App.vue
- services/agri-gis/frontend/agriculture/parcels/ParcelEditor.vue
- services/agri-gis/frontend/src/components/OlViewer.vue
- services/agri-gis/frontend/src/components/CesiumViewer.vue
- services/agri-gis/frontend/src/store/layerStore.ts
- services/agri-gis/frontend/src/store/mapStore.ts
- services/agri-gis/frontend/src/views/FieldCaptureView.vue
- services/agri-gis/backend/app/services/plot_service.py
- services/agri-gis/backend/app/services/workbench_service.py
**已存在的具体能力**
- 正式农业壳只有会话、farm、parcel、asset；地图仅授权边界，无旧API/外部底图。
- 旧OpenLayers/Cesium、图层目录、viewport查询、编辑undo/redo、移动视图是复用候选，不是新产品导航或权限接入完成。
**仍需实现或集成**
- DT-033 完成农业工作台导航和状态壳层：补farm/season上下文与影像、分析、巡查、报告、Agent导航；布局偏好、失效/空态及安全切换。
- DT-034 实现图层目录样式和资源绑定：受权resource_id图层目录，样式版本/来源/质量/许可，撤权后的瓦片和缓存失效。
- DT-035 实现地块绘制节点编辑和拓扑校验：将绘制/节点/吸附接到新parcel expected_revision，服务端拓扑复核和断网草稿；现候选编辑不等于完整地图编辑验收。
- DT-036 实现视窗查询空间筛选与规模控制：将bbox/作物/时间查询连接新授权资源；分页/简化/MVT、复杂度限额及真实规模p95；简化几何不能成为面积依据。
- DT-037 实现时间轴对比和分析结果定位：接通analysis_id+parcel_revision时间轴/定位；共同网格、跨季、缺测/过期/预测区别及两期对比。
- DT-038 提供可选轻量三维视图：feature flag懒加载三维，复用同一授权选中资源；合法地形/3DTiles及来源，失败二维可用。
- DT-039 实现协作冲突撤销与版本恢复：持久恢复产生新revision，协作比较/重新应用及依赖失效；旧undo/redo需适配不可变历史和当前权限。
- DT-040 建立移动外业入口与能力检测：独立轻量移动农业路由、当前授权任务、HTTPS/GPS/相机/存储能力检测，真实移动浏览器许可拒绝/旋转验收。
**证据与范围限制**
- App.vue明示后续影像/外业/报告未开放；旧src视图存在不等于production profile可达。
- 尚无本轮规模性能、真实移动设备或3D权限验收；均not_started。

### W05 数据影像

**现有可复用代码**
- services/agri-gis/backend/app/agriculture/asset_catalog_store.py
- services/agri-gis/backend/app/agriculture/asset_catalog_rules.py
- services/agri-gis/backend/app/agriculture/asset_object_gateway.py
- services/agri-gis/backend/app/agriculture/ingestion/runner.py
- services/agri-gis/backend/app/agriculture/ingestion/worker.py
- services/agri-gis/backend/app/services/public_imagery_service.py
- services/agri-gis/backend/app/services/public_imagery_client.py
- services/agri-gis/backend/app/services/imagery_processing_engine.py
- services/agri-gis/backend/app/services/imagery_asset_service.py
- services/agri-gis/backend/app/services/imagery_quicklook_service.py
- services/agri-gis/frontend/agriculture/asset-catalog/AssetCatalog.vue
**已存在的具体能力**
- DT041候选具备版本化目录/权利快照、typed授权、实体下载与Vue；尚未最终验收。
- DT023受控下载/隔离parser/资源限制可用作上传/检索安全边界；不是完整分片上传系统。
- 旧public imagery search/import重新取Item并分析footprint；旧栅格处理/quicklook/asset摘要和manifest可复用，需接入新持久worker及权利链。
**仍需实现或集成**
- DT-041 建立资产目录和数据权利模型：关闭DT041最终CI、真实浏览器、typed下载与冻结映射证据缺口；登记资产不代表已可处理。
- DT-042 实现分片上传预检和安全发布：分片/续传、hash/类型识别、隔离预检与原子发布、临时对象恢复清理；验证缺片/乱序/重复提交。
- DT-043 实现公开STAC检索及受控影像获取：受控STAC适配器绑定合法collection/domain和候选ID，足迹覆盖/许可/波段重查；至少一真实合法来源检索裁取证据。
- DT-044 实现栅格标准化和波段语义校验：将栅格算子接入独立worker，冻结波段语义、scale/offset/nodata和采集时间；限制重投影/裁切输出，不虚构校正。
- DT-045 接入云阴影与有效观测质量掩膜：来源级SCL/QA云影雪饱和/nodata mask，parcel有效比例和insufficient_data；分类重采样与阈值版本化。
- DT-046 建立输入输出血缘及实体完整性链：全流程不可变输入资产版本/算法参数/manifest及权利血缘；损坏或缺失实体阻断并传播失效。
- DT-047 实现COG发布瓦片和缩略图服务：COG概览/受权range和瓦片缓存/缩略图服务；真实结构与跨租户缓存污染验收，显示拉伸不改分析值。
- DT-048 实现授权到期撤回与数据生命周期：到期/撤回贯穿图层、处理、报告、共享与Agent；能力短期窗口、法律保留、可恢复清理及并发缓存测试。
**证据与范围限制**
- 旧真实影像引擎可复用但不等于新平台租户/parcel/season链已实现。
- DT041仍in_progress；DT042-048 not_started；本次未验证外部来源或产物。

### W06 持久作业

**现有可复用代码**
- services/agri-gis/backend/app/agriculture/job_authority.py
- services/agri-gis/backend/app/agriculture/job_store.py
- services/agri-gis/backend/app/agriculture/job_routes.py
- services/agri-gis/backend/app/agriculture/ingestion/runner.py
- services/agri-gis/backend/app/agriculture/ingestion/worker.py
- backend/internal/flow/command_outbox.go
- backend/internal/flow/command_adapter.go
- backend/internal/flow/billing.go
- backend/internal/agriculture/job_events.go
**已存在的具体能力**
- 已有job授权快照与阶段重验，平台command outbox/dispatcher、预算和事件原语；可复用安全协议。
- JobExecutionProvider是需要真实队列租约实现的Protocol；job_store是append-only授权sidecar，不是GIS计算job/attempt持久队列。
- 生产main.py install_job_routes(app,None)，不能把已有验证fixture当作计算worker接通。
**仍需实现或集成**
- DT-049 建立持久任务及状态转换模型：GIS权威job/attempt/input snapshot及状态机、唯一终态、DB重启恢复；不要把授权记录等同任务。
- DT-050 实现幂等提交与事务Outbox：Go operation+reservation+outbox本库原子提交，GIS Inbox/job去重，关联回执/未知结果对账；同键异payload冲突。
- DT-051 实现独立Python计算Worker运行时：独立计算worker从持久队列领取，算子registry、下载/temp/限额和错误分类；HTTP退出仍执行。
- DT-052 实现租约心跳与fencing防止过期写入：原子领取/心跳/递增fencing，所有进度/结果核对当前租约；真正执行租约provider。
- DT-053 实现重试取消及未知状态处理：有界重试/退避/dead-letter，取消竞态唯一终态，外部未知结果不直接重跑。
- DT-054 实现成果原子发布及临时对象回收：attempt隔离临时对象、manifest/hash校验及短DB发布/outbox；孤儿对象可恢复GC且不删引用。
- DT-055 实现任务状态事件和平台投影：GIS outbox→平台inbox投影，乱序/重放和operation-job关联，受控SSE/轮询恢复游标。
- DT-056 实现计算配额公平调度与资源保护：租户并发/像元/磁盘配额和长短任务公平调度，队列丢失重建及运营视图。
**证据与范围限制**
- DT049-056全部not_started。通用平台队列/租约或DT022权限测试不证明GIS计算runtime完成。
- 必须真实DB+独立进程故障注入（断点崩溃、过期token、取消、对象发布、队列丢失），fixture算子证据明确标注。

### W07 遥感分析

**现有可复用代码**
- services/agri-gis/backend/app/services/imagery_processing_engine.py
- services/agri-gis/backend/app/services/growth_monitoring_engine.py
- services/agri-gis/backend/app/services/growth_monitoring_service.py
- services/agri-gis/backend/app/services/change_candidate_discovery_engine.py
- services/agri-gis/backend/app/services/change_candidate_discovery_service.py
- services/agri-gis/backend/app/services/disaster_service.py
- services/agri-gis/backend/tests/test_growth_monitoring.py
- services/agri-gis/backend/tests/test_imagery_processing_engine.py
**已存在的具体能力**
- 旧imagery_processing_engine._band_products含NDVI算子；growth_monitoring_engine处理真实NDVI两期共同范围/重投影/有效像元与异常矢量。
- growth_monitoring_service重新验证源step、NDVI band、SHA256实体；旧change candidate与disaster叠加服务可复用数值实现。
- 这些功能处在旧project/task/plot模型；生产农业profile未注册分析路由。
**仍需实现或集成**
- DT-057 建立分析算子注册及共同网格约束：版本化算子registry参数schema/输入波段/适用条件；共同CRS/grid/resolution和配准门禁；分类与连续值分别重采样。
- DT-058 实现NDVI与地块分区统计：接通有效反射率/质量mask→NDVI→parcel_revision分区均值中位数分位数面积/有效比例及图层；零分母/小地块边缘测试。
- DT-059 实现多期长势变化与可比性门禁：引入crop season、观测间隔/传感器/分辨率可比性与基准规则，异常仅候选；跨季阻断，不能自动诊断病因/减产。
- DT-060 接入变化候选发现与人工分类：候选发现接新worker/共同网格/来源摘要和人工分类不可变审计；并发分类/旧revision拒绝。
- DT-061 实现灾害范围叠加与影响初评：灾害合法来源、按parcel/season交集面积与重复范围去重；初评/复核限制，不推断赔付或经济损失。
- DT-062 统一分析不确定性与结果有效性：统一observed/inferred/predicted、quality_status及撤回/边界/算法失效，地图/报告/Agent一致，不混合置信度覆盖率确认状态。
- DT-063 建立遥感与几何科学正确性回归：独立真值科学回归与误差报告，涵盖CRS/面积/反射率/mask/配准/统计/矢量化；领域专家结论审阅另记证据。
- DT-064 完成农业分析真实端到端闭环：真实合法来源两个可比较时相完整检索→worker→授权地图→下载；保存摘要、参数、耗时、质量和成本，成功/不足/失败路径可复算。
**证据与范围限制**
- DT057-064全部not_started；旧引擎并非全是假数据，但不能自动计为新PRD生产验收。
- 本次未运行科学参考或真实来源；DT064需要真实数据和授权，不可用demo填补。

### W08 外业闭环

**现有可复用代码**
- services/agri-gis/backend/app/services/field_verification_service.py
- services/agri-gis/backend/app/services/field_verification_artifact_service.py
- services/agri-gis/backend/app/services/review_service.py
- services/agri-gis/backend/app/services/rule_config_service.py
- services/agri-gis/backend/app/services/workbench_service.py
- services/agri-gis/frontend/src/store/fieldCaptureStore.ts
- services/agri-gis/frontend/src/views/FieldCaptureView.vue
- services/agri-gis/backend/tests/test_field_mobile_capture.py
- services/agri-gis/backend/tests/test_field_resolution_workflow.py
**已存在的具体能力**
- 旧field服务已有create/import/match/resolve/reopen、证据附件；规则/任务质检快照和审核/rollback可复用。
- 旧移动store已有GPS accuracy/captured_at、在线状态、草稿及补传；但localStorage键为全局agriscope-mobile-field-draft-v1/pending-v1，不满足新用户/租户隔离，不能直接上线。
**仍需实现或集成**
- DT-065 实现巡查工单及任务范围：建立绑定farm/parcel revision/season与分析证据的巡查工单、有效成员指派和业务状态机；离职/越权成员拒绝。
- DT-066 实现GPS和现场媒体证据采集：新安全上传实体照片及hash、WGS84/精度/时区/设备/来源；区分GPS/手填/后补；真实终端许可和失败待补传。
- DT-067 实现弱网草稿和幂等补传：按user+tenant隔离草稿、退出锁定/清除，采集ID+payload hash幂等，照片可补传；替换全局旧storage键并测试三阶段断网。
- DT-068 实现现场点与地块匹配及人工处置：基于采集时parcel_revision+精度匹配，边界/多重/未匹配显式状态；人工理由审计，实际改图新增版本。
- DT-069 实现规则质检和分级审核：版本化字段/规则/地块/影像摘要质检批次和分级审核，变化失效及后端自审/角色分离策略。
- DT-070 实现巡查通知送达与闭环追踪：平台受控通知、真实目标/回执/重试和拒收；工单闭环不依赖假送达；授权接收人真实试发。
- DT-071 建立农业作业记录与效果反馈：农业作业事实/计划分离、数量单位/执行时间/范围/证据、跨季关联及更正版本；不覆盖历史。
- DT-072 完成真实移动巡查业务验收：授权真实地块+真实移动端弱网采集/照片补传/复核/关闭/报告引用，核验原始GPS精度及照片hash。
**证据与范围限制**
- DT065-072全部not_started；旧移动和审核路径未接农业production profile。
- 真实设备、现场、通知送达和实际角色闭环需外部授权与现场输入；桌面fixture不替代DT072。

### W09 知识证据

**现有可复用代码**
- services/loom-engine/flow/engine.ts
- services/loom-engine/flow/connectors.ts
- services/loom-engine/flow/gaps9.ts
- services/loom-engine/flow/reports.ts
- services/loom-engine/flow/terms.ts
- backend/internal/flow/citations.go
- backend/internal/flow/reviewer.go
- industry-packs
**已存在的具体能力**
- services/loom-engine/flow/engine.ts：通用RSS/Telegram/WeChat来源、采集租约/fencing、文档修订、分块检索、计划与来源撤回。边界：createSource无农业权利/地区作物/有效期模型；searchDocuments/readDocument当前无来源撤回/适用性过滤；不是农业知识闭环。
- services/loom-engine/flow/connectors.ts：现有采集连接器适配层。边界：连接器存在不证明获准农业来源或live采集成功。
- services/loom-engine/flow/gaps9.ts：派生索引、可选嵌入、原始响应归档、片段hash校验。边界：需农业来源权利/撤回门禁和版本影响传播；hash embedder不能作语义质量验收。
- services/loom-engine/flow/reports.ts：带原始revision引用、claims和周期brief的通用报告。边界：Scope为tenant/workspace；没有农业农场/作物适用性和正式报告生命周期。
- services/loom-engine/flow/terms.ts：词典与工作区别名扩展。边界：现有词库非完整农业作物/物候/区域词典。
- backend/internal/flow/citations.go：引用存在性、版本、scope与claim类别检查。边界：不是农学事实正确性的证明。
- backend/internal/flow/reviewer.go：宿主规则审核、冲突/不确定标识。边界：词项支持率启发式，不能替代农学专家或真实模型评测。
- industry-packs：core、energy-storage、education-math行业包结构可作模板。边界：无agriculture行业包。
**仍需实现或集成**
- DT-073 建立农业知识目录与来源权利：建立农业知识目录、权利/时效/地区/作物适用模型及UI；Loom材料与GIS事实仅受控引用，不把通用sources当完整领域模型。
- DT-074 接入首批真实知识来源和人工上传：在获准范围接真实农业来源和人工上传；增加解析损坏/重复/撤回处理和可核对采集记录；凭据不足只阻塞相关live适配器。
- DT-075 实现租户和农场范围的证据检索：在现有tenant/workspace检索上加农场/作物/授权范围与来源有效性门禁，覆盖FTS、向量、read、引用及缓存；返回原版本和适用性。
- DT-076 管理知识修订索引和重新计算：将农业知识revision、分块/嵌入版本与幂等索引任务接通；撤回/删除影响新回答、索引缓存及报告引用，历史证据保留。
- DT-077 防止外部资料提升Agent权限：实现农业检索到Agent的不可信内容边界，工具独立服务端授权/预算/出站控制；建立注入、跨租户与秘密外传负例。
- DT-078 建立农业信息采集与周期报告调度：将Loom周期采集/brief接农业授权、预算、时区、暂停撤回和草稿审核；验证重复tick/重启不重复版本或扣费。
- DT-079 建立农业术语规则和知识适用性：补作物/品种/物候/灾害/单位术语及地区季节适用规则、专家审阅和出处；不把现有行业词典直接作农技规则。
- DT-080 验证知识回答的证据与拒答能力：冻结农业知识评测集及模型配置，逐例检查引用/支持度/冲突/过时/缺数/越权并人工复核；保留失败和修复后结果。
**证据与范围限制**
- 审计基线7c6e0229e3e708ab540f338a7e01cb0f24d25a12；只读源码/规格核对，未修改源代码/任务状态，未安装依赖、运行模型或执行live操作。
- 直接源码核对：engine.ts revokeSource取消采集，但FTS/向量searchDocuments与readDocument未按来源revoked/权利到期/农业适用性过滤；需重接和负例测试，不能只复用接口。
- Loom当前实现是通用知识引擎；本审计未调用来源、模型或嵌入服务，也无农业引用质量live结果。
**外部输入**
- 获准农业手册/RSS/公开资料及使用/发布/期限证据；账号型连接器凭据和许可
- 目标作物/地区/物候术语与专业审阅人员
- 可用模型/嵌入路由、隐私发送授权与测试预算（无凭据仍可完成fixture/接口/权限）

### W10 Agent工具技能

**现有可复用代码**
- backend/internal/flow/subject.go
- backend/internal/flow/context.go
- backend/internal/flow/runchain.go
- backend/internal/flow/tools.go
- backend/internal/flow/mcp.go
- backend/internal/flow/skills.go
- backend/internal/flow/capability.go
- backend/internal/agriculture/routes.go
- services/agri-gis/frontend/agriculture/App.vue
- backend/cmd
**已存在的具体能力**
- backend/internal/flow/subject.go：服务端从用户+subject重解析scope。边界：仅canvas/intelligence_workspace/topic；无farm/parcel/crop-season农业subject。
- backend/internal/flow/context.go：服务端上下文、最近消息、证据范围、操作回执与后台任务投影。边界：无农业revision/质量/图层上下文。
- backend/internal/flow/runchain.go：共享Agent运行链扩展基础。边界：不是农业专属adapter与验收。
- backend/internal/flow/tools.go：固定有类型工具、参数校验、权限、幂等request hash与审批关联。边界：现有intel工具不是GIS真实读写工具。
- backend/internal/flow/mcp.go：MCP白名单与Schema漂移阻断。边界：需农业工具注册、服务身份与外部发布契约。
- backend/internal/flow/skills.go：Skill内容hash、安装与run版本固定。边界：无首发农业Skill集合。
- backend/internal/flow/capability.go：run能力快照固定工具/模型/价格/Skill。边界：需农业参数与资源版本冻结，不能把自定义提示词当授权。
- backend/internal/agriculture/routes.go：农业服务注册与farm/parcel/asset typed dispatcher。边界：未接农业Agent；JobEvents生产provider仍为nil关闭。
- services/agri-gis/frontend/agriculture/App.vue：真实农业会话及farm/parcel/asset入口。边界：页面明确影像分析、外业、报告、农业Agent按后续任务开放。
- backend/cmd：现有服务/迁移/支付插件命令入口。边界：未见受控农业Go CLI。
**仍需实现或集成**
- DT-081 扩展农场和地块Agent上下文：扩农业ScopeResolver/ContextProvider；服务端绑定farm/parcel revision/crop season/layer/观测质量，实施上下文裁剪和跨范围拒绝。
- DT-082 实现农业只读工具与MCP契约：注册农业地块/影像/观测/作业状态只读工具与MCP契约；真实GIS调用、输出上限、资源重授权、schema漂移负例必须跑通。
- DT-083 实现分析及业务写工具的受控执行：新增收费分析和允许写入工具，绑定call/actor/resource revision/budget/idempotency；经平台operation到GIS job，不阻塞模型等待栅格。
- DT-084 构建农业Skill包及版本固定：实际创建农业长势巡检/灾害初评/农场报告Skill与industry pack；声明输入输出/required_tools/缺数分支，验证hash导出一致和run固定。
- DT-085 提供预置和租户自定义农业Agent：实现预置农业Agent与租户自定义配置/知识范围/工具子集/预算；权限取创建者及运行者交集，不开放任意脚本。
- DT-086 实现具体操作预览审批及版本校验：实现农业具体差异预览与批准绑定hash、资源版本、范围、费用上限、有效期；执行时重查并覆盖过期/篡改/重放/并发改图。
- DT-087 实现聊天地图图表与任务联动：把农业聊天接真实授权地图/图表/job卡片，白名单结构化事件且与模型文字分离；SSE重连/撤回/截断/恶意事件负例。
- DT-088 交付受控CLI开放API和MCP集成包：交付Go农业CLI、同权API、版本化OpenAPI/SDK示例与外部MCP包；两租户登录/查询/提交/取消/受控下载/幂等/退出码验收。
**证据与范围限制**
- 审计基线7c6e0229e3e708ab540f338a7e01cb0f24d25a12；只读源码/规格核对，未修改源代码/任务状态，未安装依赖、运行模型或执行live操作。
- Flow subject/tools/usage/catalog检查显示农业运行适配仍缺；通用Pi/RunChain/MCP/审批机制存在只减少重建工作，不证明DT081-088完成。
- 农业profile与API真实路由边界仅证明当前开放面；没有运行农业自然语言→GIS→地图/报告的端到端测试。
**外部输入**
- 前序W03-W09真实授权GIS/观测/作业/知识API可用
- 真实Agent模型路由、测试预算与数据外发范围；预置角色/租户策略评审

### W11 报告审核

**现有可复用代码**
- services/agri-gis/backend/app/services/statistics_report_service.py
- services/agri-gis/backend/app/services/statistics_report_renderer.py
- services/agri-gis/backend/app/services/disaster_report_service.py
- services/agri-gis/backend/app/services/review_service.py
- services/agri-gis/backend/app/services/service_sharing_service.py
- services/loom-engine/flow/reports.ts
- backend/internal/agriculture/object_gateway.go
- services/agri-gis/backend/app/agriculture/main.py
**已存在的具体能力**
- services/agri-gis/backend/app/services/statistics_report_service.py：旧GIS统计报告ZIP/XLSX/PDF、清单hash、过期和下载复核。边界：旧project/task模型与本地storage；农业profile不挂载该路由。
- services/agri-gis/backend/app/services/statistics_report_renderer.py：现有中文表格与PDF渲染基础。边界：需面向新农场/种植季数据快照适配、逐页实际QA。
- services/agri-gis/backend/app/services/disaster_report_service.py：旧灾害XLSX与分布图、来源审计。边界：不等于新农业报告PDF+JSON以及质量/事实语义。
- services/agri-gis/backend/app/services/review_service.py：旧三级任务审核和图斑版本回退。边界：不是immutable ReportRevision的draft/reviewed/published/withdrawn流程。
- services/agri-gis/backend/app/services/service_sharing_service.py：旧共享服务登记、审批、凭据hash、撤销、健康与使用事件。边界：需对新农业报告/对象权限与即时撤回重接，不可直接暴露旧路由。
- services/loom-engine/flow/reports.ts：知识报告/claim与原始引用revision。边界：知识报告与GIS报告事实分属不同数据所有者，需受控引用集成。
- backend/internal/agriculture/object_gateway.go：当前受控对象代理、授权重查基础。边界：不等于报告发布/分享/来源撤回全闭环。
- services/agri-gis/backend/app/agriculture/main.py：独立农业ASGI路由边界。边界：明确不import legacy API，当前只安装身份/对象/job边界/farm/parcel/asset。
**仍需实现或集成**
- DT-089 实现农业报告结构与生成任务：新农业周期/长势/灾害报告schema及持久生成job；绑定输入manifest、GIS结果、现场与知识证据，JSON/PDF同一快照和缺数表达。
- DT-090 实现可追溯地图图表及导出渲染：适配旧渲染器生成新报告PDF/地图/图表原件，含图例、尺度、CRS、时间、来源署名与正确单位；逐页中文/长表QA。
- DT-091 实现报告审核发布与版本冻结：实现不可变ReportRevision及draft/reviewed/published/withdrawn状态、内容/输入hash批准和作者审核分离；修改重新审核。
- DT-092 实现受控导出分享及访问撤销：接对象级授权的报告导出清单/hash与分享范围/期限/撤销；下载复核版本/权利，禁止永久公开桶和跨租户内容。
- DT-093 统一不可变审计和证据追溯：统一跨Go/GIS/Loom的追加审计，关联actor/tenant/resource version/approval/request/run/job/result；验证普通角色不可改历史并脱敏。
- DT-094 实现报告来源撤回和内容事故流程：建立来源撤回影响图、报告事故处置与撤回共享/下载/检索缓存；修正版重新审批，历史撤回版本不能直接恢复为有效。
- DT-095 验证报告中的事实推断预测及限制：强制区分观测事实/统计推断/预测；质量、时域、作物限制、未完成外业显式呈现；阻断无依据数字/建议并做专家审阅。
- DT-096 完成报告交付与复现验收：用真实授权分析/巡查材料执行生成→审核→发布→下载→撤回与可复现重建；留存软件版本、输入清单、PDF逐页检查。
**证据与范围限制**
- 审计基线7c6e0229e3e708ab540f338a7e01cb0f24d25a12；只读源码/规格核对，未修改源代码/任务状态，未安装依赖、运行模型或执行live操作。
- legacy report/review/sharing实现保存在树内，但新农业main.py不挂载legacy router，domain_boundary只开放显式小集合；必须重接可信身份、数据模型和权限。
- 旧项目任务审核不是新报告revision批准；XLSX/PDF生成代码不等于本次真实PDF产物和逐页QA。
**外部输入**
- 真实获准农场/影像/外业材料与可访问来源版本
- 独立作者/审核者与农学专家；正式报告模板/署名/发布和分享策略
- 生产对象存储与下载域名（live验收用；本地可用隔离兼容存储）

### W12 商业计量

**现有可复用代码**
- backend/internal/flow/billing.go
- backend/internal/flow/usage.go
- backend/internal/flow/capability.go
- backend/internal/app/payment.go
- backend/payment-sdk/types.go
- backend/internal/flow/slots.go
- backend/internal/app/storage_quota.go
**已存在的具体能力**
- backend/internal/flow/billing.go：整数微积分预算预留、父子分配、结算/释放及未知结果reconciliation。边界：需接GIS quote/operation/job/attempt用量与农业权益，不可重建第二账本。
- backend/internal/flow/usage.go：冻结价格版本、幂等用量、LLM/OCR/ASR/MCP/存储/通知/影视/数学维度。边界：无农业像元/计算/面积/设备计量维度。
- backend/internal/flow/capability.go：run价格与能力冻结。边界：需遥感job冻结价与重试结算边界。
- backend/internal/app/payment.go：通用充值/订单与支付provider调用基础。边界：不等于农业订阅权益、对公到账复核或已授权真实收费。
- backend/payment-sdk/types.go：Provider含CreateOrder/QueryOrder/CloseOrder/VerifyNotification/DownloadTradeBill。边界：当前接口不含退款/订阅续期方法；需选择性扩展与渠道验证。
- backend/internal/flow/slots.go：通用租户并发槽基础。边界：不是农业像元/存储/面积/套餐统一配额。
- backend/internal/app/storage_quota.go：现有存储配额基础。边界：需农业对象生命周期及租户计量适配。
**仍需实现或集成**
- DT-097 实现套餐权益和降级策略：建立版本化农业套餐权益与server-side席位/农场/面积/存储/计算/Agent配额；实现试用到期/降级/欠费的只读与导出窗口。
- DT-098 实现遥感任务估价和预算预留：新增遥感计量及冻结价格的quote/预留→operation/job链；整数微积分、并发余额竞争、同键重放和任务创建失败补偿。
- DT-099 实现真实用量和成本采集：采集真实像元/计算单位、token、存储周期/流量，关联tenant/job/attempt/price_version并幂等；成本和售价分开、未知成本显式。
- DT-100 实现结算退款和不确定结果对账：把现有预算机制接农业结果/取消/重试/崩溃/迟到事件；未知外部结果保留必要预留进入对账，追加退款不改旧账。
- DT-101 实现支付订阅与获准收费通道：实现所选支付或用户选择的manual_invoice流程、农业订阅续期/权益冻结、退款/验签/去重/金额币种与到账复核；未验真实渠道关闭。
- DT-102 统一API计算与消费限流：统一Web/Agent/CLI/MCP/Worker入口的租户配额与限流，覆盖面积/像元/计算并发/存储/日预算，返回明确原因和恢复建议。
- DT-103 实现运营客服和限时支持授权：实现运营客服的限时、限对象、理由/工单支持授权及审计；默认只有服务指标/账单，不授予万能客户内容访问。
- DT-104 完成账单对账与真实收费路径验收：逐项对账真实任务用量/内部账本/provider账单；获准真实收费/退款或对公到账试点；独立复核余额守恒、差额/未知成本。
**证据与范围限制**
- 审计基线7c6e0229e3e708ab540f338a7e01cb0f24d25a12；只读源码/规格核对，未修改源代码/任务状态，未安装依赖、运行模型或执行live操作。
- 现有Flow预算是可复用账本基础；农业任务并未连接像元定价、套餐、支付订阅及用量守恒验收。
- 支付插件和回调代码的存在不证明商户/真实交易/退款授权或live成功；本审计未发起任何资金动作。
**外部输入**
- 用户确认套餐/价格/服务成本及收费模式，不擅自制定最终人民币售价
- 所选商户沙箱/生产凭据或明确manual_invoice对公到账流程和独立财务复核
- 真实小额支付/退款或对公试点具体授权、provider账单与独立复核者

### W13 运维

**现有可复用代码**
- docker-compose.agriculture.yml
- deploy/agriculture/Dockerfile.web
- deploy/agriculture/Dockerfile.parser
- scripts/agriculture/sbom.py
- docs/agriculture/dependencies/inventory-gaps.json
- backend/internal/flow/metrics.go
- deploy/flow/monitoring/alerts.yml
- deploy/flow/backup/backup.sh
- deploy/flow/backup/restore.sh
- deploy/flow/backup/daily.sh
- backend/cmd/migrate-schema/main.go
- services/agri-gis/backend/app/agriculture/migrate.py
- scripts/windows-proxy.ps1
**已存在的具体能力**
- docker-compose.agriculture.yml：明确本地W00基线、三库/Redis/平台/Loom/探针/同源web内网隔离。边界：文件明确不是production；没有完整GIS业务worker/对象存储/Loom双向回调接线；镜像版本标签非digest锁定。
- deploy/agriculture/Dockerfile.web：独立农业前端构建入口。边界：需最终候选镜像SBOM、权限与可重复构建核验。
- deploy/agriculture/Dockerfile.parser：受限文件解析镜像基础。边界：不是完整栅格worker生产构建。
- scripts/agriculture/sbom.py：已实现实际依赖/可达构建图/许可通知库存检查。边界：脚本自己声明不是最终镜像SBOM/法律意见。
- docs/agriculture/dependencies/inventory-gaps.json：记录实际许可/通知缺口。边界：commercial_release_approved为false；不能把库存已生成写成商用授权齐备。
- backend/internal/flow/metrics.go：token保护的Flow聚合Prometheus指标。边界：缺农业全链job/质量/像元成本与trace关联验收。
- deploy/flow/monitoring/alerts.yml：通用Flow队列、租约、预算、通知和reconciliation规则。边界：需农业指标与获准通知接线/故障注入。
- deploy/flow/backup/backup.sh：单库pg_dump、hash、manifest基础。边界：不是三库+对象一致点协同快照。
- deploy/flow/backup/restore.sh：单库恢复及manifest比对。边界：需真实农业跨库/对象/授权/账本恢复与RPO/RTO。
- deploy/flow/backup/daily.sh：多数据库依次备份/对象镜像/保留调度。边界：顺序dump并不证明跨库一致性；未接农业恢复演练。
- backend/cmd/migrate-schema/main.go：Go显式迁移命令。边界：需农业升级/回退完整过程。
- services/agri-gis/backend/app/agriculture/migrate.py：GIS显式迁移命令。边界：现有迁移组件证据不是生产规模升级恢复。
- scripts/windows-proxy.ps1：现有Windows代理辅助脚本。边界：不是Windows11/WSL2农业全栈可复现环境诊断/验收。
**仍需实现或集成**
- DT-105 建立生产构建供应链与依赖门禁：形成Go/Pi/Loom/GIS Worker/Vue完整农业生产构建、依赖与镜像digest锁定、实际镜像SBOM/许可漏洞门禁、非root与秘密注入。
- DT-106 完善Windows11和WSL2本地开发路径：交付Windows11+WSL2完整农业本地入口/只读诊断，覆盖带空格路径、卷权限、CRLF、代理及离线提示；需实际目标环境验证。
- DT-107 建立私网部署HTTPS和环境隔离：完成dev/acceptance/production全栈profile接线、HTTPS网关/私网服务、真实readiness、优雅停止/轮换/生产拒绝fixture及重启故障验证。
- DT-108 实现全链路监测日志与告警：接跨服务request/trace/run/job/tenant关联日志和农业错误/租约/质量/成本指标；告警处理说明、获准渠道与秘密/签名URL扫描。
- DT-109 实现协同备份与真实恢复演练：实现三库+对象受控一致点/清单备份，密钥恢复依赖；隔离环境真实恢复并核对hash、revision、授权、任务、账本，计时RPO/RTO。
- DT-110 实现数据导出注销与保留流程：实现跨农业对象/媒体/报告/知识索引/缓存/共享/运行任务的租户导出注销状态机，冻结新业务、legal hold与最小账务审计保留。
- DT-111 实现迁移升级兼容与回滚机制：建立多服务兼容迁移的expand-contract、dry-run/备份/回退；新旧版本共存事件/API及原Flow业务回归，生产规模副本故障演练。
- DT-112 验证容量性能与故障恢复边界：按记录环境实测10万地块/租户、20并发、5000万像元分块目标与p95/峰值；kill worker/对象写失败/DB不可用故障恢复。
**证据与范围限制**
- 审计基线7c6e0229e3e708ab540f338a7e01cb0f24d25a12；只读源码/规格核对，未修改源代码/任务状态，未安装依赖、运行模型或执行live操作。
- Compose明确W00本地基线；真实main.py可含更多业务代码但Compose仍未完整组装GIS服务/worker/对象存储与双向Loom回调，不能宣称一键production。
- 初始SBOM不是最终镜像SBOM；已有历史Docker/PG证据只覆盖原冻结任务，未在本审计重新运行。
- 单库dump与后续manifest以及多库顺序备份需要明确一致性语义；未获得农业完整恢复/RPO/RTO/容量测量结果。
**外部输入**
- 实际Windows11/WSL2环境
- 获准部署基础设施、域名/TLS、私网/秘密管理、对象存储与恢复测试空间
- 约定容量测试硬件/合法数据量/负载窗口和获准告警接收渠道
- 数据保留/删除/合同/审计legal-hold规则与责任人

### W14 验收

**现有可复用代码**
- specs/agriculture/v1.0.0/tasks.json
- specs/agriculture/v1.0.0/docs/ACCEPTANCE_MATRIX.md
- scripts/agriculture/spec_status.py
- backend/internal/agriculture/isolation_matrix_postgres_test.go
- tests/agriculture/test_golden_fixtures.py
- scripts/agriculture/farm-browser-e2e.mjs
- scripts/agriculture/parcel-browser-e2e.mjs
- scripts/agriculture/asset-browser-e2e.mjs
- specs/agriculture/v1.0.0/docs/implementation/STATUS.md
**已存在的具体能力**
- specs/agriculture/v1.0.0/tasks.json：144项任务、依赖、需求映射与原验收定义。边界：本审计W09-W17全部not_started；不能删验收以改变完成定义。
- specs/agriculture/v1.0.0/docs/ACCEPTANCE_MATRIX.md：80项FR逐条验收与G0-G5门禁。边界：正式矩阵未通过；历史组件成功不可代替完整产品验收。
- scripts/agriculture/spec_status.py：状态、依赖及冻结证据一致性检查。边界：检查通过不证明业务/安全/农学/付费通过。
- backend/internal/agriculture/isolation_matrix_postgres_test.go：真实PG农业权限矩阵组件测试基础。边界：需扩至Agent/CLI/MCP/瓦片/报告/worker全部入口。
- tests/agriculture/test_golden_fixtures.py：几何/影像黄金fixture基础。边界：不是完整算法大样本、真实遥感或农学效果验收。
- scripts/agriculture/farm-browser-e2e.mjs：农场浏览器整链验收工具。边界：不是完整多浏览器/移动/外业/报告/收费UAT。
- scripts/agriculture/parcel-browser-e2e.mjs：地块浏览器整链验收工具。边界：当前DT026证据边界必须按状态原文，不推定全产品完成。
- scripts/agriculture/asset-browser-e2e.mjs：资产目录浏览器整链工具。边界：资产登记与上传处理/遥感算子live不同。
- specs/agriculture/v1.0.0/docs/implementation/STATUS.md：既有任务证据和未验证范围的权威索引。边界：历史冻结来源与本次HEAD须区别；不得把旧CI结果说成本次重跑。
**仍需实现或集成**
- DT-113 完成需求任务和接口覆盖检查：完成80 FR+20 NFR到代码/迁移/UI/API/事件/工具/真实证据的全矩阵映射；自动ID/schema一致性与关键端到端人工抽查。
- DT-114 执行全入口跨租户安全验收：扩当前权限组件测试到Web/Agent/CLI/MCP/下载/瓦片/缓存/SSE/Worker全入口；真实PG普通角色、池scope/撤销/同编号同bbox负例。
- DT-115 执行几何遥感正确性验收：冻结完整坐标/面积/scale-offset/mask/NDVI/变化与统计黄金集，原始输出/版本/参数/容差可复跑，人工复核数据不足和物候解释。
- DT-116 执行作业幂等与恢复故障验收：真实DB+对象存储逐边界故障注入领取/计算/对象写/提交/Outbox消费；证明旧worker、重复事件、取消/成功竞态和重试一致。
- DT-117 执行Agent证据与农学效果评测：冻结真实模型/提示/技能/工具schema/农业数据；测工具选择、引用/数值、拒答/越权/注入与现场误报漏报，专家复核与修复复测。
- DT-118 执行费用守恒及支付回调验收：农业预算/部分消费/取消/退款/未知对账并发崩溃验收；区分sandbox/manual_invoice/真实支付证据，独立核验账单和权益。
- DT-119 完成浏览器移动和可访问性验收：完整农业UI在Chromium+非Chromium、真实Android/iOS验收；弱网定位相机/补传、键盘焦点/标签/表格替代与时区/单位/DST。
- DT-120 完成真实客户端到端UAT：真实客户角色执行建农场/导地块/处理影像/Agent/外业/审核报告/获准收费全链UAT，冻结脚本、缺陷整改复测和客户签认。
**证据与范围限制**
- 审计基线7c6e0229e3e708ab540f338a7e01cb0f24d25a12；只读源码/规格核对，未修改源代码/任务状态，未安装依赖、运行模型或执行live操作。
- 仅做静态证据/代码审计，未重跑测试、构建、CI、live或浏览器；所有原历史结果须连同其source_commit和环境引用。
- 测试文件存在不代表已执行；fixture/contract/integration/live/farm/paid/device必须分层。
- DT113-120正式全部not_started；现有25 done与2 in_progress的早期组件不等于80 AC或G0-G5通过。
**外部输入**
- 可用真实PostgreSQL/PostGIS、对象存储及隔离故障注入环境
- 合法影像黄金样本与真实模型预算/农学专家
- Chromium+非Chromium、真实Android/iOS设备
- 真实客户、数据授权、审核角色和已获准收费路径/独立财务复核

### W15 发布

**现有可复用代码**
- web/src/features/agriculture/main.tsx
- web/src/features/agriculture/organization.tsx
- services/agri-gis/frontend/agriculture/App.vue
- deploy/agriculture/README.md
- docs/agriculture
- .github/workflows/publish-images.yml
- specs/agriculture/v1.0.0/docs/implementation/STATUS.md
**已存在的具体能力**
- web/src/features/agriculture/main.tsx：平台登录、组织和真实工作区空状态入口。边界：不是完整首农场导入向导/演示沙箱商业隔离。
- web/src/features/agriculture/organization.tsx：组织成员/工作区管理基础。边界：需完整客户初始化、角色引导及中断恢复UAT。
- services/agri-gis/frontend/agriculture/App.vue：新农业Farm/Parcel/Asset界面组装。边界：不是旧GIS全功能页面迁移；后续Agent/影像/外业/报告未开放。
- deploy/agriculture/README.md：本地受控基线说明及未接通事项。边界：不是生产部署手册；部分历史说明需按实际代码修订。
- docs/agriculture：身份/权限/农场/资产/迁移/验收专题文档。边界：缺面向完整商业产品的运维/客户/API/CLI/MCP与故障演练闭环。
- .github/workflows/publish-images.yml：既有镜像发布工作流基础。边界：工作流存在不是农业发布候选、生产域名或许可已通过。
- specs/agriculture/v1.0.0/docs/implementation/STATUS.md：明确全部G0-G5未通过及外部依赖登记。边界：不能由局部任务done推成商用就绪。
**仍需实现或集成**
- DT-121 完善客户初始化与首个农场引导：基于真实组织/农场入口补空租户入门/邀请/首农场导入向导、CRS/许可/种植季及错误修复；演示沙箱不能产生正式报告/账单。
- DT-122 完成商用产物与授权发布检查：对实际候选镜像/静态产物审许可/SBOM/模型数据权利/域名和外部服务/收费门禁，排除默认口令、旁路、tldraw和公共私有桶。
- DT-123 演练真实升级数据迁移和回退：在授权数据副本演练完整真实升级与失败回退；指定窗口/责任/触发条件，核对任务/文件hash/账本无重复。
- DT-124 准备受限发布与部署执行：提交具体域名/网络/资源/费用/回退计划，在授权受限环境部署并执行业务/隔离/报告冒烟；公网/付费资源/通知另需明确授权。
- DT-125 开展获准真实付费试点：选定真实客户/农场/时段/支持责任/收费方式开展授权付费试点；留存使用成功率、误报、交付、单位成本/反馈及整改。
- DT-126 完成首个商用版本发布门禁：在S0-S3全部必须需求和真实启用依赖证据齐备后独立复核G0-G3；公开未完成/外部阻塞，不把S4/S5算进首版完成。
- DT-127 交付开发运维客户使用文档：交付与最终版本相符的开发/部署/升级/恢复/故障/对账/API/CLI/MCP及客户手册，证据索引和下一Agent入口；独立操作者照做验收。
- DT-128 建立上线后质量成本与反馈维护流程：建立上线缺陷、数据事故、费用异常/客户反馈入口与责任；复核来源/模型质量/成本/权限流程，补丁回归回退和S4/S5进入条件。
**证据与范围限制**
- 审计基线7c6e0229e3e708ab540f338a7e01cb0f24d25a12；只读源码/规格核对，未修改源代码/任务状态，未安装依赖、运行模型或执行live操作。
- 没有将已有镜像发布workflow或README当作实际农业商用发布；全部门禁未通过，收费/公网部署需具体授权。
- 某些旧文档的历史路径/待办说明早于当前代码；后续交接以实际文件/状态/证据哈希为准，不照抄不存在的doctor.py等命令。
**外部输入**
- 上线域名/资源/费用/商业授权材料与实际候选镜像
- 授权生产数据副本、升级窗口和回退责任人
- 受限部署/公网开放/付费资源/客户通知的具体授权
- 真实客户/农场/时间范围/支持责任、数据与收费授权、独立UAT签认

### W16 持续监测

**现有可复用代码**
- services/agri-gis/backend/app/models/monitoring_network.py
- services/agri-gis/backend/app/services/monitoring_network_service.py
- services/agri-gis/backend/app/dao/monitoring_network_dao.py
- services/agri-gis/frontend/src/views/MonitoringNetworkView.vue
- backend/internal/flow/notify.go
- services/agri-gis/backend/app/agriculture/domain_boundary.py
**已存在的具体能力**
- services/agri-gis/backend/app/models/monitoring_network.py：旧站点/设备/遥测(设备+幂等键唯一、设备时间/接收时间)、故障与告警模型。边界：project模型，不是新tenant/farm/site独立设备凭据体系。
- services/agri-gis/backend/app/services/monitoring_network_service.py：旧设备登记、遥测request hash、人工模型结果/告警/回执登记。边界：输入operator_code；无独立设备凭据；直接回写last_telemetry_at为请求时间，需修复迟到/乱序状态；人工回执非真实通知网关。
- services/agri-gis/backend/app/dao/monitoring_network_dao.py：设备/遥测/告警存取与现有查询结构。边界：需新农业scope/RLS和事件幂等/质量/分区与历史版本适配。
- services/agri-gis/frontend/src/views/MonitoringNetworkView.vue：旧监测UI与流程参考。边界：被农业vite profile排除，不能视为新产品已可用。
- backend/internal/flow/notify.go：email/webhook目标授权、撤销、稳定delivery id、有限重试与未知结果。边界：当前事件白名单是报告/操作/计划/研究，非农业设备告警与确认升级闭环。
- services/agri-gis/backend/app/agriculture/domain_boundary.py：农业运行角色只开放明确名单旧表。边界：monitoring设备/模型/报告等legacy业务表未因此开放；worker无默认领域CRUD。
**仍需实现或集成**
- DT-129 建立设备站点注册与独立身份：将旧设备登记适配tenant/farm/site身份与序列号约束，新增每设备独立凭据/轮换撤销/最小权限、单位/校准/安装版本及真实设备验收。
- DT-130 实现遥测接入去重及离线补传：按选定协议交付HTTP或MQTT设备网关，去重/同键异载荷、设备/接收时间、离线批补/迟到乱序/坏单位质量；不能复用人身份替代设备认证。
- DT-131 建立观测时序存储与质量版本：建立遥感+传感观测索引与测量/接收/校准/质量/更正不可变历史，按实测选择PG分区索引和保留，防止删除报告依赖。
- DT-132 实现对象状态汇总与历史回放：实现稳定对象身份的最新状态/过期/离线与当时边界/设备关系/质量版本回放；排除未来数据，观测与预测分图层。
- DT-133 实现规则告警抑制及通知闭环：实现可版本化阈值/持续时间/恢复/抑制规则，告警去重、离线/坏数据/业务风险区分、真实送达/确认/升级与工单反馈。
- DT-134 接入持续观测Agent与周期解释：新增设备/时序只读工具与持续监测Skill、质量和异常证据解释及地图联动；巡查建议仍审批，提示注入不能转成设备控制。
- DT-135 验证设备规模和时间序列恢复：按真实设备数/频率/网络积压测吞吐、补传、查询、规则时延、去重/丢失与恢复，仿真/实机分列，不外推万台能力。
- DT-136 完成持续监测现场验收：获准现场持续运行设备→观测→回放→告警→人员反馈并原始记录对账；明确安装网络/维护责任与真实G4结论。
**证据与范围限制**
- 审计基线7c6e0229e3e708ab540f338a7e01cb0f24d25a12；只读源码/规格核对，未修改源代码/任务状态，未安装依赖、运行模型或执行live操作。
- 旧MonitoringNetwork是可复用领域雏形，不是农业运行域已开放。真实设备接口、独立凭据、现场运行/通知均未验。
- ingest_telemetry只做设备+幂等键及payload hash复用，直接写last_telemetry_at=request.observed_at；需新增单调最新状态/迟到质量规则。
- deliver_alert记录提交的回执，并非发送到真实通知渠道；不可把回执表有值当作live送达。
**外部输入**
- 真实设备/厂商协议/独立凭据配置条件、站点安装网络与负责人
- 试点设备数量/频率/校准/离线积压条件
- 通知渠道与明确获准接收人、现场持续运行窗口和处理人员

### W17 预测决策

**现有可复用代码**
- services/agri-gis/backend/app/models/monitoring_network.py
- services/agri-gis/backend/app/services/monitoring_network_service.py
- services/agri-gis/backend/app/services/growth_monitoring_service.py
- backend/internal/flow/billing.go
- backend/internal/flow/capability.go
- services/agri-gis/backend/app/agriculture/main.py
**已存在的具体能力**
- services/agri-gis/backend/app/models/monitoring_network.py：PestModelVersion保存模型版本/训练评估URI/hash/指标；PestAssessment保存输入hash、用户提交的预测标签/置信度和人工审核。边界：只有登记模型/结果的基础，不是实际推理引擎、独立校准或已验证预测。
- services/agri-gis/backend/app/services/monitoring_network_service.py：register_model / create_assessment / review_assessment与专家咨询流程。边界：register_model直接采用提交metrics并设active；没有农业模型权利/区域作物适用性/独立效果门禁；assessment接收预测值而非计算。
- services/agri-gis/backend/app/services/growth_monitoring_service.py：旧长势遥感计算/产物相关基础。边界：历史长势分析不等于未来作物预测或情景模型。
- backend/internal/flow/billing.go：受控预测/情景计算可复用预算机制。边界：尚无预测任务、模型版本、场景参数/费用接线。
- backend/internal/flow/capability.go：run能力/模型/工具版本冻结基础。边界：不能替代农学模型权重/训练数据/校准证据冻结。
- services/agri-gis/backend/app/agriculture/main.py：新农业入口默认不装载旧预测/设备模块。边界：仍需实现默认禁用控制、具体命令白名单、现场联锁与授权；不等于受控硬件能力已交付。
**仍需实现或集成**
- DT-137 建立农学模型注册与适用条件：在旧模型登记之上实现模型卡、代码权重/训练评估数据许可、版本hash、输入单位/资源、区域作物物候适用性及实验/生产门禁。
- DT-138 完成真实数据校准与独立评估：真实数据按farm/year/space防泄漏拆分，独立校准与简单基准/误差/专家阈值评估；缺真值保持experimental，不采信登记时自报metrics。
- DT-139 实现带不确定性的预测结果：实现实际预测任务与冻结输入/模型/参数/时域，误差区间和明确非观测图层；缺数/超适用域拒绝、随机性或复现可核验。
- DT-140 实现情景假设及方案比较：新增显式情景假设/基线/参数差异及多方案比较，计算前估价预留/额度；结果展示误差/限制/费用而非事实。
- DT-141 建立决策建议与人工执行计划：实现有证据、条件/单位/窗口/风险的可审阅建议→人工执行计划，版本化具体批准与执行反馈；不准LLM编处方直接执行。
- DT-142 验证干预效果误报与业务收益：预先冻结效果指标/观测窗口/对照方法，跟踪真实执行到后续观测及误报漏报/成本收益，专家复核；无对照不得声称因果增益。
- DT-143 实现默认禁用的设备控制适配：实现默认禁用的独立设备控制adapter/dry-run，限定命令/范围/期限/幂等回执、现场联锁/限额/手动接管/急停；真实物理发送单独授权。
- DT-144 完成预测决策阶段验收和能力声明：逐项汇总模型/预测/建议/现场效果证据等级与G5边界，独立复核可生产/实验/禁用声明；不启用控制可独立发布已验预测，启用则额外要求DT143现场验收。
**证据与范围限制**
- 审计基线7c6e0229e3e708ab540f338a7e01cb0f24d25a12；只读源码/规格核对，未修改源代码/任务状态，未安装依赖、运行模型或执行live操作。
- PestModelVersion/Assessment注册采用请求提供的评估/预测数值；不等于运行模型或校准独立验证，不能据此宣称预测准确率。
- 未发现新农业profile挂载的预测任务/情景/物理控制链；对未完整检索的外部/上游能力不作不存在断言。
- 模型/农时/现场效果有客观外部时间依赖；模拟器和短期fixture不能替代G5。DT143物理控制是条件启用项，不应阻塞经独立验收的无控制预测。
**外部输入**
- 选定模型代码/权重/训练评估数据权利、输入数据及目标作物区域
- 足够跨度真实标注数据、独立验证集/农学专家和实际农时
- 建议执行/效果对照与现场责任人；预测计算的预算/价格
- 仅启用控制时：用户单独具体授权、厂商命令/安全资料、获准设备、现场联锁/手动接管/应急条件与监督人员

## 4 全部需求追溯与完成规则

`REQUIREMENT-TRACEABILITY.json`逐条保留80FR、20NFR原文、原verification_status、任务映射及未完成任务。`TASK-AUDIT.json`逐条保留144项原任务和审计注释。`REMAINING-TASKS.md`完整列出119项剩余工作。不得以此审计重建新的缩水PRD或删除原依赖。

任务done前必须具备冻结源、命令/退出码/实际结果、源码hash、真实必要依赖测试和证据。逐需求验收还须覆盖验收矩阵中的前置、反例及最低层级；L1不能冒充L2/L3。阶段发布批准另行记录。

## 5 外部条件清单

以下是触及相应任务时的明确输入，不能把全部117项提前标blocked，也不能借外部缺失停止独立工程工作。

- DT-032 迁移旧项目地块与历史业务：迁移客户生产历史需提供授权数据副本及业务字段映射；可先验证公开和fixture样本
- DT-043 实现公开STAC检索及受控影像获取：真实源连通或访问凭证不可用时仅live验收阻塞；fixture通过不代表该源可商用运行
- DT-063 建立遥感与几何科学正确性回归：农学解释的真实适用性需客户或农学专家审阅；自动数值验证可先完成
- DT-064 完成农业分析真实端到端闭环：需可访问的真实合法影像及适当时相；缺失时标blocked_external
- DT-070 实现巡查通知送达与闭环追踪：真实邮件或消息送达验收需提供渠道和明确获准的接收人，未授权不发送
- DT-072 完成真实移动巡查业务验收：需授权地块、真实移动终端及现场人员；模拟定位只能作为fixture验证
- DT-074 接入首批真实知识来源和人工上传：客户私有材料需明确授权；账号型来源缺凭证时只阻塞该适配器
- DT-080 验证知识回答的证据与拒答能力：涉及真实模型质量的评测需可用模型路由；fixture只证明接口及权限行为
- DT-095 验证报告中的事实推断预测及限制：真实农学解释审阅需专业人员；不能以语言模型自评分作为唯一通过依据
- DT-096 完成报告交付与复现验收：真实客户数据和审核角色不可用时，业务live验收标blocked_external
- DT-101 实现支付订阅与获准收费通道：上线收费需用户选择通道及提供真实商户或B2B到账验证条件；未提供时保持该能力未验收
- DT-104 完成账单对账与真实收费路径验收：真实付费操作、退款及商户配置需用户明确授权；缺失时只阻塞收费路径验收
- DT-106 完善Windows11和WSL2本地开发路径：真实Windows11/WSL2验证需相应环境；Linux验证可先完成，Windows状态单独保留
- DT-112 验证容量性能与故障恢复边界：目标硬件或足量授权数据缺失时容量结论保留未验证，不能推算生产SLA
- DT-117 执行Agent证据与农学效果评测：真实模型路由和农学审阅人员需可用；否则效果评测标blocked_external
- DT-118 执行费用守恒及支付回调验收：真实扣款和退款需明确授权；可先完成无真实资金的接口及账本测试
- DT-119 完成浏览器移动和可访问性验收：缺少指定真实设备时对应兼容项保持未验证，模拟器结果单列
- DT-120 完成真实客户端到端UAT：需真实客户、获准数据、审核角色与已选收费方式，未就绪不得标完成
- DT-122 完成商用产物与授权发布检查：需实际上线域名、数据服务及商业授权材料；缺失项对应功能关闭或阻塞发布
- DT-123 演练真实升级数据迁移和回退：真实生产数据迁移需用户提供授权副本及切换计划
- DT-124 准备受限发布与部署执行：公网部署、付费资源与外部客户通知需具体授权；可先完成本地及已授权受限环境验证
- DT-125 开展获准真实付费试点：需明确客户、数据授权及真实收费授权，条件不足保持blocked_external
- DT-129 建立设备站点注册与独立身份：真实设备接入验收需设备及厂商协议；模拟设备只作接口验证
- DT-130 实现遥测接入去重及离线补传：真实设备、网络及协议文档不可用时live接入阻塞
- DT-133 实现规则告警抑制及通知闭环：真实通知通道和接收人需明确授权
- DT-135 验证设备规模和时间序列恢复：设备规模与网络条件由试点确认，不能由单设备结果外推
- DT-136 完成持续监测现场验收：需现场设备、安装网络和负责人员；条件未就绪保持blocked_external
- DT-137 建立农学模型注册与适用条件：选定模型和相关数据权利需核实，未就绪可完成注册框架
- DT-138 完成真实数据校准与独立评估：需真实标注数据、时间跨度及农学专家；不足时模型效果验收阻塞
- DT-142 验证干预效果误报与业务收益：需足够农时和现场执行结果，不能通过短期模拟替代
- DT-143 实现默认禁用的设备控制适配：真实控制需用户单独授权、设备资料与现场安全条件；未满足保持禁用
- DT-144 完成预测决策阶段验收和能力声明：模型和现场效果存在实际农时及设备条件，未满足项如实阻塞对应能力发布

## 6 推荐本地接续顺序

1. 按交付说明克隆**明确产品分支**并核对完整历史，不默认使用main或CI scaffold。保留现有改动；不执行强制重置。
2. 核对026/041本轮CI与差异，修复真实失败；建立当前本地Go/Bun/Python/PostGIS/Docker验证环境。
3. 验收后并行027种植季、031坐标面积、042上传、049作业账本；由单一集成人管理共享迁移/身份/路由/契约文件。
4. 沿原依赖推进W03至W08闭环，再衔接知识、农业Agent、报告和商业能力。任务阶段以tasks.json逐项为准，不只看工作包主阶段。
5. 尽早准备真实影像/地块、模型路由与预算、手机与现场人员、对象存储、收费方式和审核人；只影响相应L3/试点项。
6. 首版S0至S3达到G3后才讨论商用发布；S4/S5按真实设备和模型条件推进。设备控制默认关闭，DT-144对DT-143的依赖仅在启用物理控制范围时适用。

接续提示词和具体运行/获取步骤由同包`LOCAL-AGENT-HANDOFF-PROMPT.md`、`LOCAL-SETUP-AND-RESUME.md`提供。

## 7 本次审计限制

- 本次是固定源码、配置、任务与证据的审计，未重新执行全部已完成测试，未部署生产、调用付费模型或运行真实客户/设备流程。
- 正式任务状态并不等于所有代码不存在；本报告已区分已集成、候选待验收、上游可复用和未来外部验证。
- 原规格README_START_HERE等保留初始“全部未实施”说明，不能作为当前状态；当前权威是tasks.json、STATUS、冻结证据和本审计。
- GitHub完整产品发布、CI scaffold发布及具体CI通过各自是不同事实。交付前必须在发布记录明确分支、提交、树校验与未通过结果，不以推送成功表示产品完成。

