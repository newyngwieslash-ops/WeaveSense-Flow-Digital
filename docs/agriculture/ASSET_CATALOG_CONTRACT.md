# DT041 资产登记与权利目录合同

状态：实施合同，不是完成证明。ADR-037；GIS拥有资产事实/不可变版本/来源权利快照，Go拥有当前授权及必要操作意图，Vue仅使用同源平台API。

## 身份与来源

`farm_id` 可为空。普通business delegation的非空farm约束保持；无farm资产使用精确资产专用profile和 `resolve_asset(tenant_id,workspace_id,asset_ids)`，不伪造farm、不全局放宽 `resolve_owned`。

已有farm上的登记需要当前显式 `agri.asset.create`。无farm初建是窄治理操作，owner/tenant_admin选择明确初始grantee及受角色上限约束的能力；治理不授予内容访问或许可审批。列表/read的asset IDs均由平台从当前授权推导，空授权列表不向GIS发全目录扫描。直接asset grant继续绑定签收revision/hash，版本推进不自动拓展能力。

授权来源明确取并集：有farm资产经当前可信typedownership证明归属后，可使用该父farm当前有效的对应 `agri.asset.read/create/asset_download.read` 显式能力及角色上限；`farm.read`或owner身份不隐式授权资产。无farm资产没有父授权路径，必须匹配direct签收版本/hash；仅owner/tenant_admin能执行无farm授权治理。direct失效不能被无关farm或空farm循环变成允许；合法父cap可独立授权跨metadata版本，其撤回/过期立即失效。列表只从现有明确授权及已确认创建意图索引取得候选，再逐项验证当前事实，不扫描整个GIS目录。

## 严格客户端 metadata

`{name,kind,data_class,farm_id,source,rights_declaration,sensitivity,retention}`：

- name非空至200字符；kind为raster/vector/table/photo/audio/model；data_class为demo/operational。
- source为 `{kind,source_id,reference}`，kind区分customer_data/public_imagery/third_party；source_id只能是受审注册项或null，reference为有界opaque引用，不接受endpoint/凭据/存储key/任意URL。
- rights_declaration为 `{owner,license_ref,permitted_uses,attribution,valid_from,expiry}`；允许未知null，所有声明默认未经批准。permitted_uses采用W00六种用途；不得通过提交approved/status或商业许可布尔量自批准。
- sensitivity为public/internal/confidential/restricted，public不授予分发权限。
- retention为 `{retain_until,legal_hold}`，不得通过后继版本解除hold/缩短已确定保留期；无解除审批端点。sensitivity不允许无审核降级。
- 后继metadata版本不改变farm/kind/data_class，版本记录不可原地覆盖。同名资产允许，身份不是文件名。

## 平台 API

- `GET /api/agri/v1/asset-context`：当前工作区治理资格、可显式授予能力的现存成员、获授权父农场，以及当前允许接入的来源。父农场名称另外需要farm.read；仅asset.create时不泄漏名称。非空source_id必须有精确当前scope准入，不存在/失效统一SOURCE_NOT_AVAILABLE；null仅登记待审核声明。
- `POST /api/agri/v1/asset-previews`：workspace_id、action=create/version、metadata；create要求expected_revision=0及initial_grants，version要求asset_id及expected_revision>=1。具体预览绑定主体、作用域、输入hash、期限及版本。
- `POST /api/agri/v1/assets`：workspace_id、preview_id、expected_revision=0、metadata、initial_grants，Idempotency-Key必需。只表示登记，不表示实体ready。
- `POST /api/agri/v1/assets/:id/versions`：workspace_id、preview_id、expected_revision、metadata；追加不可变版本/CAS。
- `GET /api/agri/v1/assets`：workspace_id、有界limit/cursor、kind/data_class筛选；只列当前授权IDs，data数组加next_cursor/has_more。
- `GET /api/agri/v1/assets/:id/versions/:version`：workspace_id；历史版本也要求当前权限。
- `GET /api/agri/v1/asset-operations/:id`：workspace_id；实际持久操作引用，GET固定200，pending/未知不能伪称完成。

使用统一data/meta信封。资产输出包括asset_id、workspace_id、revision、version、name/kind/data_class/farm_id、status、metadata、权利状态/审核引用/hash、entity和created_at；metadata登记的entity为null，状态只能表明等待实体校验，不能ready。输出不含存储key或私有URL。

## 服务端用途与实体门禁

受信配置把source registry项精确映射tenant/workspace/type，公共来源也须显式授权范围，客户来源不能借用到别租户。调用W00既有 `authorize` 检查全部父源、用途、期限、撤回、商业/分发与署名，不新建更宽松许可引擎。

`AssetCatalogStore.authorize_use(tenant,workspace,asset_id,version,purpose,profile,require_entity=True)` 由固定服务操作选择purpose/profile，浏览器参数不能自批准。operational要求实际ready、operational等级、实体校验和所有来源允许；demo只能进demo流程。每次检查重载当前来源规则，历史approved快照不覆盖当前撤回/过期。

DT020对象网关在读取前后都调用资产权利guard，将实际不可变对象引用绑定精确asset version，禁止目录禁用后旧链接继续访问。无farm对象使用受审资产专用授权链；不绕过现对象网关。DT042才提供受信实体发布：本任务不得接受客户端verified/hash/key并据此变ready。真实实体fixture只验证基础规则，不冒充实际来源许可或商用验收。

私有rights_snapshot保存完整不可变审查证据；公开响应仅输出闭集来源标识、权利状态、审查opaque引用、署名及绑定完整快照的hash，不输出私有审查URL、凭据或存储key。历史版本必须同时满足当时与当前来源限制。版本推进后旧授权revision/hash不自动升级；治理者通过既有资源授权API明确重新签收当前typed资产事实，普通非空farm授权链不因此变宽。
