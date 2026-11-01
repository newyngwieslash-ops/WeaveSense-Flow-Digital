# DT025 农场合同（实施中，未验收）

依赖基线a76b0fa8已完成DT024。本文约束并行实现，不表示接口已经交付；遵循原规格与ADR-035。

## 公共 API

前缀 `/api/agri/v1`：GET `/context`，GET/POST `/farms`，GET/PATCH `/farms/{id}`，POST `/farms/{id}/archive`。GET query、写入JSON中的 `workspace_id` 仅为资源选择器，由可信会话重验；禁止tenant/actor/role/operator_code身份字段。新增 GET `/farm-operations/{id}?workspace_id=...` 仅返回当前有权主体的操作状态。

农场输入固定字段：external_code、name、operator_name、可选contact:{name,email,phone}、region_code、dictionary_version、IANA timezone、area_unit(m2/ha/mu)、geometry(Polygon/MultiPolygon)、source_crs、coordinate_order:"longitude_latitude"、data_class(demo/operational)。身份/workspace/data_class不可修改，归档保留编号与历史。联系信息不进入错误、日志或权属回执。

创建附显式initial_grants:[{subject_user_id,capabilities,expires_at?}]；至少一个当前有效且已具适当业务角色的成员，初始capabilities闭集agri.farm.read/update/archive。不自动给creator/owner内容权，不暗改成员角色。owner/tenant_admin仅可发起workspace创建治理；既有农场操作仍须显式grant及角色上限。

使用现有农业canonical envelope。成功创建201要求GIS事实和平台初始grants均已提交；未知/待登记返回持久202操作引用，不伪造farm成功。创建人若无内容权，只得到治理操作回执，不附带farm详情。写入Idempotency-Key；PATCH/archive要求expected_revision，缺失428，版本或同key异payload冲突409，非法/不支持几何422。重试沿用原operation/key。

创建/边界修改/归档先展示具体对象、版本、参数与grant计划；后端确认绑定actor/workspace、对象/expected_revision、规范化参数hash及期限，不用未绑定boolean替代。直接用户确认不是未来Agent审批或成果独立审核。

## Go/GIS 协议与持久化

两端负责人共同冻结版本化schema与golden签名样本再实现，不各自猜字段。workspace-create使用独立严格typ，复用Ed25519/keyring/RFC8785/replay，绑定可信actor、tenant/workspace、epoch、operation、请求hash、grant_plan_hash、method/path/body及最长30秒期限。只允许固定创建操作，不放宽旧business非空真实farm_ids。GIS回查平台持久操作及当前权限后写。

平台保留grant计划；GIS同事务创建farm、不可变command receipt与outbox；平台校验实际回执后同事务消费幂等事件、登记精确grants。网络不持DB事务。扩展现有flow CommandDispatcher受信adapter，不建第二调度器；必须generation/lease fencing CAS。同ID异hash拒绝，完成tombstone防撤权后迟到created重授。事实已建但无授权时保持操作pending，不声称跨库原子回滚。

列表由平台求得明确可读farm集合，GIS再次校验；空集合直接标准空结果，不签空票/伪造farm。非空集合绑定IDs/filter/cursor，游标绑定workspace/actor/授权版本。已有farm grant按稳定ID跨内容revision有效；旧操作票据仍绑定resource_versions，不放宽非farm授权版本比较。

归档后旧active-only resolver拒绝操作，事实历史保留。历史读取若开放须独立明确只读profile及当前显式权限，不把archived伪装active；已有报告保留自身权限/版本。

## GIS、配置与前端

区域字典使用严格版本化部署JSON，支持多区域/时区/单位；无配置明确依赖错误，不默认黑龙江/RS。保留已使用版本快照，code不是已验证行政覆盖。首轮仅显式EPSG:4326或OGC:CRS84与API经纬顺序，记录原始CRS/轴序声明及identity变换；未知/其他CRS明确422，不假造重投影，也不算通用导入完成。

校验有限坐标/范围/闭环/拓扑/孔洞/MultiPolygon重叠/点数，不自动MakeValid。权威面积PostGIS ST_Area(geom::geography,true)，m²固定精度；ha=m²/10000，亩=m²*3/2000。无法证明正确的跨日界线/极区/超大面明确拒绝。正式迁移追加且可重放，精确RLS/角色白名单，不改旧迁移。

Vue复用农业App/SessionController/CSRF/跨标签/撤权/BFCache，专用薄canonical客户端，同源/no-store/取消旧请求，不新登录或旧GIS axios。锁内OpenLayers二维无外部底图，构建只精准放行ol，保留legacy/Cesium/tldraw禁令。

验收包括真实双PG创建/授权/重试/撤权/并发revision/归档，多区域几何golden、迁移重放/空scope、实际浏览器闭环和失败草稿保留。所有新增路由加入DT024精确隔离gate，不以mock或静态页完成任务。
