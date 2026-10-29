# Go 平台服务身份配置

本页说明 DT012 的实际配置入口，不代表真实农场业务、完整 GIS RLS 或生产部署已验收。协议与阶段边界见 [SERVICE_IDENTITY.md](SERVICE_IDENTITY.md)。

## 模板与秘密

模板：`deploy/agriculture/platform-identity.template.json`。其中密钥和时间都是**不可运行的占位符**，不能直接作为生产配置；不会自动生成、补齐或退回默认密钥。

由部署者在秘密管理系统内提供两套独立 Ed25519 密钥：

- 平台保存自己的签名私钥及对应公钥；GIS 只取得平台公钥。
- GIS 保存自己的签名私钥；平台只取得 GIS 公钥。平台配置拒绝 GIS 私钥及双向重复公钥。
- `public_key` 是标准 Base64 编码的 32 字节公钥；`private_seed` 是标准 Base64 编码的 32 字节私钥种子，不是任意密码或 PEM 文本。
- `kid` 使用字母起始、仅含字母/数字/下划线/连字符的不透明版本标识，最长 128 字符。
- `not_before`、`not_after` 必须是 UTC 秒级 RFC3339，例如格式 `YYYY-MM-DDTHH:MM:SSZ`；结束时间必须晚于开始时间。票据不得超过密钥或当前授权有效期。
- 不将真实配置提交到 Git、聊天、日志或 CI 产物。配置通过只读秘密挂载提供，并限制操作系统文件读取权限。

平台进程配置：

```sh
FLOW_AGRICULTURE_ENABLED=true
FLOW_AGRICULTURE_SERVICE_IDENTITY_CONFIG=/run/secrets/agriculture-platform-identity.json
```

上面路径仅为部署示例，文件必须由部署者明确提供。生产仍需要真实 PostgreSQL 角色、迁移及准入配置；不得为此启用 SQLite 开发豁免。

农业功能启用但未设置身份配置时，生产 OwnershipResolver 保持缺失，内容授权返回依赖不可用，不用 fixture 填补。配置路径存在但配置无效时，平台启动失败；错误不输出秘密内容或文件路径。

## 传输与内部路由

`gis_base_url` 是固定服务源站，不允许携带用户信息、路径前缀、查询或 fragment。生产非本机 peer 必须使用 **HTTPS 且证书受平台信任、主机名匹配**；实现不会关闭 TLS 校验。专用内部 CA 应通过部署系统的受信 CA 配置提供，而不是关闭验证。

私网服务仍需通过现有 `CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS` 精确允许主机；这项白名单不能把普通私网 HTTP 变为安全连接。

本地开发的唯一明文例外是字面 loopback 地址，例如 `http://127.0.0.1:8080` 或 `http://[::1]:8080`，且仍须相应精确白名单。`http://localhost` 等 DNS 名称即使在白名单也不接受，不依赖 DNS 名称证明本机身份。该例外不能用于容器之间或局域网服务器的明文生产调用。

实际入口：

- 平台向 GIS：`POST /internal/agri/v1/ownership/resolve`，专用 `ownership.resolve` 服务能力。
- GIS 向平台：`POST /internal/agri/v1/authority/check`，专用 `authority.check` 服务能力；平台在根路由注册，不是公共 `/api` 会话接口。
- 业务委派票据由平台内部重新授权后传给 GIS，不提供公共 mint API，也不返回浏览器或模型。

出站请求使用受控平台传输，固定源站、有限超时、禁止重定向。当前权限回调实时读取数据库与 epoch，不调用 GIS，不缓存“仍有权限”的结果。未配置真实 GIS OwnershipProvider 时，不能以成功健康检查代替内容授权验收。

## 轮换与撤销

配置可同时保留仍有效的旧公钥和新公钥，以 `platform_signing_kid` 指定平台当前签名私钥。先在双方验证配置部署新公钥，再切换签名版本；保持必要的短票据重叠窗口。不同方向分别轮换，不复用私钥。

配置在进程装配时读取，**没有文件热加载承诺**。修改 `revoked`、有效期或密钥列表后，须通过既有受控部署流程重新装配对应进程并验证；不能只修改磁盘文件便声称正在运行的进程已撤钥。撤销后的 key 不再签名或验证。业务权限撤销另外由实时主体/epoch 回调控制，不能仅等待票据过期。

## 验证命令与边界

```sh
cd backend
go test ./internal/agriculture -run 'Test(Delegation|ServiceKey|ServiceAssertion|InternalAuthority|GISClient|ServiceIdentity|IdentityRejects)' -count=1
```

测试文件中的固定种子仅为明确的测试 fixture，不是示例生产秘密。跨语言 HTTP 与 PostgreSQL nonce 验证需要各自真实测试依赖；未运行时必须保留跳过/未验收状态。此处不承诺部署、设备控制、费用结算或全部 144 项任务完成。
