# WeaveSense-DigitalTwin 数据与 API 契约

版本：1.0.0  
日期：2026-10-29  
本文件是新增农业模块的实现合同，描述待开发的数据、接口和事件。示例用于说明协议，不是可直接部署的服务实现。实施者必须将其转为受版本控制的 OpenAPI、JSON Schema、迁移与契约测试，并在 [任务状态](implementation/STATUS.md) 中记录差异。

## 1. 通用协议约定

### 1.1 路径、身份与命名

对浏览器、CLI 和客户集成暴露的平台入口统一为 /api/agri/v1。GIS 内部入口为 /internal/agri/v1，只接受平台或受控服务身份。旧 AgriScope /api/v1 端点不能绕过新鉴权对公网暴露；兼容路由必须转入同一授权服务。

新增农业 JSON 字段采用 snake_case；现有 Flow 接口维持原合同，由适配器明确转换，不能全仓批量重命名。资源 ID 为服务端生成的不可猜测不透明字符串，示例前缀 farm_、parcel_、job_ 仅帮助辨识，不能包含用户姓名、行政身份证号或密钥。ID 与租户校验共同使用，不能把“不容易猜到”视为权限控制。

租户由平台会话或受限访问令牌解析。workspace_id 可作为选择条件，但必须验证成员及资源关系；客户端传入 tenant_id、operator_code、requester_code 不构成身份，新增写接口对此类冒充字段直接拒绝或由明确兼容适配器剥离并留测试证据。

### 1.2 共用字段

| 字段 | 类型/规则 |
| --- | --- |
| tenant_id、workspace_id | 服务端派生的不透明 ID；内部对象与事件必带；普通请求体不能覆盖 |
| id、revision | ID 不可变；revision 为正整数、严格单调递增 |
| created_at、updated_at | UTC RFC 3339 时间；用户显示时转换为配置时区 |
| observed_at、ingested_at | 观测时间与进入平台时间分开；不得用上传时间冒充观测时间 |
| source_updated_at、forecast_issued_at、target_time | 来源修改、预测生成和预测目标时间分别记录；未知为null，不用当前时间补造 |
| temporal_precision、source_timezone | 来源时间精度及原时区；date 精度另存 observed_date，不伪造秒 |
| data_class | demo、operational；变更必须走受控重新验证，不允许直接改标签 |
| content_sha256 | 实体字节 SHA-256，小写 64 位十六进制；S3 ETag 不等价 |
| parameter_hash | 明确 canonicalization_version 后对规范化参数计算 SHA-256 |
| source_refs、evidence_refs | 指向有访问权限和版本的来源/证据；不放访问密钥或临时签名 URL |
| validity | current、historical、revoked；与执行状态独立 |
| quality_status | valid、degraded、insufficient_data、invalid、unknown |
| area_m2 | 非负十进制定点数；显示单位由服务端转换，禁止用经纬度平方度充当面积 |
| geometry | 对外 GeoJSON；WGS84 经度在前、纬度在后；源 CRS 单独保留 |
| actor_user_id、actor_service_id | 平台可信上下文产生，审计中同时保留执行人与发起人 |

数据库中的业务时间以 timestamptz 或明确日期类型存储；金额、微积分、次数、字节和时长采用整数或固定精度 decimal，禁止二进制浮点账本。CRS、质量、单位和用途枚举必须在 Schema 中冻结，新增枚举兼容策略需要版本说明。

### 1.3 分页、查询与响应

列表接口使用不透明游标，默认 limit=50，最大 200；游标绑定租户、过滤条件、排序键和 schema_version，不能跨租户复用。地图范围查询使用 bbox 与 zoom，明确返回简化精度、是否截断和下一页；禁止隐式返回全租户精细几何。

标准成功响应包含 data、meta.request_id、meta.schema_version；列表再含 next_cursor、has_more。异步操作返回 HTTP 202、operation_id、job_id（尚未创建时可空）、status_url 和 events_url。202 只表示已接受，不表示计算、发布或扣费完成。

采用强ETag/304的详情GET有明确例外：request_id放X-Request-ID响应头，表示体仅含稳定的data及schema等元数据，避免每次请求的追踪ID破坏强验证。不能把实际字节不同的完整响应标成相同强ETag；无法稳定序列化的敏感响应使用no-store与expected_revision，不强行启用304。

标准错误含 error.code、error.message、error.retryable、error.details、request_id。详情不得包含 SQL、内部路径、存储签名、供应商密钥或其他租户信息。

| HTTP | 业务错误码 | 语义 |
| --- | --- | --- |
| 400 | INVALID_ARGUMENT | 参数或格式错误 |
| 401 | UNAUTHENTICATED、TOKEN_EXPIRED | 身份缺失、无效或过期 |
| 403 | CAPABILITY_DENIED、ENTITLEMENT_DENIED | 已知可见资源上的操作能力或套餐不允许 |
| 404 | RESOURCE_NOT_FOUND | 不存在或对当前范围不可见；跨租户不揭示存在性 |
| 409 | REVISION_CONFLICT、IDEMPOTENCY_CONFLICT、IDEMPOTENCY_KEY_EXPIRED | 对象版本或相同幂等键载荷冲突；过期重放不能当成新请求 |
| 409 | APPROVAL_REQUIRED、APPROVAL_STALE | 需要批准或批准快照已变化 |
| 409 | TOO_LATE、LEASE_LOST | 作业已终态或当前 Worker 失去发布资格 |
| 412/428 | PRECONDITION_FAILED、PRECONDITION_REQUIRED | If-Match 不匹配，或版本化写入未提供任何必需前置条件 |
| 402 | BUDGET_INSUFFICIENT | 可计费操作的授权余额不足 |
| 413/415 | PAYLOAD_TOO_LARGE、UNSUPPORTED_MEDIA | 大小或格式超出受控能力 |
| 422 | GEOMETRY_INVALID、CRS_REQUIRED、QUALITY_GATE_FAILED | 几何、坐标或业务质量检查未通过 |
| 429 | QUOTA_EXCEEDED | 并发、调用或容量限额；给出允许的 retry_after |
| 503 | PROVIDER_UNAVAILABLE、DEPENDENCY_UNAVAILABLE | 外部服务或必要依赖不可用；不得返回伪造成功 |

### 1.4 幂等与并发

创建分析、巡查提交、报告发布等客户副作用接口必须接收 Idempotency-Key。持久去重 namespace 为 tenant_id + actor/client_id + operation_kind + api_major_version，namespace 内的 key 唯一，并记录规范化请求 hash；不能只在 Redis 中暂存。actor/client_id 来自可信身份，服务端生成 operation_kind，不允许通过随意改请求字段规避去重。支付回调使用已验签的 provider + merchant_account + environment + event_id 去重，不假设第三方会发送平台自定义请求头。

相同 key/相同载荷返回原 operation/resource 的当前可见状态，并标记 idempotent_replay=true；相同 key/不同载荷返回 409 IDEMPOTENCY_CONFLICT；并发相同请求只允许一项获得创建权。副作用已经发生但响应丢失时不得删除幂等记录。校验失败且没有产生副作用可以重试，但 key 的载荷绑定规则仍适用；修改输入须使用新 key。重放首先重新鉴权，已撤权者不能凭旧 key 读取响应。

资源详情返回服务生成的强 ETag，绑定 tenant/resource_id + revision + representation_version + effective_projection + authorization_version，并包含实际序列化表示的hash；状态对象另有严格递增 state_revision，每次对外可见状态改变即变更 ETag。不得用对象存储 ETag 代替此业务状态版本。If-None-Match 返回 304 前仍需重新鉴权，并按当前字段投影重新确定ETag；同一资源在降权后不能返回304让浏览器复用之前的完整敏感表示。返回新的授权投影，客户端收到权限版本变化或重新验证发现变化后清除旧敏感缓存；已由用户离线保存的数据不能声称可追回。私有业务缓存不得进入公共CDN；精确个人信息默认no-store，其他内容按private并重新验证策略处理。

面向人工的版本化写操作提交 expected_revision 或 If-Match；两者都有时必须对应同一当前授权资源表示。检查与更新在同一事务进行，body版本冲突返回409 REVISION_CONFLICT，HTTP If-Match失败返回412 PRECONDITION_FAILED，完全缺失必需条件返回428。冲突只给当前可见版本及差异；不得静默覆盖。作业进度ETag与地块revision、Worker fencing_token是不同机制，不能互相替代。

普通API的完整重放响应默认保留至少30天；较早成功操作保留最小tombstone，含namespace、key_hash、request_hash、operation/resource_id与终态，期限不短于该业务对象和允许重试窗口。已清除响应且无法安全恢复的旧key返回409 IDEMPOTENCY_KEY_EXPIRED并给出可用查询指引，不把它视为全新请求。账本、支付、事件和已发布成果的去重凭据按审计策略持久保存，不能24小时后回放即重复扣费。主体/租户注销且合同允许彻底删除其去重记录时，应先永久关闭对应namespace并拒绝后续请求；正常密钥轮换保留相同actor/client namespace，不能使旧请求获得第二次执行。只删除部分业务对象时继续保留必要hash与终态tombstone，不释放同key复用。更改保留期须先分析历史重试和支付通知窗口。

### 1.5 浏览器、隐私和敏感字段

Cookie 会话写接口复用平台统一 CSRF 保护：提交绑定会话的 CSRF token，后端校验 Origin/必要时 Referer，设置 Secure、HttpOnly 与适当 SameSite，按明确来源配置 CORS；SameSite 不能代替完整校验。GET/HEAD 无业务写副作用。CLI 与服务令牌不自动附加 Cookie，采用显式 Authorization，不能以“内部接口”为由跳过认证。此方案参考 [OWASP CSRF 指南](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html)，具体接入点由 W01 与既有会话实现核实。

电话、姓名、设备标识、精确 GPS 和照片 EXIF 按个人/敏感信息分类；列表、客服日志、分析日志和 Agent 默认只接收业务所需的脱敏字段。完整电话及精确现场定位需要明确 field/contact capability 与字段级输出策略；导出、报告、地图共享独立检查。原始证据坐标不因显示脱敏而改写，但公开衍生图件可以按政策模糊并标明精度。图片外发前移除不必要 EXIF/设备信息，保留受控原件和衍生 hash。业务必须使用精确地块边界时按资源权限提供，不能以一刀切模糊破坏空间计算正确性。

## 2. 数据权威与约束

### 2.1 所有权

| 数据库 | 权威内容 | 外部引用处理 |
| --- | --- | --- |
| flow_platform | 用户/租户/工作区/成员、资源授权、审批、Agent运行、operation投影、权益、价格、预算、账本、支付 | 不直接写 flow_geo；通过事件与 API校验农场/任务引用 |
| flow_geo | 农场、地块、种植季、空间版本、观测、资产、分析、巡查、领域审核、报告实体 | 身份引用是外部 ID，授权由平台服务验证 |
| flow_intel | 来源、文档版本、检索索引、知识证据与采集作业 | 农场资料按租户/用途授权，不跨库直接关联查询 |

以下表名是目标逻辑模型。若已存在等价上游表，优先通过迁移补齐，不重复建立两套权威。所有跨租户受保护表均包含 tenant_id；同库外键应使用 tenant_id + id 或等价约束防止错绑。共享公开数据必须有明确独立公共目录和访问策略，不能把空 tenant_id 作为任意数据可公开的后门。

### 2.2 平台新增或扩展表

| 表/实体 | 核心字段 | 必须约束 |
| --- | --- | --- |
| agri_resource_bindings | tenant、workspace、resource_type、resource_id、subject_type/id、capabilities、auth_version | 绑定的资源来自GIS已确认回执；主体必须是当前租户有效成员或服务身份 |
| agri_agent_profiles | tenant、workspace、owner、name、instructions、knowledge_scopes、tool_allowlist、revision | 自定义权限不能超过配置者及执行者能力；版本不可原地覆盖已用快照 |
| agri_operations | operation_id、tenant、actor/client、kind、request_hash、job_id、status、state_revision、reservation_id | namespace + idempotency_key 唯一；GIS job 回执只能绑定同tenant/operation |
| agri_idempotency_records | namespace、key_hash、request_hash、operation/resource_id、response_ref、retention_until、tombstone | namespace + key_hash 唯一；事务创建权；成功副作用后不得先于业务去重凭据删除 |
| agri_approval_requests | actor、action、resource_revision、input_hash、max_cost、expires_at、status、approver | 批准绑定精确版本/参数/费用；变更则过期；独立审核策略防自审 |
| agri_plan_versions | code、version、entitlements、limits、valid_from/to | 已成交或执行所用版本不可修改，只新增版本 |
| agri_subscriptions | tenant、plan_version、period、status、billing_mode、provider_ref | 续订、取消和到期幂等；没有支付/合同凭证不激活付费权益 |
| agri_meter_records | tenant、operation/job、meter、quantity、unit、measured_at、dedupe_key | 同租户同dedupe_key唯一；数量不为负；原始计量与价格解耦 |
| agri_payment_events | provider、merchant_account、environment、event_id、signature_status、order_id、amount、currency、payload_hash | provider + merchant_account + environment + event_id 唯一；同事件不同payload告警并拒绝 |
| agri_reconciliations | operation、reservation、external_ref、reason、state、evidence | 结果未知不能直接标退款完成；调整以新账目记录 |
| agri_support_access | tenant、support_actor、reason、granted_by、expiry、scope | 限时最小范围；客户授权和访问审计不可缺失 |

沿用 Flow 现有 reservation、usage 和 ledger 时，应加农业类型与关联，不新增独立可消费余额。积分账本与法币付款记录分离，转换规则固定版本，不能用积分退款假装支付渠道退款。

### 2.3 农场、地块与种植季

| 实体 | 核心字段 | 必须约束 |
| --- | --- | --- |
| farms | id、tenant、workspace、name、region_code、timezone、area_source、status、revision | 区域与字典可配置；不固定黑龙江；农场范围必须可验证 |
| parcel_identities | id、tenant、farm_id、external_code、lifecycle_status、current_revision_id | tenant + farm + external_code按配置唯一；历史ID不重复利用 |
| parcel_revisions | parcel_id、revision、geometry、source_crs、area_m2、valid_from/to、reason、hash | tenant + parcel + revision唯一；几何合法；时间区间一致 |
| parcel_lineage | operation_id、parent_id/revision、child_id/revision、relation_type、effective_at | 分割/合并不得形成环；父子同租户；保持来源与审批 |
| crop_seasons | id、farm、name、start_date、end_date、timezone、status | 起止合法；跨年允许；重叠是否允许按农场经营策略显式判断 |
| parcel_crops | season、parcel、parcel_revision、crop_code、variety、planting_date、management_mode | 绑定确定边界；分割后不能自动把历史产量全量复制到每个子地块 |
| custom_field_definitions | tenant/farm、code、type、constraints、required、version、active | code稳定，类型变更需迁移；停用保留历史值 |
| parcel_property_versions | parcel_revision、schema_version、properties、hash | 必填与类型校验；规则变更令关联质检需要重检 |

分割/合并在一个领域事务中锁定所有参与地块，校验 expected_revision、几何覆盖、合法性和配置化容差，发布父地块历史状态、子地块及 lineage。面积变化必须可解释；超出容差的真实边界修正走单独审批路径，不伪装成纯分割。

### 2.4 观测、资产与权利

| 实体 | 核心字段 | 必须约束 |
| --- | --- | --- |
| data_sources | provider、collection、endpoint_profile、auth_secret_ref、license_profile、status | 地址与出口允许列表独立配置；返回给客户端仅安全摘要 |
| rights_records | owner/source、permitted_uses、commercial_use、redistribution、attribution、expiry、evidence | 许可未知时禁止正式共享/分发；用途检查覆盖派生产物 |
| assets | id、tenant、farm可空、kind、data_class、status、current_version | kind区分raster/vector/table/photo/audio/model；不可通过改扩展名绕过解析 |
| asset_versions | asset、version、object_version、size、sha256、mime、source_ref、rights、validity | 同版本实体不可覆盖；哈希与实际字节一致 |
| imagery_metadata | asset_version、product_family、sensor、acquired_at、footprint、CRS、grid、bands、quality_assets | 波段语义/单位/scale/offset/nodata必需；QA来源可追溯 |
| upload_sessions | tenant、asset、expected_bytes、parts、expiry、state、checksum | 完成会话不直接令资产ready；会话/分片不能跨tenant或asset复用 |
| asset_lineage | parent_version、child_version、operation、transform、manifest_hash | 关系同租户或显式授权的公共源；无循环 |
| observations | target_type/id、observed_at/date、ingested_at、metric、value、unit、method、quality、evidence | 实测与计算/预测类型分开；保留迟到/纠正记录，不能覆盖事实 |
| observation_revisions | original_observation、replacement、reason、actor | 纠正保留原值；最新视图不能删除历史 |
| tile_products | asset_version、style_hash、grid_profile、permission_version、cache_prefix | 私有路径租户化；源撤回后禁新访问并失效缓存 |

每个资产状态至少包括 uploading、validating、ready、rejected、quarantined、revoked。只有 ready + operational + 权利允许 + 质量满足的版本可用于正式分析；demo 数据只允许 demo 流程，结果和账单均明确标识。

### 2.5 作业、结果与事件

| 实体 | 核心字段 | 必须约束 |
| --- | --- | --- |
| analysis_jobs | job_id、tenant、operation、kind、input_snapshot、params_hash、status、state_revision、outcome、priority、cancel_requested、retry_of_job_id | tenant + operation唯一；输入发布后不可变；状态迁移检查；重试引用同租户旧job |
| command_receipts | tenant、operation、request_hash、state、job_id可空、cancelled_before_accept | tenant + operation唯一；先取消后接单的tombstone阻止迟到命令执行 |
| job_attempts | job、attempt_no、worker、fencing_token、lease_until、started/ended、limits、error | token单调递增；过期attempt不能发布成果 |
| job_events | tenant、job、sequence、type、payload、created_at | job + sequence唯一；回放授权与任务一致 |
| analysis_results | job、result_id、algorithm/version、input_hash、quality、validity、manifest_hash | 同job只发布一个权威结果版本；重算建立新job |
| result_artifacts | result、kind、asset_version、object_version、size、sha256、style | 先校验实体再发布引用；不可含任意客户端路径 |
| analysis_zones | result、geometry、category、score、area、review_status | 候选自动分类与人工确认分离；低质量不能自动确认 |
| review_decisions | target/version、reviewer、decision、reason、evidence、policy_version | 审核绑定hash；目标变化失效；无权自审策略受检 |
| outbox_events | event_id、aggregate、sequence、payload_hash、payload、publish_attempts、next_attempt | 与领域变更同事务写入；发送失败不丢事件 |
| inbox_events | producer、event_id、payload_hash、received_at、processed_at、result | producer + event_id唯一；不同hash同id拒绝 |
| analysis_dependencies | result/report、input_asset_revision、parcel_revision、rule/model_version | 用于来源撤回和新版本导致的有效性重算 |
| orphan_objects | tenant、job_attempt/upload、object_version、reason、eligible_delete_at | 仅清理已确认无有效引用且属于对应attempt的对象 |

作业状态为 queued、running、retry_wait、cancelling、succeeded、failed、cancelled。outcome 为 valid_result、insufficient_data、not_applicable 或 execution_failed；非终态和 cancelled 可为 null，不能用 valid_result 表示取消成功。取消、重试、租约与计费规则详见 [架构](ARCHITECTURE.md)。

### 2.6 巡查、报告与扩展对象

| 实体 | 核心字段 | 必须约束 |
| --- | --- | --- |
| inspections | farm、parcel/version、analysis_zone、assignee、due_at、status、revision | 指派对象属于当前范围；撤权后停止访问 |
| field_submissions | inspection、capture_id、GPS、horizontal_accuracy、observed_at、attributes、payload_hash | tenant + capture_id唯一；GPS/手填来源清楚；内容不同不能幂等覆盖 |
| field_attachments | submission、asset_version、upload_state、sha256 | 部分上传保持 evidence_pending；不能伪称全部上传完成 |
| agricultural_operations | parcel/season、operation_type、performed_at、actor、materials、evidence | 已执行事实与计划分离；数值单位与来源明确 |
| quality_runs | scope_snapshot、rule_version、input_hashes、counts、findings、status | 不可变；来源变化触发失效，不改写旧质检 |
| reports | tenant、farm、report_type、latest_revision、status | 产品报告编号与实体revision分开 |
| report_revisions | report、revision、content_hash、source_manifest、rule/model/skill_versions、quality | 定稿后不可原位编辑；无证据指标不得填充 |
| publications | report_revision、approval、artifact_manifest、published_at、revoked_at | 绑定精确hash；撤回不删除审计 |
| share_grants | resource_revision、recipient_scope、expires_at、allow_download、revoked_at | 默认私有；外部共享须用户明确授权；不得公开所有关联原始数据 |
| device_identities，S4 | device、tenant、farm、credential_ref、protocol、status、revocation | 每设备独立身份；轮换与撤销不能只改UI |
| telemetry，S4 | device、message_id、observed_at、sequence、metric、value、unit、quality | 设备+message_id去重；乱序/重复/迟到可追溯 |
| model_versions，S5 | model_id/version、artifact、license、applicability、training_refs、validation_refs | 无验证或权利不明不得生产启用 |
| prediction_runs，S5 | model_version、inputs、scenario、horizon、intervals、validation_status | 与实测分开；适用范围和误差限制必显 |
| control_commands，S5 | device、approval、command_hash、idempotency_key、expiry、ack、state | 默认关闭；人工授权、互锁及超时；不能重试造成重复物理动作 |

## 3. 领域 API 清单

所有接口都执行 AG-FR-005 的租户隔离与 AG-FR-006 的服务身份规则。表中路径省略统一前缀 /api/agri/v1。写入不允许直接跨租户更改关联 ID。

### 3.1 农场、地块和种植季

| 方法与路径 | 输入要点 | 输出与行为 |
| --- | --- | --- |
| GET /context | 当前工作区选择 | 当前能力、可见农场、单位、时区、feature flags；无密钥 |
| GET /farms | 游标、状态、搜索 | 授权农场摘要 |
| POST /farms | 名称、区域、时区、范围、幂等键 | 201农场；GIS建立事实，平台登记资源授权 |
| GET /farms/{id} | 无 | 详情、revision、允许动作 |
| PATCH /farms/{id} | expected_revision、允许修改字段 | 新revision；冲突409 |
| POST /farms/{id}/archive | expected_revision、理由 | 归档；保留历史和已有报告 |
| GET /farms/{id}/parcels | bbox、season、作物、质量、游标、几何精度 | 授权要素列表、截断标识与next_cursor |
| POST /farms/{id}/parcels | geometry、source_crs、来源、属性 | 新地块及revision；面积由服务计算 |
| GET /parcels/{id} | revision可选 | 当前或授权历史版本 |
| POST /parcels/{id}/revisions | expected_revision、geometry/properties、理由、审批引用 | 新版本、差异和受影响结果数量 |
| POST /parcels/{id}/split-preview | expected_revision、切割线/候选面 | 几何与面积预览、preview_hash，不落正式地块 |
| POST /parcels/{id}/split | preview_hash、expected_revision、approval_id、幂等键 | 子地块、lineage和父地块历史状态 |
| POST /parcels/merge-preview | 同农场地块与各版本、目标属性 | 合并预览与冲突 |
| POST /parcels/merge | preview_hash、expected_revisions、approval_id | 新地块与父地块lineage |
| GET /parcels/{id}/history | 游标 | 边界、属性、审批、split/merge历史 |
| POST /parcels/{id}/restore | historical_revision、expected_revision、理由 | 基于历史内容建立新版本，不删除中间版本 |
| GET/POST /farms/{id}/seasons | 时间/作物或季节配置 | 列表或创建；检查经营时间与地块关联 |
| POST /seasons/{id}/assignments | parcel_id/revision、作物品种、播种日期 | parcel_crops记录；批量需原子策略 |
| POST /vector-imports | upload_session、CRS、字段映射、preview选择 | 202导入任务；显示拒绝行和拓扑问题 |
| POST /vector-imports/{id}/commit | preview_hash、导入策略、审批 | 版本化写入；变化不沿用过期预览 |

复杂地块数据的导入先预检，返回字段映射、几何错误、重复编码和预计变更数量。默认全批次原子提交；若明确选择部分成功模式，每行结果必须可下载，未成功项不隐性丢失。

### 3.2 数据、影像和地图

| 方法与路径 | 输入要点 | 输出与行为 |
| --- | --- | --- |
| GET /assets | kind、farm、时间、quality、data_class | 授权资产列表 |
| POST /assets | 来源、权利、用途、kind、data_class | 创建登记，不代表实体ready |
| POST /uploads | asset_id、文件名、类型、大小、预期checksum | 会话、过期时间、受限分片能力 |
| POST /uploads/{id}/parts | part_no、大小、checksum | 受控上传地址或代理端点 |
| POST /uploads/{id}/complete | 全部分片清单、总大小、checksum | 202校验；相同会话重复完成不重复资产 |
| GET /uploads/{id} | 无 | 已收分片、校验状态和失败原因 |
| DELETE /uploads/{id} | 原因 | 取消会话；延迟回收未引用临时对象 |
| POST /imagery/search | provider、collection、farm/bbox、日期、云量候选阈值 | STAC候选、真实footprint、来源与许可摘要 |
| POST /imagery/coverage-preview | item_ids、目标地块版本 | 真足迹覆盖、缺口和选景建议；不是bbox并集替代 |
| POST /imagery/imports | provider、item_ids、范围、rights确认、quote | 202受控下载、标准化与入库任务 |
| GET /assets/{id}/versions/{version} | 无 | 元数据、质量、血缘、许可、安全预览入口 |
| POST /assets/{id}/revoke | expected_revision、理由 | 阻断新使用，失效共享/检索/派生发布 |
| GET /layers | farm、time、kind | 可用图层和权限版本 |
| GET /layers/{id}/tiles/{z}/{x}/{y} | style_version、time可选 | 在线鉴权瓦片；缓存tenant化 |
| GET /layers/{id}/legend | 无 | 样式、单位、阈值、缺失值及示意标识 |
| GET /assets/{id}/download | version、purpose | 授权流式下载；有界直签仅显式策略允许 |
| GET /parcels/{id}/observations | metric、时间、method、游标 | 观测/预测明确区分的时序 |
| POST /spatial-queries | 受控查询类型、资源、参数 | intersects/area/group_statistics结果；不接任意SQL |

来源权利检查不能仅在入库时执行。分析、报告发布、导出、外部共享和模型训练分别验证 permitted_uses。公开资料也要保留署名，不把客户数据转入跨租户共享索引。

### 3.3 分析任务与结果

| 方法与路径 | 输入要点 | 输出与行为 |
| --- | --- | --- |
| POST /analysis-quotes | kind、输入版本、范围、参数 | quote_id、price_version、预估/上限、取消规则、有效期 |
| POST /analyses | quote_id、主体、输入版本、参数、approval_id、幂等键 | 202 operation_id；预算与outbox已持久化 |
| GET /operations/{id} | 无 | 平台提交/接受/执行/结算投影 |
| POST /operations/{id}/cancel | expected_revision、理由、幂等键 | job尚未回执也可记录取消意图；送达未知先对账，不能提前释放预留 |
| GET /jobs/{id} | 无 | 作业状态、当前attempt、质量、授权后结果摘要 |
| GET /jobs/{id}/events | Last-Event-ID | 授权SSE回放；事件去重与断线续传 |
| POST /jobs/{id}/cancel | expected_revision、理由、幂等键 | 接受取消或TOO_LATE；重复取消幂等 |
| POST /jobs/{id}/retry | 原job、修正后输入或同输入、quote、原因 | 新operation/job，保存retry_of_job_id |
| GET /results/{id} | 无 | outcome、quality、validity、输入/算法版本、结果摘要 |
| GET /results/{id}/zones | bbox、类别、审核状态、游标 | 异常或变化候选；受限响应量 |
| POST /results/{id}/reviews | expected_result_hash、decision、理由、证据 | 不可变审核；人工确认与算法候选分离 |
| GET /results/{id}/artifacts | 无 | 有权限的成果清单，不返回裸存储桶凭据 |
| POST /results/{id}/recompute | 新规则/边界版本、quote | 新任务；原结果保持历史 |

首发分析 kind 至少覆盖 parcel_zonal_statistics、ndvi、ndvi_change、rgb_change_candidate、disaster_intersection 和 thematic_report。每种算法独立登记参数 Schema、默认值、适用来源、资源上限、版本和输出 Schema；不得几个算法名称调用完全相同逻辑却对外声称具有不同农学能力。

### 3.4 外业与报告

| 方法与路径 | 输入要点 | 输出与行为 |
| --- | --- | --- |
| GET/POST /inspections | 条件或任务草稿、资源、责任人、期限 | 授权列表/创建；指派能力校验 |
| POST /inspections/{id}/assign | expected_revision、assignee、理由 | 新版本与通知事件；发送权限/渠道另验 |
| POST /field-submissions | capture_id、inspection、坐标、精度、时间、属性、附件清单 | 原子采集记录；照片未到齐则evidence_pending |
| POST /field-submissions/{id}/attachments | 附件会话、checksum | 幂等补传，完成状态可复核 |
| POST /inspections/{id}/resolve | expected_revision、结论、证据、可选整改 | 可审计闭环；无证据不能自动确认异常原因 |
| POST /agricultural-operations | 地块/种植季、动作、时间、材料/单位、证据 | 经营操作事实，区别于巡查计划 |
| POST /quality-runs | 固定范围、规则版本、输入快照 | 202完整质检，生成不可变批次 |
| POST /reports | 类型、主体、时间窗、结果和证据版本 | 202报告草稿生成 |
| POST /reports/{id}/revisions | expected_revision、结构化修改 | 新版本；分析数字不可自由编造覆盖 |
| POST /reports/{id}/submit-review | expected_content_hash、quality_run | 审核流程；验证来源和质量仍有效 |
| POST /reports/{id}/reviews | content_hash、decision、理由 | 独立审核，版本不符拒绝 |
| POST /reports/{id}/publish | approved_revision、manifest_hash、幂等键 | 冻结publication；DOCX/PDF等实体与manifest一致 |
| POST /reports/{id}/revoke | publication_id、理由 | 新访问/共享/索引阻断，保留历史 |
| POST /shares | 资源版本、接受者范围、用途、期限 | 受控分享；不是自动公开链接 |
| DELETE /shares/{id} | 理由 | 撤回；在线代理即时拒绝，直签遵守有界TTL说明 |

## 4. 关键协议示例

以下 ID、时间、数值都是合同示例，不表示存在真实农场或测量结果。

### 4.1 提交长势变化分析

```json
{
  "subject": {"kind": "farm", "id": "farm_example"},
  "workspace_id": "ws_example",
  "analysis_kind": "ndvi_change",
  "quote_id": "quote_example",
  "input": {
    "season_id": "season_example",
    "parcels": [{"parcel_id": "parcel_example", "revision": 3}],
    "baseline_asset_version_id": "av_baseline_example",
    "current_asset_version_id": "av_current_example"
  },
  "parameters": {
    "algorithm_version": "ndvi-change/1.0.0",
    "quality_profile_version": "crop-monitoring/1.0.0",
    "poor_delta_threshold": -0.1,
    "good_delta_threshold": 0.1
  },
  "approval_id": "approval_example"
}
```

参数中的阈值是示例，正式默认值由可配置规则和农学验证决定。服务端重新计算请求 hash，验证 quote 与审批绑定相同输入/参数，不接受客户端覆盖价格或权限。若外部来源不可用，202之后的任务可以失败，但不能把空结果包装成健康结论。

### 4.2 数据不足结果

```json
{
  "job_id": "job_example",
  "execution_status": "succeeded",
  "outcome": "insufficient_data",
  "validity": "current",
  "quality": {
    "status": "insufficient_data",
    "valid_pixel_ratio": 0.23,
    "required_pixel_ratio": 0.75,
    "reasons": ["CLOUD_SHADOW_COVERAGE"],
    "quality_profile_version": "crop-monitoring/1.0.0"
  },
  "summary": null,
  "artifacts": [],
  "evidence_refs": ["evidence_quality_example"],
  "next_actions": ["SELECT_ANOTHER_OBSERVATION", "REQUEST_FIELD_INSPECTION"]
}
```

此示例的比例也是测试值。系统可以生成质量说明，但不得生成“长势正常”、病虫害诊断或伪造异常区。是否收取已经发生的质量预检成本，按用户提交前可见的报价规则结算。

### 4.3 成果清单

每个成果 manifest 至少包括 schema_version、result_id、tenant_id、created_at、input_revisions、algorithm_version、parameter_hash、quality、rights_refs 与 artifacts。每个 artifact 包括 id、kind、mime_type、size_bytes、sha256、object_version、spatial_extent、CRS、单位和可选样式。外部输出移除内部 object_key，改为需授权的 artifact_id。

manifest 本身的 hash 由固定规范计算并存在数据库；不要将自身 hash 放入被哈希正文造成递归。签名如启用，应签 manifest hash 并保存 key_id、签名算法和签署时间，不能称其为法定测绘成果认证。

## 5. 内部作业与事件合同

### 5.1 内部端点

| 方法与内部路径 | 调用者 | 关键限制 |
| --- | --- | --- |
| POST /jobs | 平台Outbox发布器 | 签名委托、operation_id、输入快照、预算授权摘要；去重 |
| GET /operations/{id}/receipt | 平台对账器 | 受控租户与operation范围；返回not_found也需要可信身份 |
| POST /operations/{id}/cancel | 平台Outbox/协调器 | 接单前持久取消tombstone，接单后转job取消；与接单争用同一tenant/operation唯一记录 |
| POST /jobs/{id}/claim | 调度器/Worker | 在短事务中生成新的lease与fencing_token |
| POST /jobs/{id}/heartbeat | 当前Worker | 必须匹配worker、attempt、fencing；过期拒绝 |
| POST /jobs/{id}/progress | 当前Worker | 有界进度事件；不写敏感外部响应 |
| POST /jobs/{id}/publish | 当前Worker | 校验manifest和实体、当前fencing、取消与来源状态 |
| POST /jobs/{id}/attempt-failure | 当前Worker/协调器 | 区分可重试与不可重试，保留成本和错误证据 |
| POST /events | 服务间Outbox | 验证生产者和租户范围；Inbox去重 |
| POST /permissions/check | 平台授权服务 | 根据当前成员/授权版本给出允许动作；失败默认拒绝 |

claim 等内部路径可以实现为 Worker 直接访问仅限作业的受控 DAO，但不能因此赋予全库管理员权限。协议测试必须覆盖租约失效和跨租户领取拒绝。

### 5.2 事件信封

```json
{
  "event_id": "evt_example",
  "event_type": "agri.analysis.completed",
  "schema_version": "1.0",
  "producer": "agri-gis",
  "tenant_id": "tenant_example",
  "workspace_id": "ws_example",
  "aggregate_type": "analysis_job",
  "aggregate_id": "job_example",
  "aggregate_revision": 7,
  "occurred_at": "2026-10-29T03:00:00Z",
  "correlation_id": "op_example",
  "causation_id": "evt_previous_example",
  "trace_id": "trace_example",
  "payload_hash": "hash_of_canonical_payload",
  "payload": {
    "operation_id": "op_example",
    "job_id": "job_example",
    "result_id": "result_example",
    "outcome": "valid_result",
    "manifest_sha256": "hash_of_manifest"
  }
}
```

示例 hash 字符串仅为说明占位，真实 Schema 要求固定长度十六进制，测试不得把此示例当生产回执。

处理者记录 producer + event_id + payload_hash。相同事件同 hash 返回已处理结果；相同 ID 不同 hash 返回冲突并告警。aggregate_revision 用于发现乱序，不能依靠消息抵达先后来覆盖新状态。消费者缺版本时重取权威资源或等待缺失事件。重试采用退避和上限，超过阈值进入可观测死信/待人工处理状态，修复后用原 event_id 重投。

### 5.3 首发事件目录

| 事件 | 生产者 → 消费者 | 结果 |
| --- | --- | --- |
| agri.farm.created / archived | GIS → 平台 | 注册/归档资源授权投影 |
| agri.parcel.revised / lineage.created | GIS → 平台/Loom索引适配 | 更新显示，标记依赖结果历史状态 |
| agri.asset.ready / revoked | GIS → 平台/知识索引 | 更新可用资产，处理撤回依赖 |
| agri.analysis.accepted / progress / completed / failed / cancelled | GIS → 平台 | 任务投影、通知和对账触发 |
| agri.usage.measured | GIS → 平台计量 | 去重用量；经价格版本进入结算 |
| agri.report.published / revoked | GIS → 平台/知识索引 | 更新发布与共享状态 |
| agri.inspection.assigned / resolved | GIS → 平台通知 | 受控通知和业务反馈 |
| platform.authorization.revoked | 平台 → GIS/Loom | 权限缓存失效、待执行步骤拦截 |
| platform.budget.released / reconciled | 平台 → GIS投影 | 更新可显示费用状态，不改写计算事实 |
| agri.telemetry.accepted / alert.opened | GIS → 平台，S4 | 设备状态及告警投影 |
| agri.prediction.completed / control.acknowledged | GIS → 平台，S5 | 预测/设备确认，不能伪装观测 |

为避免无界循环，事件消费者触发新事件必须保存 causation_id、限定允许的状态转移，不因同步投影再次发同类命令。

## 6. Agent 与工具合同

### 6.1 工具登记

工具目录至少定义 name、version、description、input_schema、output_schema、required_capabilities、side_effect_class、meter、timeout、output_limit、idempotency、approval_policy。工具 Schema 与技能版本在 run 开始时冻结；运行期间变更必须重新授权，不把工具名称相同视为能力相同。

| 工具 | 领域 API | 权限/副作用 |
| --- | --- | --- |
| agri.parcel.search | 地块查询 | agri.parcel.read；只读 |
| agri.parcel.inspect | 地块详情、观测、血缘 | 范围内读取，有输出上限 |
| agri.imagery.search | imagery/search | 元数据检索；收费来源按计量策略 |
| agri.analysis.quote | analysis-quotes | 预估，不提交计算 |
| agri.analysis.submit | analyses | 授权预算、幂等、审批策略 |
| agri.analysis.status | jobs/results | 只读当前范围 |
| agri.map.show_result | UI事件 | 仅授权layer/result ID，不接受任意URL或脚本 |
| agri.inspection.create_draft | inspections | 创建草稿；指派另有批准 |
| agri.report.generate | reports | 固定证据，异步可计量 |
| agri.report.publish | reports/publish | 明确审核及发布能力 |
| agri.knowledge.search | Loom受控检索 | tenant/workspace/权利范围内返回带来源的片段 |

任意工具回传的文字、网页、文件和外部 MCP 描述都是数据，不具有改变系统权限的权威。提示注入测试应覆盖外部资料要求读取其他租户、导出密钥、跳过审批等输入。

### 6.2 上下文与输出

农业 Agent 上下文包含：subject 类型和 ID、用户当前允许动作、所选地块 revision、种植季、时间窗、地图 bbox、可用数据质量、预算上限、语言/单位，以及有权限的知识证据引用。无权数据不进入模型上下文后再要求模型保密。

工具成功输出包含 status、request_id、operation/job/result 引用、data或有界summary、evidence_refs、quality、observed_at、warnings、next_actions。UI图层使用显示事件，证据下载用授权artifact引用。大列表只返回摘要与游标，不让模型接收整个省域地块集。

农业结论必须标明 observation、derived_analysis、prediction 或 recommendation。病害原因、产量或施肥量不能由 NDVI 差值直接确定；没有合格模型时返回可核查线索和所缺资料。

### 6.3 CLI、开放 API 与外部 MCP

首发 CLI 支持登录/选择工作区、查询农场与地块、提交分析、查任务、下载有权限报告、显示预算估价。访问令牌按租户、资源与动作限权；存储使用系统凭据机制或受控配置，不将token写入命令历史和输出。

建议命令族为 auth、context、farms、parcels、imagery、analyses、reports；默认JSON输出附schema_version，进度可轮询或SSE，非交互环境需要显式预算上限。高影响写操作必须提供有效approval或已授权策略，不能因为CLI调用就绕过UI审批。

API与MCP发布使用独立客户端注册、配额和审计；SDK从稳定OpenAPI生成并有兼容测试。公开API范围不能大于平台同一用户能力。批量下载与第三方分享遵守资源用途与权益。

## 7. 商业订阅与结算合同

### 7.1 报价与计量

报价响应必须包含 quote_id、tenant、subject、request_hash、price_version、expires_at、meters、estimated_charge_microcredits、maximum_authorized_charge_microcredits、cancellation_policy 和可能的外部成本。用户同意上限后，服务端不能以“实际更贵”自动突破。

计量维度按业务选择并版本化：分析次数、处理像元数/面积、CPU/GPU时间、存储字节时长、出站字节、模型tokens、商业数据采购。客户收费与供应商成本分开。任何“免费”只表示该客户价格为零，真实资源消耗仍记账以便控制成本。

价格记录包含计量单位、整数价格分子/分母、聚合窗口和舍入策略；先按固定窗口聚合去重计量再舍入，不能每次重试/回调分别舍入并累加。已确认的客户扣费不得超过授权上限；供应商成本异常超过客户授权时进入成本异常处理，不能未经授权转嫁客户。用量更正产生关联原记录的调整事件，不改写历史已结算计量。

### 7.2 支付与合同账单

平台需要 provider-neutral 的订单、支付回调、订阅周期、退款、到期和权益状态机。在线自助付费发布至少完成一个真实渠道的小额授权验收；没有渠道凭据时可继续用 sandbox 验证协议，但状态保持 blocked_external，不声称可收款。

B2B 合同试点在用户明确选择后允许 manual_invoice 模式：登记合同/订单，上传实际到账凭证，由具备财务能力的独立人员复核后激活权益；UI和发布清单明确此模式，不展示尚未实现的自动支付承诺。它不使在线渠道验收自动通过，未完成的自助支付继续禁用并标明阻塞。开发 Agent 不得擅自开通收费服务或进行真实支付。

回调入口先限体积并保存短期受控原始请求字节，按选定 provider 官方 SDK/协议验证签名头、时间戳、有效密钥版本与允许时间偏差，再解析和校验金额、币种、商户、订单和 test/live 环境。不能先 JSON 反序列化再重新序列化后验签；例如 [Stripe 官方签名文档](https://docs.stripe.com/events/manage-webhook-endpoints) 要求原始 body。本项目不因此默认选用 Stripe，具体渠道由经营地区和商户资质决定。

对通过验签的事件持久 Inbox 去重，按订单状态机处理重复和乱序；过期签名拒绝，provider 合法重试可能带新签名但仍以业务 event_id 去重。回调已持久接受才返回成功；崩溃重放不能重复授予权益或退款。高价值状态变化需要调用 provider 查询或对账确认，payload 中的“成功”不是唯一凭据。原始 body、签名头和个人信息不能进入普通日志；按隐私政策限制密文留存。浏览器跳转参数不触发充值。退款区分平台积分调整与渠道法币退款，执行状态和回执分别保存。

### 7.3 取消与未知结果

未开始执行的取消释放全部预留；已开始执行按报价中的取消策略结算确认消费，剩余释放。若外部供应商消费未知，则 reservation 进入 reconciliation；用户界面显示待对账及已确认金额，不显示虚假“全部退款”。

账本唯一键以 tenant、operation、meter_event/settlement_type 为基础；事件重投、刷新页面、重复工具调用和重试不能增加第二笔相同消费。对账修正写冲正或调整记录并关联原条目。

### 7.4 商业与运营API适配

以下为农业入口需要具备的平台API能力。路径除特别注明外省略/api/agri/v1；已有等价Flow接口时通过适配器复用，W00记录实际路由，不重建第二套订单或余额。

| 方法与路径 | 输入 | 输出与约束 |
| --- | --- | --- |
| GET /entitlements | 当前工作区/业务范围 | 当前套餐版本、可用能力、限额与生效时间，不泄露其他租户合同 |
| GET /usage | 时间窗、计量维度、游标 | 已确认/预留/待对账用量分列；billing能力可见金额 |
| GET /billing/subscription | 无 | 当前计划、期限、渠道、续期/到期和只读窗口 |
| POST /billing/orders | plan_version、周期、选定渠道、幂等键 | 订单、冻结金额/币种和受控支付下一步；不从客户端价格计算 |
| POST /billing/manual-receipts | order_id、到账证据asset、入账标识 | 待独立复核记录；提交者不能直接激活权益 |
| POST /billing/manual-receipts/{id}/review | expected_revision、decision、证据 | 独立billing能力复核；成功一次激活对应订单权益 |
| POST /billing/refunds | order/settlement、金额、原因、幂等键 | 退款请求及渠道状态；金额不超过可退余额，未知结果待对账 |
| GET /billing/reconciliations/{id} | 无 | 受权财务查看证据、差额和处置；普通用户仅安全状态摘要 |
| POST /billing/reconciliations/{id}/adjustments | expected_revision、证据、调整理由/金额、审批 | 新冲正/调整条目，不能直接改余额或ledger历史 |
| POST /tenant-exports | 资源范围、格式、用途、幂等键 | 202导出作业与授权manifest；敏感字段按申请人能力裁剪 |
| POST /tenant-deletion-requests | 目标租户、确认快照、保留政策、幂等键 | 受权注销流程；冻结新处理，依赖/账务/保留核对后清理 |
| POST /support-access | 工单、范围、期限、授权人 | 租户明确授权的限时支持访问；运维不能自批跨租户授权 |
| POST /api/billing/webhooks/{provider} | provider原始body与签名头 | 独立平台回调入口；按已验签商户/订单定位tenant，持久接受才返回成功 |

平台订单状态至少区分created、pending_payment、paid、expired、cancelled；退款状态另为requested、pending_provider、succeeded、failed、reconciliation，不用“订单已取消”表达“钱已退”。已付订单不能通过晚到的pending通知降级；支付和退款都以权威查询/对账及有效事件推进。用户可见账单必须说明当前渠道、确认金额与待核对金额。

## 8. 合同版本、测试与实现交付

新字段默认可选或有兼容默认值；删除、重命名、语义变化、枚举收紧和必填新增需要新主版本或迁移窗口。事件生产者在升级期支持消费者能力协商，不能新服务一部署就使旧Inbox无法解析。

每个 API 的完成条件包括：

1. OpenAPI/Schema 与实现一致，可生成受支持客户端。
2. 至少一个真实正例、一个越权负例、一个无效输入或冲突例。
3. 涉及副作用时有幂等、失败恢复及并发验证。
4. 涉及计费时有预算不足、重复回调、取消及未知结果对账。
5. 涉及影像时有真实样例、质量不足和错误CRS测试。
6. 涉及历史和发布时有输入变化/撤回使结果失效的测试。
7. 不将mock、fixture、空数据演示或静态截图作为真实业务完成证据。

数据迁移须包括 schema_version、向前迁移、备份/恢复方案、旧数据映射、隔离校验和重放安全。未经确认的旧AgriScope项目默认迁入隔离区，不能通过项目编码猜测租户，也不能把内置公开快照自动升级为客户经营边界。

完整发布验收见 [验收矩阵](ACCEPTANCE_MATRIX.md)，逐项开发任务见 [开发清单](../DEVELOPMENT_CHECKLIST.md)。

