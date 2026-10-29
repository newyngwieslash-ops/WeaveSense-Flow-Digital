# 无 Docker 的本地 PostgreSQL/PostGIS 隔离测试

本指南仅用于开发验证，不是生产安装或部署。适用于非 root 的 Debian 13 amd64 环境；需要现有 Python 3、`dpkg-deb`、运行库（ICU 76、OpenSSL、GEOS、PROJ、GDAL 等）和项目按哈希锁定的 `.local/agri-identity-venv`。缺依赖时应报告缺项，不修改系统权限或自动升级系统包。

## 准备与运行

在仓库根目录执行：

```bash
python scripts/agriculture/native-postgres-prepare.py \
  --destination .local/native-postgres/packages-v1
python scripts/agriculture/native-postgres-test.py \
  --package-root .local/native-postgres/packages-v1
```

准备脚本从固定的 Debian 官方地址下载 `deploy/agriculture/native-postgres-packages.json` 指定的 8 个包，逐个比对 SHA256，再用 `dpkg-deb -x` 解包到新的 `.local` 子目录。目标已存在即拒绝，不覆盖；归档路径、链接和特殊文件先检查。不会运行包的 `postinst`、`apt install`、`sudo`、系统服务管理或系统 alternatives。PostGIS control 文件软链接仅建立在解包目录。可用 `--cache` 指向仓库 `.local` 内已有缓存，但仍逐包校验。

固定版本为 PostgreSQL **17.11**、PostGIS **3.5.2**。来源追溯：Debian trixie `InRelease` 使用系统 Debian archive keyring 验签成功，再按已签名清单核验 `Packages.xz`，最后按包记录验证 `.deb`。发布清单保留元数据和包 SHA256；不静默选择更新版本。离线本地 `prepared.json` 是解包完整性收据，不是抵抗本地恶意篡改的独立信任根。Debian 的签名验证链见 [SecureApt](https://wiki.debian.org/SecureApt)。

测试脚本检查解包收据，每次创建独立 UUID 集群，随机生成短期 fixture 密码，使用 SCRAM 和 **127.0.0.1 随机端口**。禁用 Unix socket 是执行环境限制下的显式配置，不开放公网监听。`initdb` 与进程启动遵循 [PostgreSQL initdb 文档](https://www.postgresql.org/docs/17/app-initdb.html)及[服务启动文档](https://www.postgresql.org/docs/17/server-start.html)。

新集群执行仓库真实 `deploy/agriculture/postgres/init.sh`：平台、知识、GIS 三个数据库，各自独立迁移/运行角色，以及受限 admission/actor guard 角色。随后使用真实迁移角色和非所有者运行角色执行测试；不将运行角色升级成 owner 或 BYPASSRLS。测试结束或失败均尝试停止本次集群，并删除 bootstrap 明文密码文件；不停止其他数据库，不删除其他工作目录。

## 结果与验收边界

结果保存于 `.local/native-postgres/runs/run_<UUID>/report.json`：包含真实测试状态、六项必跑 PostgreSQL 测试名称、退出码、数据库/解释器版本、包 SHA256 和实际工作区源码哈希。六项必跑测试缺失、失败或跳过都会使脚本失败；数据库退出也必须成功。测试日志和数据库文件留在 Git 忽略目录中，不发布 DSN、短期密码、数据库文件或原始异常日志。

该套件覆盖签名验证与持久 nonce、绑定/RLS/受限函数权限、并发撤销及过期、真实 Workbench 属性写入及内部提交、审计失败回滚和历史保留。Workbench 稳定地块映射与适配器是测试进程显式 fixture；生产没有打开旧路由或部署 fixture 映射。真实 PostgreSQL 通过不等于真实农场、全量旧 GIS 表 RLS、生产可用或商业验收通过。

本地结果与 GitHub CI 独立记录。无论本地通过多少项，未运行/待审批的远程 CI 不能记作成功。恢复后的实际工作区字节用新的哈希清单追溯，不声称等同于已丢失的旧提交。
