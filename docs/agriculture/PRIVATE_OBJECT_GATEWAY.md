# 私有对象网关基础（DT020）

## 交付边界

本模块提供受控对象版本的私有字节读取，不是第二套资产、农场、报告发布或上传系统。原有 DatasetAssetService 的裸存储 URI 和 operator_code 路径不在农业公网入口开放。对象清单只是已批准字节与权威资源版本的引用；当前农业资源权属 provider 未配置时，内容请求返回 503，不使用清单或票据伪造权属。

缩略图、tile、报告、媒体和原件共用此访问边界。`category=tile` 不表示已实现瓦片生成、样式渲染或大体积 COG 引擎。当前本地 provider 每个对象最多 64 MiB；大文件/S3 provider、上传登记、正式报告发布与真实商业数据验收分别由后续工作包完成。

## 固定内部协议

浏览器只使用平台同源 `GET/HEAD /api/agri/v1/workspaces/{workspace_id}/objects/{object_id}/versions/{version}`。
平台先用真实会话查询当前工作区成员关系，再从该关系推导租户；请求不能附加查询参数、
任意存储 key、外部 URL、租户或服务身份。`HEAD` 也执行相同授权，不作为存在性捷径。

- 服务元数据：`POST /internal/agri/v1/objects/resolve`，独立 `object.resolve` 服务断言，最长 30 秒。
- 请求体严格为 `{tenant_id, workspace_id, object_id, version}`，tenant/workspace 必须与已验证服务断言一致。
- 响应严格为 `{tenant_id, workspace_id, object_id, version, resource:{kind,id}, resource_revision}`；不返回 key、文件名、MIME、字节、路径或 URL。它不授予用户下载权限。
- 内容：`GET/HEAD /internal/agri/v1/tenants/{tenant}/workspaces/{workspace}/assets/objects/{object}/versions/{version}`，固定 `agri.asset_download.read`。
- 报告字节对应路径中 `reports`，固定 `agri.report.read`；这不替代报告生命周期和已发布投影校验。
- 平台根据可信会话派生 tenant/workspace，解析最小元数据后执行现有 Authorize，再签发内部请求。服务票据不交给浏览器，不提供公开 mint 或元数据代理。
- `object_id` 是字母起始的不透明 ID；`version` 是 1 至 9007199254740991 的整数，路径仅接受无前导零十进制，JSON 使用整数。

两个内容路由分别绑定固定动作，不从未验证 claims 或浏览器字段选动作。完整签名绑定 HTTP 方法及原始路径，查询字符串、编码别名及带正文的 GET/HEAD 均拒绝。签名验证之后、任何数据库元数据读取之前，固定 scope guard 先比较路径 tenant/workspace 与已验证票据；错配一律 403，不因其他租户对象是否存在而改变响应。路由 extractor 再从实际路径和双 scope 数据库清单独立找资源，与票据完整资源范围比对。清单引用版本必须等于独立权威 provider 的当前版本，farm 集合和当前平台权限 epoch 同样复核。

## 数据库与对象 key

正式 owner CLI 有序执行 `20261030_agriculture_private_objects.sql`。表 `agriculture_private_objects` 使用 `(tenant_id, workspace_id, object_id, version)` 主键，key 全局唯一；FORCE RLS、精确单 tenant/workspace restrictive policy、app 仅 SELECT、worker 无 CRUD。普通 runtime 不创建表或登记对象。

canonical key：`tenants/{tenant}/workspaces/{workspace}/objects/{object}/v{version}`。用户文件名、任意 URI 或任意相对路径不能参与 key。清单保存明确资源 kind/id/revision、用途类别、SHA-256、字节数、允许的 MIME、demo/operational、分发授权及状态。

同版本的内容、归属和所有元数据不可原位更改；数据库触发器仅允许 `active → revoked`，禁止恢复、改版本和 DELETE。权利撤回用 revoked，不删除历史证据。新内容必须新版本；物理垃圾收集及 legal hold 处理尚未开放，不能从此基础推断已实现完整生命周期。owner 是受控迁移/登记边界，不声称最高数据库管理员无法改变数据库。

## 本地存储与配置

`AGRI_PRIVATE_OBJECT_ROOT` 是管理员明确配置的真实绝对目录，配合既有 `AGRI_GEO_DATABASE_URL` 与私有服务身份配置。未配置存储/provider 时拒绝内容。部署应把该根挂载为只读、非公开目录；此任务不创建生产数据或存储凭据。

读取从根文件描述符出发，对根的每一级祖先和 key 的每一级路径使用 `O_NOFOLLOW`，最终只接受普通文件。禁止符号链接、路径穿越、URL、编码 key、版本置换；检查实际字节数和完整 SHA-256 后，返回已经验证的字节，而非重新打开一个可能变化的文件路径。该实现不发起外网请求，不是通用下载器或 SSRF 适配器。

## 撤回、Range 与缓存

每次请求重新验证身份、当前对象状态、独立资源权属及平台 epoch。磁盘读取完成后再次复核清单、权属及权限，再发出任何内容。没有可复用的存储直签 URL，也没有浏览器持有的内部 token。

所有对象响应（含错误、HEAD、206）均含 `Cache-Control: no-store`（GIS 字节响应另带 `private`）和 `X-Content-Type-Options: nosniff`，不返回 ETag/304，不进入公共 CDN。平台统一会话中间件可能将 `private, no-store` 规范为 `no-store`，两者都禁止缓存，而不是允许公共缓存。Range 仅允许单个合法 `bytes=` 区间，不能扩大完整对象授权；多段范围拒绝。平台额外设置 `Vary: Cookie` 和 `Content-Security-Policy: default-src 'none'; sandbox`，拒绝 `If-Range`。只有精确白名单 PNG/JPEG/WebP/AVIF 可内联；其余 MIME（包括 SVG/HTML/PDF）一律 attachment/octet-stream，不透传上游 Cookie、跳转、缓存头或凭据。

平台只接受声明且一致的有界 Content-Length，校验 206 范围与请求相符后才转发。
进程级两个缓冲槽从读取直到浏览器响应完成始终占用，避免慢客户端累计完整对象缓冲。
这不是大型影像流式引擎或吞吐性能验收，GIS 部署仍需配置其服务并发和内存额度。

撤回后新访问拒绝；检查之后已经发出的字节和客户端已保存副本无法追回。本实现不声称立即取消正在网络发送的响应，也不把平台退出登录等同于抹除已经下载的数据。内部短票据仍有最长 30 秒限制，但新请求同时受当前权属和 epoch 校验。

## 验证

组件测试使用实际临时文件、真实签名和 ASGI 请求；真实 PG 测试另外启动 uvicorn 的独立 loopback TCP listener，通过 httpx 读取真实文件，并使用独立 fixture 权属表和普通 app 数据库角色。fixture 表不是生产权属 provider，demo 数据不是客户验收。

```sh
python scripts/agriculture/native-postgres-test.py \
  --package-root .local/native-postgres-current/root \
  --python .local/identity-rebuild/venv/bin/python --phase objects \
  --go .local/toolchains/go/bin/go
```

必需 PG 用例：`test_postgres_private_objects_scope_immutability`、`test_postgres_private_object_http_bytes_and_withdrawal`、`test_real_go_python_object_gateway_chain`。最后一项执行真实平台会话入口、Go 授权与签发、Python 验签与 PG 清单/独立权属及实际文件，不用固定 actor 请求头替代会话；平台测试使用 SQLite，不能把它记为平台 PG 证明。验证真实相同 object ID 在另一 tenant/workspace 的独立行、无 scope/连接复用、runtime 写入拒绝、元数据不可变、权属/对象撤回、精确字节和 Range。实际结果与冻结源码哈希另存证据，不以本说明代替执行结果。
