# 最终 CI 交接结果：存在两处真实失败，未验收升级

2026-11-02 06:03:38 UTC，本轮六个工作流中五个成功，identity 工作流失败。源码基线为 `7c6e0229e3e708ab540f338a7e01cb0f24d25a12`，独立 CI scaffold 提交为 `52d6e3f43899a1f6412f5e4c5be0e5bc428ae1ec`。二者不是同一 Git 树；组装来源及逐文件哈希见 `ci-evidence/session-ci-provenance.json`，发布映射见最终发布回执。

- [失败运行 41396200447](https://github.com/newyngwieslash-ops/WeaveSense-Flow-Digital/actions/runs/41396200447)
- [失败作业 125117951099](https://github.com/newyngwieslash-ops/WeaveSense-Flow-Digital/actions/runs/41396200447/job/125117951099)
- 原始脱敏 artifact：`13119669079`，ZIP 59,454 字节，SHA256 `0222ae055217e6285974a6d97e2483c7238488f61dcd9d33cb9d01d7dc13aec9`。解包的38个原文件保持不变，位于 `ci-evidence/`；逐文件清单见 `CI-HANDOFF-RESULT.json`。

## 确认失败及未知范围

1. **身份资源绑定负例失败**：`ci-evidence/identity-python.json` 中75项74通过、1失败。失败测试为 `test_agriculture_identity::test_signed_actual_path_cannot_authorize_a_different_resource`，定位 `services/agri-gis/backend/tests/test_agriculture_identity.py:494`。该行要求 `response.json() == {"code": "request_scope_mismatch"}`；前一行要求HTTP403。脱敏报告不含实际响应体，因此不能推断实际返回码内容、根因或已发生越权。需要本地私有诊断复现，禁止削弱权限负例来使CI变绿。
2. **资产浏览器链数据库回执检查点失败**：`ci-evidence/identity-assets.json` 的 `TestAssetBrowserPostgresChain` 失败，阶段 `verify_created`，`backend/internal/agriculture/asset_browser_chain_test.go:254` 为 `asset platform receipt checkpoint differs`。源码在该阶段要求 `flow_agriculture_asset_operations`、`flow_agriculture_asset_events` 对租户/工作区的计数均为1；SQL错误或计数不等于1都会进入该失败分支。脱敏报告没有哪张表、实际计数或SQL错误，**根因尚未确定**。

资产完整浏览器结果未产生，23个浏览器检查标记 `not_run`；这不等于23个交互各自失败，也不能算它们通过。报告中的 artifact_verification_passed=false 不足以单独认定产物遭篡改。需要区分产品缺陷、事件数量契约与测试断言问题后修复。

终端只打印 `Real asset HTTP/browser acceptance failed`，因为脚本先检查资产gate；它并不覆盖较早已记录的身份Python失败。所有JSON测试结果已逐项扫描：两个失败条目、23个资产浏览器not_run，无额外显式skip；XML中的required_suite_gate是汇总失败，不是第三个独立根因。

## 已通过但不能外推的范围

- 地块：`identity-parcels.json` passed=true，9个Go测试和21个浏览器检查通过；`identity-parcel-python.json` 52项通过。因此不能说DT-026未跑浏览器或整个地块链失败。
- 资产HTTP/下载：资产报告18个Go测试中17项通过，包括HTTP与下载链；资产Python109项通过。不能把整个资产实现说成没有工作。
- fresh GIS迁移、农场、隔离等本轮报告亦保留。五个其他成功工作流的名称、运行与作业见机器JSON。
- 数据等级是fixture、production_acceptance=false：实际数据库/HTTP/浏览器执行不等于真实农业业务或生产验收。

**正式台账仍为25 done、DT-026/DT-041 in_progress、117 not_started；119项未闭合。** 本次未修改正式状态、没有修复或重跑。地块局部通过应保留并供复核，但不得在缺少独立完整验收闭合时自动升级done。

## 本地下一步与复现命令

先读 `LOCAL-SETUP-AND-RESUME.md`、本文件、`.github/workflows/agriculture-identity.yml` 和 `scripts/agriculture/identity-postgres-ci.sh`。准备该工作流规定的锁定Python环境、Go、Node/Bun、两个真实农业前端构建、Docker/PostGIS/Redis、Chromium、nginx和mime.types。以下命令是复现入口，不是宣称已在用户本地运行：

```bash
# 在完整产品仓库根目录；仅对一次性fixture基础设施执行。
export AGRI_IDENTITY_PYTHON="$PWD/.local/identity-rebuild/venv/bin/python"
export CHROME_BIN="$(command -v google-chrome || command -v chromium || command -v chromium-browser)"
# NGINX_BIN、NGINX_MIME_TYPES先按工作流配置为已核验的本地文件。
AGRI_GEO_MIGRATION_ONLY=1 AGRI_IDENTITY_CHAIN_REQUIRED=0 bash scripts/agriculture/identity-postgres-ci.sh
AGRI_GEO_MIGRATION_ONLY=0 AGRI_IDENTITY_CHAIN_REQUIRED=1 bash scripts/agriculture/identity-postgres-ci.sh

# 身份负例可先局部定位（前提：相同锁定Python依赖已安装）。
(cd services/agri-gis/backend && "$AGRI_IDENTITY_PYTHON" -m pytest \
  tests/test_agriculture_identity.py::test_signed_actual_path_cannot_authorize_a_different_resource -q)
```

资产目标Go命令是 `go -C backend test -race ./internal/agriculture -run '^TestAsset(CatalogHTTP|CatalogDownload|Browser)PostgresChain$' -count=1 -timeout 15m -json`；它必须在脚本所创建的一次性六角色数据库、Redis、前端与浏览器环境中运行，单独缺环境会skip或fail，不能作为成功证据。

本地补充私有、无秘密泄漏的诊断：记录身份负例的实际错误码；将资产检查点错误区分为SQL错误、表名、期望/实际计数，并核对事件生命周期。保留原失败证据，修复后冻结新源码，重跑必要真实链和防回归测试，再按原依赖及证据规则验收026/041。不要只改expected、不放宽鉴权、不用mock替换真实依赖，也不要无条件从零重写已通过地块链。
