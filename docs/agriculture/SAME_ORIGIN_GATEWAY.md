# DT014：React / Vue 同源网关

农业专用入口使用 `deploy/agriculture/Dockerfile.web`、
`deploy/agriculture/nginx.conf.template` 与 Compose `web` profile。
原影视 `Dockerfile` 和 `nginx.conf` 不修改。这里提供部署构件，**不是已经完成
生产部署、TLS、商用发布或 GIS 领域验收的声明**。

## 路由与信任边界

| 路径 | 行为 |
| --- | --- |
| `/`、React 深链接 | React 农业构建；HTML `no-store` |
| `/agri` | 308 到 `/agri/`，保留查询参数 |
| `/agri/` 与 Vue 深链接 | Vue 农业构建；未知业务路径由应用显示未找到，不造地图数据 |
| 两套 `/assets/` 内容哈希文件 | 实体存在才200；immutable一年缓存 |
| 缺失JS/CSS/图片等文件 | 404且不缓存；不会回退SPA HTML |
| `/api/*` | 仅转发 Go platform；no-store；保留真正错误状态，不拦截API错误成HTML200 |
| `/internal`、`/api/internal` 及子路径 | 404，绝不转发 |
| `/api/v1`（原GIS）及子路径、`/agri/api`及子路径 | 404；不绕过Go向GIS直连 |

`/api/agriculture/gis/*` 没有凭空增加领域处理器，按真实 Go 路由返回未找到或
未配置错误；不得将可打开农业页面等同于已具备农场或遥感数据。

网关不保存内部服务密钥。DT012签名服务上下文只由Go生成并通过私有链路访问GIS。
访问日志仅包含方法、规范化路径、状态及长度，不记录查询参数、Cookie、Authorization
或Referer。Nginx请求错误日志可能包含原始URL，因此本配置将其丢弃到/dev/null，
用访问状态日志和外部可用性检查诊断；不得在URL中传递任何秘密。
请求中的转发链被重写；本配置不信任浏览器自报X-Forwarded-For/Proto。
未配置公网TLS，所以不自行绑定公网端口、不声称HTTPS上线完成。

## 确定性构建与本地启动

镜像固定版本标签 Bun1.3.9、Node24.22.1、Nginx1.27；使用各自原锁文件。
版本标签并非镜像digest，完整供应链/生产发布仍需后续门禁。
React实际执行 `build:agriculture`（含模块/许可检查），Vue执行独立农业构建。
专用Dockerignore禁止复制node_modules、旧dist、环境文件和密钥文件。
产物分别位于webroot及webroot/agri，不包含旧AgriScope身份切换器。

1. 检查已有配置。首次可复制 `deploy/agriculture/env.example` 为本地私有文件，
   例如 `.env.agriculture`；存在时不覆盖。
2. 用获准的本地密码管理方式填写每项独立、至少32字符URL-safe随机秘密，权限0600。
   模板值为空，不能启动；不打印 `docker compose config` 完整输出。
3. 在具有Docker/Compose的本地环境执行：

```sh
docker compose --env-file .env.agriculture -f docker-compose.agriculture.yml \
  --profile core --profile platform --profile web config --quiet
docker compose --env-file .env.agriculture -f docker-compose.agriculture.yml \
  --profile core --profile platform --profile web up -d --build
```

浏览器入口默认 `http://127.0.0.1:8091`。8090仍只是依赖探针，不是业务网关。
平台关闭公开注册；应使用已有获准账户初始化流程，不能为了演示开放注册或降级身份。
GIS、数据库、worker不发布公网端口。网关非root、只读根文件系统、cap_drop ALL，
仅/tmp临时可写。
web healthcheck只证明静态入口可读取，不证明平台、GIS或数据库业务就绪；
`depends_on: service_started`也不是应用健康验收。

开发也可通过同一个网关服务两套真实构建产物，重建后刷新；不把两个独立Vite端口
描述为同源。热更新聚合不是本配置提供的能力。生产TLS、反向代理可信边界、域名、
账户及真实对象提供方必须另行配置和验收。

## 验证层次

原DT009浏览器测试使用Node临时代理，不等于Nginx验证。DT014新增测试必须启动
真实Nginx并服务两套真实构建产物；测试配置只能替换监听地址、静态根、Go上游和
本机运行文件路径，不得替换路由/认证处理为mock。

本执行器没有Docker；原生Nginx HTTP检查不能作为Docker镜像构建或Compose启动
通过证据。浏览器若因执行器AF_UNIX限制不能启动，明确记录阻塞，不禁用sandbox
冒充浏览器验收。实际命令、结果与证据由测试报告单独记录。

## 待执行的普通 CI

`.github/workflows/agriculture-gateway.yml` 仅 `contents: read`，复用固定源码
准备工具的逐文件允许列表，实际编译Go、构建React/Vue；临时runner解包官方
发行版nginx而不启动系统daemon，运行真实Chromium网关测试。
另一步仅构建临时本地Docker镜像并启动loopback容器，验证静态产物、内部路径404、
上游故意不可用502，然后删除本次创建的容器及镜像。这不是容器认证端到端验收。
不推镜像、不发布、不部署，不读取仓库secret；仅上传四个显式脱敏JSON证据文件。
发行版Nginx与镜像版本分别记录，不冒称一致。

当前工作流仅本地编写和静态检查，实际CI结果待执行；文件存在不等于通过。
浏览器与Docker报告分开记录，任何失败不能合并宣称任务完成。
