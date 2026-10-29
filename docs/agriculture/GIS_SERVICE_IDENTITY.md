# 农业 GIS 的可信身份边界

在 `services/agri-gis/backend` 中启动独立农业 profile：

```sh
uvicorn app.agriculture.main:app --host 127.0.0.1 --port 8001 \
  --http h11 --h11-max-incomplete-event-size 262144
```

此入口**不导入旧 GIS 路由**。生产仅注册健康检查及私有、固定的
`POST /internal/agri/v1/ownership/resolve` 归属元数据接口；不注册公共签发、
委托探测、fixture 模式、几何或影像接口。目前尚无真实 `OwnershipProvider`，
凭据有效但缺少领域依赖时返回 503，不伪造可用业务。

后续领域路由必须安装同一验证依赖，并显式提供
`delegation_dependency(action, resource_extractor)`。提取器由固定路由代码实现，
接收 `Request` 和已校验请求 body，**不接收 claims**，返回处理器实际使用的完整
资源集合。缺少提取器属于配置错误；路径、非标准载荷字段和完整批次都须与签名
资源范围精确对应，不能反过来从 token 推断处理器将访问什么。

总请求头最多 256 KiB／128 项，紧凑 JWS 最多 192 KiB，解码 claims 最多
128 KiB，解码 header 最多 2 KiB。可信入口代理也须采用兼容的有界限制；
不得截断凭据或取消上限。

## 私有配置与密钥轮换

`AGRI_SERVICE_IDENTITY_CONFIG` 指向运营方挂载的 JSON 秘密文件，权限应为 0600，
不允许组或其他用户读取。模板为 `deploy/agriculture/gis-identity.template.json`；
占位符故意不可运行，不会自动生成生产密钥。Ed25519 公钥和私钥 seed 均为
32 字节，使用标准 base64 表示；私钥不进入日志或响应。
平台验签密钥与 GIS 回调签名密钥方向独立。

先在接收方安装新公钥，再切换发送方 `kid`。旧公钥只保留到仍有效的最长
30 秒票据过期，再撤销或移除。验证会检查 `revoked`、`not_before`、`not_after`；
配置变更通过部署重启／重载生效。已撤销密钥即使验签正确也不能接受，且永不
访问 token header 提供的密钥 URL。

回调地址来自可信配置，不能由 token 决定。默认要求 HTTPS；HTTP 仅允许字面量
回环 IP（`127.0.0.1`／`::1`），不允许 IPv6 zone。`localhost` HTTP 也拒绝，
避免依赖 DNS 判定回环。禁止重定向和环境代理。独立 `authority.check` 只查询
当前主体、组织、工作区成员状态及授权 epoch，不递归调用 GIS 授权。
超时或响应不合约即拒绝访问。运行时不得启用 body/header 调试追踪，避免票据、
请求数据、数据库 URL 和私钥进入访问日志。

## 持久写请求重放账本

使用 `flow_geo_migrate` 在 **flow_geo** 中执行
`20261029_agriculture_identity_replay.sql`，再通过 `AGRI_GEO_DATABASE_URL`
配置普通运行角色 DSN。迁移显式检查数据库及角色，不能误写平台库。
启动检查拒绝表所有者、可继承所有者、superuser、BYPASSRLS 或可修改账本的
运行角色。账本启用 FORCE 租户 RLS 和唯一 jti，普通运行角色仅有 SELECT/INSERT。

签名、请求绑定、当前授权、领域 revision 和完整农场集合验证全部完成后，先提交
nonce 抢占，再进入处理器。处理器后续失败也不释放 nonce；存储不可用则拒绝。
数据库墙钟及抢占后的到期复核防止连接池／锁等待使过期票据继续执行；已提交的
nonce 即使在处理器开始前票据过期，也保持已消费。所有者维护任务可在明确租户
范围内清理过期行；普通运行角色不能删除、修改或清空账本。

只有业务 POST/PUT/PATCH/DELETE 使用该写请求防重放机制。只读归属／状态查询的
服务 assertion 中 jti 仅用于关联，不是写入收据。两者均不代表业务 exactly-once；
领域 operation_id 仍须独立实现幂等。

## 验证证据的边界

Python 密码学及 ASGI 测试使用明确标识的进程内归属和授权 fixture。只有测试进程
注册 `/internal/agri/v1/delegation/check`。真实 Go→FastAPI HTTP 测试另外验证
跨语言签发、验签和反向授权回调。Geo PostgreSQL 测试要求显式提供隔离迁移及
运行 DSN；缺少时记录 `not_run`／skip，不计通过。这些测试不代表真实农场、
旧路由全部改造、完整 GIS RLS 或领域写业务验收完成。
