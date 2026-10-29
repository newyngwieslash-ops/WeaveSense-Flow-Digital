# 农业 W00 开发基线（不是生产部署）

本目录只建立可重复的基础设施和依赖探针。现有 Flow/Loom 的空 scope 系统行为仍待 W01 收紧和 PostgreSQL 回归，**不得接入真实客户数据、支付、设备或对外发布**。GIS 只开放依赖检查；原 AgriScope 的可自报身份路由没有挂载。G0 和 DT-005 的干净卷验收只有实际执行成功后才能通过。

## 前置与固定来源

- Linux 容器环境、Docker Engine 与 Compose v2；本次云环境没有 Docker，以下启动尚未执行。
- Go 平台沿用 `backend/Dockerfile`（Go 1.25，Node/Pi 22）；Loom 沿用其锁文件与镜像构建；GIS 专业代码保留在 `services/agri-gis`。
- 最小依赖：PostGIS `17-3.5`、Redis `7.4.2-alpine`、探针 Python `3.12.10` / asyncpg `0.30.0`。版本标签尚不等于镜像 digest 锁定；生产供应链固定和完整 SBOM 属后续门禁。
- 不复用 `docker-compose.flow.yml` 的数据库卷、`.env.flow` 或现有用户数据。项目名始终 `weavesense-digitaltwin-dev`。

## 配置与检查

首次在新工作区复制 `.env.agriculture.example` 为 `.env.agriculture`；已有文件先检查，不能覆盖。由获准的本地操作者配置每项互不相同、至少 32 位 URL-safe 随机秘密，保留文件权限 `0600`。模板不含可使用的默认秘密。不要打印完整 `docker compose config`、数据库 URL 或环境文件。

```sh
python3 scripts/agriculture/doctor.py
docker compose --env-file .env.agriculture -f docker-compose.agriculture.yml --profile core config --quiet
docker compose --env-file .env.agriculture -f docker-compose.agriculture.yml --profile core up -d --build --wait
curl --fail http://127.0.0.1:8090/health/ready
```

探针查询真实 `flow_geo` 的 PostGIS 及当前角色，不是静态假健康；只有它绑定本机 loopback。`/health/live` 仅存活。其他路径返回 `503 AGRICULTURE_BUSINESS_API_NOT_READY`，不能作为 GIS 功能通过。

数据库首次初始化建立 `flow_platform`、`flow_intel`、`flow_geo`。每库独立 `*_migrate` 迁移 owner 与 `*_app` 运行账号，运行账号不是 owner，不具备 superuser/BYPASSRLS/建库/建角色权限，不能连接其他域库。默认权限将新表 DML 授予对应运行账号，不授予 DDL。首次脚本不会修改已有卷；初始化失败须人工诊断，不能通过删除卷掩盖失败。

## 可选真实上游启动回归

```sh
docker compose --env-file .env.agriculture -f docker-compose.agriculture.yml --profile core --profile platform --profile knowledge config --quiet
docker compose --env-file .env.agriculture -f docker-compose.agriculture.yml --profile core --profile platform --profile knowledge up -d --build --wait
```

`platform-migrate` 执行原 Go 迁移，`loom-migrate` 执行原 Loom 迁移；运行服务关闭自动迁移，使用各自运行账号。它们不向宿主发布端口，无真实 provider 配置，无外部 egress；这一步只验证上游隔离运行兼容性，不自动启用知识采集或农业 API。现有前端生产包可能含单独授权组件，因此本基线不直接发布原始 web 镜像；农业前端 profile 完成许可排除后再接入。

使用容器内请求检查平台 `/api/health/ready` 和 Loom 的实际健康入口；不要把未启动的功能写成成功。干净卷测试应在独立 CI 项目名执行，输出迁移结果、角色权限、健康响应和退出码，保留失败日志但清除敏感值。测试数据使用 `AGRI_DATA_CLASS=demo` 和 `demo/agriculture/` 对象前缀；当前基线没有假装提供对象存储上传功能。

## 停止与恢复

```sh
docker compose --env-file .env.agriculture -f docker-compose.agriculture.yml --profile core --profile platform --profile knowledge stop
```

需要移除容器时可使用相同参数 `down`，**禁止自动使用 `down -v`**。启动相同命名卷时首次初始化不会重跑；变更数据库秘密必须走明确轮换方案，修改 `.env` 不会改变已有 PostgreSQL 账号。

本地静态/单元检查：`python3 scripts/agriculture/doctor_test.py`、`sh -n deploy/agriculture/postgres/init.sh`。这些检查不代替真实 PostGIS、角色隔离、迁移和容器启动验收。
