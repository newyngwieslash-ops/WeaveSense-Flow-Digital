# 原生 PostgreSQL 平台成员与授权验证

此入口专门验证恢复后当前平台源码与当前农业 PostgreSQL 初始化脚本；GIS 原生入口仅做下述共享环境隔离修正，不替代浏览器、Docker、Loom 或真实农场验收。

## 父进程环境隔离修正

恢复审查发现仅设置 `PGHOST=127.0.0.1` 不足以抵消继承的 `PGHOSTADDR`；[PostgreSQL 17 官方连接说明](https://www.postgresql.org/docs/17/libpq-connect.html#LIBPQ-PARAMKEYWORDS) 明确同时存在时网络地址由 hostaddr 决定。两个原生入口现在仅共享 `native_test_environment.py`：只继承 PATH、LANG、LANGUAGE、LC_ALL、LC_CTYPE、TZ，HOME 指向本次新运行目录。父进程 PG*、DATABASE_URL、CANVAS_*、FLOW_*、AGRI_TEST_*、模型密钥、代理与动态库注入配置均不继承；入口自行填入本次短期 fixture 连接参数。平台 Go 使用准备好的缓存，设置 GOTOOLCHAIN=local、GOPROXY=off。

该修正仅涉及测试环境构造，GIS 测试/业务代码不变。验证时故意给父进程设置 `PGHOSTADDR=127.0.0.2 PGSERVICE=unusedfixture`（均非生产配置），两个入口仍必须通过真正的 `127.0.0.1` 新鲜数据库完成测试。旧报告保留，新环境隔离报告单独归档，不追溯重写旧成功结果。

先按 `NATIVE_POSTGRES_TESTING.md` 在仓库 `.local` 内准备已验证的官方 PostgreSQL/PostGIS 包（不使用 sudo、系统安装或系统服务），并准备项目要求的 Go 工具链。然后在仓库根目录执行：

```bash
python scripts/agriculture/native-membership-test.py \
  --package-root .local/native-postgres/package-root-reviewed \
  --go .local/toolchains/go/bin/go
```

入口核对固定包清单与准备回执中的全部文件/链接摘要，拒绝 root 与仓库 `.local` 外的输出路径；新建 UUID 专属集群、随机短期 fixture 密码、SCRAM 认证及仅 literal loopback 随机端口。执行真实 `deploy/agriculture/postgres/init.sh`，使用不同的 `flow_platform_migrate` 与 `flow_platform_app` 连接，绝不把 app 设置为 owner/superuser/BYPASSRLS。

真实检查包括六个数据库角色权限与跨数据库连接拒绝、runtime 非 owner/不能继承迁移角色、admission 角色不能被迁移角色自动继承、PostGIS 函数可用。然后实际执行以下 Go 入口：

```bash
# 仅迁移子进程得到 migration DSN，密码不打印、不入报告。
go -C backend run ./cmd/migrate-schema up  # 执行两次
go -C backend run ./cmd/migrate-schema verify
go -C backend test -p 1 -count=1 -json ./internal/database ./internal/agriculture -run '^TestAgriculturePostgres'
go -C backend test -p 1 -count=1 -json ./internal/flow -run '^Test(OrgInviteSingleUseAndGrantRevocation|P7MembersRoleEditing|AgricultureMembership)'
```

校验迁移 82/83 存在，重放前后 migration journal 摘要不变。测试结果经过既有 `membership-ci-result.py` 严格白名单归约器：五个必需 PostgreSQL 主测试必须实际通过，skip 不可充当 pass。保留 reciprocal_grants 子测试和所选 legacy 测试。原始输出只在忽略的私有运行目录，公开报告只保留安全状态、测试名称、版本、命令、源码摘要及包来源。启动尝试后无论测试成败都关闭本次集群，删除 bootstrap 密码文件；不清理或操作其他集群。

报告路径由入口打印，位于 `.local/native-membership/run_<uuid>/report.json`。源码工作树摘要与实际测试一起记录；若后续修改源码，不得把该报告误当成新代码结果。报告声明 `remote_ci=false`、`browser_test=false`，此次是新执行，不是历史 GitHub CI 证据重写。
