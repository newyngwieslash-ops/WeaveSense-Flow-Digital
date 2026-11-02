# WeaveSense DigitalTwin 本地拉取与恢复指南

> **后续CI终态（2026-11-02）**：先读 [CI-HANDOFF-RESULT.md](CI-HANDOFF-RESULT.md)。五个工作流成功，identity有两处真实失败：身份资源绑定负例返回体断言、资产创建平台回执检查点。地块实际链通过；资产浏览器完整结果缺失，23检查not_run。正式25项done不变。请先定位两处失败并保留原证据，再推进依赖任务。

目标是把已有完整产品安全拉到本地，然后交给 Zcode/Codex 持续开发全部剩余任务。**当前产品尚未完整实现，也没有已验收的完整生产一键启动流程。** 本指南区分“完整代码已取到”“基础设施可运行”“真实集成通过”和“商用可发布”。

## 1 先认对仓库和分支

| 项目 | 本次交接的含义 |
| --- | --- |
| 目标仓库 | `https://github.com/newyngwieslash-ops/WeaveSense-Flow-Digital` |
| 计划完整产品分支 | `feat/agriculture-v1`，必须实际核验，不能只看名称 |
| 本文审查的完整实现源码 | `7c6e0229e3e708ab540f338a7e01cb0f24d25a12` |
| 缩减 CI 分支 | `ci/agriculture-w00`，不是完整开发项目 |
| 最终交付提交 | 由最终发布回执提供；本文不把包含自身的提交 SHA 写成自指常量 |

**本文不是推送成功证明。** 若尚无已核验的最终发布回执，先确认远程产品分支确实包含上述实现祖先及完整目录；只有 Flow 基线 `206c82af…` 的同名分支还不够。最终交付 SHA 可能是增加审计文档后的后继提交；不得把源码 SHA、CI scaffold SHA、文档提交 SHA 混为一谈。

完整项目状态和全部 119 项尚未正式完成任务分别见 [PRD 审计](PRD-IMPLEMENTATION-AUDIT.md) 与 [剩余任务](REMAINING-TASKS.md)。25 项正式完成、2 项在建、117 项未开始是此次源码审查快照，不是后续本地会话应硬编码的永久状态。

## 2 Windows 使用 WSL2 执行仓库命令

本仓库测试大量使用 Bash、Linux 容器、文件权限、nginx 和真实 PostgreSQL/PostGIS。建议 Windows 用户在 WSL2 中执行后续 Bash 命令，项目放在 Linux 用户目录，例如 `~/src`；不要把 PowerShell、Git Bash、Windows Python 和 WSL Python 混在一个依赖目录内。此建议不等于已在你的 Windows 机器验收。

在 PowerShell 查看现有环境：

```powershell
wsl --version
wsl --list --verbose
```

没有 WSL 时，按微软官方指南以管理员 PowerShell 执行 `wsl --install`，按提示重启并创建 Linux 用户；不要重装或注销已有发行版。新安装默认采用 WSL2，已有系统先核验实际版本。[微软 WSL 安装说明](https://learn.microsoft.com/en-us/windows/wsl/install)

安装并启动 Docker Desktop，选择 WSL2 backend / Linux containers，在 `Settings > Resources > WSL Integration` 开启目标发行版；不要为“修好 Docker”删除旧卷、重置 Desktop 或再盲装第二套 WSL 内 Docker daemon。具体系统兼容性和软件使用条件按官方当前说明确认。[Docker WSL2 集成](https://docs.docker.com/desktop/features/wsl/)、[Windows 安装说明](https://docs.docker.com/desktop/setup/install/windows-install/)

在 WSL Bash 内检查，不显示凭据：

```bash
uname -a
git --version
docker version
docker compose version
docker info >/dev/null
```

工具缺失、daemon 不可用、发行版版本不兼容或浏览器 sandbox 无法启动时，记录具体阻塞；不要用 `chmod 777`、关闭安全策略、特权容器或禁用浏览器 sandbox 替代修复。系统安装/安全设置由你或获授权的本地 Agent 处理。

## 3 在新目录安全克隆完整历史

以下命令在 WSL/Linux Bash 中运行。目标目录已存在会中止，绝不覆盖。不要加 `--depth 1`：任务证据校验会检查历史冻结提交及其文件。

```bash
set -euo pipefail
mkdir -p "$HOME/src"
cd "$HOME/src"
test ! -e WeaveSense-Flow-Digital || {
  echo '目标目录已存在；请使用新目录，或先检查已有工作区。' >&2
  exit 1
}
git clone --branch feat/agriculture-v1 \
  https://github.com/newyngwieslash-ops/WeaveSense-Flow-Digital.git \
  WeaveSense-Flow-Digital
cd WeaveSense-Flow-Digital
git status --short
git branch --show-current
git rev-parse HEAD
git rev-parse 'HEAD^{tree}'
git remote -v
git rev-parse --is-shallow-repository
git merge-base --is-ancestor \
  7c6e0229e3e708ab540f338a7e01cb0f24d25a12 HEAD
```

最后的祖先检查必须退出 0；浅克隆应显示 `false`。失败表示未取得本次完整实现历史、远程尚未完成发布或分支不同，先处理来源问题，不能强行从旧基线继续。本次交付必须保留原始 Git 历史，不以重新生成的等价文件树或新造提交替代。

拿到最终发布回执里的完整 40 位 SHA 后，在克隆目录补做检查；必须替换占位值：

```bash
EXPECTED_DELIVERY_SHA='替换为最终发布回执的40位SHA'
if [[ ! "$EXPECTED_DELIVERY_SHA" =~ ^[0-9a-f]{40}$ ]]; then
  echo '尚未填写已核验的最终交付SHA。' >&2
  exit 1
else
  actual=$(git rev-parse HEAD)
  test "$actual" = "$EXPECTED_DELIVERY_SHA" || {
    echo '当前HEAD与交付回执不同：请审查后续提交，不要reset。' >&2
    exit 1
  }
fi
```

若分支在交付后继续前进，不必自动回退；先比较实际新增提交及证据，记录你从哪一个版本开始。GitHub 身份验证交给现有凭据管理器或受控 SSH，不把 Token 拼进 URL、提示词或 shell 历史。

### 已有本地仓库

先检查状态与远程，保留所有未提交文件。不要直接 `git pull`、切换覆盖用户文件或硬重置。确认 `origin` 是指定仓库后，可只读更新 refs，再由 Agent 在一个尚不存在的 sibling worktree 和新分支上继续：

```bash
git status --short
git remote -v
git fetch origin
# 先确认下列新分支名、目录均未使用，再执行。
git worktree add -b local/agriculture-resume-20261102 \
  ../WeaveSense-Flow-Digital-local origin/feat/agriculture-v1
```

新 clone 通过来源检查后，也可以在该 clone 用 `git switch -c local/agriculture-resume-20261102` 创建本地继续分支。分支名已存在时换一个明确的新名字，不强制覆盖。

## 3A 远程完整发布受阻时使用完整历史 bundle

只有实际收到包含 **`DigitalTwin-Local-Handoff.bundle` 和 `DELIVERY-MANIFEST.json`** 的新交付包，才可使用本节。只有审计 Markdown/JSON 和提示词的包不含完整源码；不能凭文件名猜 bundle 已经交付。本节提供验证和恢复方法，不证明当前 GitHub 推送、bundle 生成或上传已经完成。

bundle 是相对固定 Flow 基线的增量 Git 历史，不是源码 ZIP，不能直接解压运行，也不能单独浅克隆。manifest 必须提供：`source_implementation_sha`、`delivery_commit`、`delivery_tree`、`baseline_commit`、`bundle_ref`、`bundle_sha256`、`bundle_size`。最终提交和 tree 由实际打包回执填入，本文不预填不存在的 SHA。

1. 将收到的这两个文件保留在仓库外的同一私有目录，不改 manifest。
2. 在 WSL Bash 中设置 `HANDOFF_DIR` 为该目录的实际绝对路径，再克隆固定基线所在的完整 Flow 历史到**新目录**。`flow-source` 仅用于读取，不向其推送：

```bash
set -euo pipefail
export HANDOFF_DIR='/替换为实际交付包所在的绝对目录'
test -f "$HANDOFF_DIR/DELIVERY-MANIFEST.json"
test -f "$HANDOFF_DIR/DigitalTwin-Local-Handoff.bundle"
mkdir -p "$HOME/src"
cd "$HOME/src"
test ! -e WeaveSense-Flow-Digital-from-bundle || {
  echo '恢复目录已存在，停止以免覆盖。' >&2
  exit 1
}
git clone --origin flow-source \
  https://github.com/newyngwieslash-ops/WeaveSense-Flow.git \
  WeaveSense-Flow-Digital-from-bundle
cd WeaveSense-Flow-Digital-from-bundle
git remote add origin https://github.com/newyngwieslash-ops/WeaveSense-Flow-Digital.git
```

3. 在这个新 clone 中验证包大小/SHA256、基线前置、bundle 内真实 ref，再导入到尚不存在的本地分支。以下不强推、不重置、不覆盖已有 ref；任一验证失败立即中止：

```bash
python3 - <<'PY'
import hashlib, json, os, re, subprocess
from pathlib import Path

directory = Path(os.environ['HANDOFF_DIR']).resolve(strict=True)
bundle = directory / 'DigitalTwin-Local-Handoff.bundle'
manifest = json.loads((directory / 'DELIVERY-MANIFEST.json').read_text())
baseline = '206c82af7ae007f45da4c582f373fbabf027f1b8'
source = '7c6e0229e3e708ab540f338a7e01cb0f24d25a12'
bundle_ref = 'refs/heads/delivery-handoff-20261102'
branch = 'local/agriculture-resume-20261102'
def require(condition, message):
    if not condition:
        raise SystemExit(message)

require(manifest['baseline_commit'] == baseline, '错误基线')
require(manifest['source_implementation_sha'] == source, '错误实现来源')
require(manifest['bundle_ref'] == bundle_ref, '错误bundle ref')
for key in ('delivery_commit', 'delivery_tree'):
    require(re.fullmatch(r'[0-9a-f]{40}', manifest[key]), '无效的' + key)
require(re.fullmatch(r'[0-9a-f]{64}', manifest['bundle_sha256']), '无效的bundle摘要')
require(type(manifest['bundle_size']) is int and manifest['bundle_size'] > 0, '无效的bundle大小')
require(bundle.stat().st_size == manifest['bundle_size'], 'bundle大小不符')
hasher = hashlib.sha256()
with bundle.open('rb') as stream:
    for chunk in iter(lambda: stream.read(1024 * 1024), b''):
        hasher.update(chunk)
require(hasher.hexdigest() == manifest['bundle_sha256'], 'bundle摘要不符')

def git(*args):
    return subprocess.check_output(['git', *args], text=True).strip()

require(git('rev-parse', '--is-shallow-repository') == 'false', '不可用浅克隆')
require(not git('status', '--porcelain'), '工作区有改动，停止')
subprocess.run(['git', 'cat-file', '-e', baseline + '^{commit}'], check=True)
subprocess.run(['git', 'bundle', 'verify', str(bundle)], check=True)
heads = git('bundle', 'list-heads', str(bundle), bundle_ref).splitlines()
require(heads == [manifest['delivery_commit'] + ' ' + bundle_ref], 'bundle提交与manifest不符')
existing = subprocess.run(['git', 'show-ref', '--verify', '--quiet', 'refs/heads/' + branch])
require(existing.returncode == 1, '本地分支已存在或ref检查失败')
subprocess.run(['git', 'fetch', str(bundle), bundle_ref + ':refs/heads/' + branch], check=True)
subprocess.run(['git', 'switch', branch], check=True)
require(git('rev-parse', 'HEAD') == manifest['delivery_commit'], '恢复HEAD不符')
require(git('rev-parse', 'HEAD^{tree}') == manifest['delivery_tree'], '恢复tree不符')
subprocess.run(['git', 'merge-base', '--is-ancestor', source, 'HEAD'], check=True)
subprocess.run(['git', 'merge-base', '--is-ancestor', baseline, 'HEAD'], check=True)
print('bundle和原始历史恢复通过；尚未向GitHub推送。')
print('HEAD=' + git('rev-parse', 'HEAD'))
PY
```

再执行本指南第 4 节的完整目录/规格/证据检查。bundle 的大小和哈希只验证交付完整性，不替代接收渠道可信性或产品测试；恢复成功也不等于 GitHub 上已经存在该版本。

### 使用自己的 GitHub 凭据正常推送

仅当你明确要把这个**已核验的交付版本**补推到指定目标时执行。使用你现有的 GitHub 凭据管理器/SSH 登录；若没有写权限或 workflow 权限，交给账户持有人解决，不把 Token 贴入聊天。先检查目标 URL、抓取当前分支、确认 fast-forward、检查 dry-run，再普通推送：

```bash
set -euo pipefail
case "$(git remote get-url origin)" in
  https://github.com/newyngwieslash-ops/WeaveSense-Flow-Digital.git|git@github.com:newyngwieslash-ops/WeaveSense-Flow-Digital.git) ;;
  *) echo 'origin不是指定目标，停止。' >&2; exit 1 ;;
esac
test -z "$(git status --porcelain)" || {
  echo '工作区存在未提交改动，先审查。' >&2
  exit 1
}
git fetch origin refs/heads/feat/agriculture-v1:refs/remotes/origin/feat/agriculture-v1
git merge-base --is-ancestor origin/feat/agriculture-v1 HEAD || {
  echo '远程不是本地祖先，禁止覆盖；请审查新增提交。' >&2
  exit 1
}
git push --dry-run origin HEAD:refs/heads/feat/agriculture-v1
# 检查上面dry-run确为正确目标和普通fast-forward后，再执行下一行。
git push origin HEAD:refs/heads/feat/agriculture-v1
local_sha=$(git rev-parse HEAD)
remote_sha=$(git ls-remote origin refs/heads/feat/agriculture-v1 | awk '{print $1}')
test "$local_sha" = "$remote_sha" || {
  echo '推送后的远程SHA尚未匹配，不能宣称成功。' >&2
  exit 1
}
printf '已核验远程SHA %s；CI与产品验收仍需分别检查。\n' "$remote_sha"
```

若目标分支尚不存在，上述 fetch 会停止；先确认仓库及创建分支权限，不自行改成 `main`、CI 分支或另一仓库。任何 non-fast-forward、保护分支或权限错误都保留，不使用 `--force`、移除 `.github/workflows` 或重新生成提交来绕过。正常推送后仅把精确 SHA 的实际 CI 结果作为新运行证据，不能把旧绿色状态当作本次通过。

## 4 检查取到的是全量产品

在仓库根目录运行以下只读检查：

```bash
python3 - <<'PY'
import json
from pathlib import Path
required = [
    'AGENTS.md', 'backend/go.mod', 'backend/agent-runtime/pi/package.json',
    'web/package.json', 'web/bun.lock',
    'services/loom-engine/package.json', 'services/math-service/mathsvc/server.py',
    'services/agri-gis/backend/pyproject.toml',
    'services/agri-gis/backend/poetry.lock',
    'services/agri-gis/frontend/package-lock.json',
    'contracts/agriculture', 'deploy/agriculture', 'scripts/agriculture',
    'tests/agriculture', 'industry-packs',
    'specs/agriculture/v1.0.0/PRD.md',
    'specs/agriculture/v1.0.0/docs/implementation/RECOVERY_STATUS.json',
    'specs/agriculture/v1.0.0/docs/implementation/evidence',
]
missing = [p for p in required if not Path(p).exists()]
if missing:
    raise SystemExit('缺少完整项目路径：' + ', '.join(missing))
spec = Path('specs/agriculture/v1.0.0')
tasks = json.loads((spec/'tasks.json').read_text())['tasks']
req = json.loads((spec/'requirements.json').read_text())
if len(tasks) != 144 or len(req['functional']) != 80 or len(req['nonfunctional']) != 20:
    raise SystemExit('需求/任务总数与完整规格不符。')
from collections import Counter
print('目录与任务数量检查通过；这不是运行或产品验收。')
print(dict(Counter(t['status'] for t in tasks)))
PY

python3 scripts/agriculture/spec_status.py --check --require-evidence
```

第一段只是最低完整性哨兵，不代替完整 Git tree 或来源清单。第二条会检查需求不可变基线、144 项结构/依赖、Markdown 状态、证据文件哈希及历史冻结提交；失败需要诊断缺失来源或状态漂移，不能先运行 `--sync` 掩盖问题，更不能改验证器放宽标准。

## 5 先把完整任务交给本地会话

在 Zcode/Codex 中打开上述**完整仓库根目录**，不是 `specs/` 子目录，也不是 `ci/agriculture-w00` 的临时装配目录。将 [完整继续开发提示词](LOCAL-AGENT-HANDOFF-PROMPT.md) 中“开始复制”到“复制结束”的内容一次性提交。

会话首先应读取原始 PRD、任务账本和实际证据，核对 DT-026/041，而不是从 W00 重造项目。让 Agent 自己核查工具和当前目录；不要求你先配置真实模型、支付或设备才能开始写其余代码。以下环境说明用于你或 Agent 复现已存在门禁。

## 6 工具链与依赖按当前仓库锁定

本次审查的 `.github/workflows/agriculture-identity.yml` 等实际配置为：

| 组件 | 仓库中的版本/命令依据 |
| --- | --- |
| Go | CI `1.25.9`，`backend/go.mod` 语言版本 `1.25.0` |
| Node | CI/农业 Web builder `24.22.1`；Pi 自身要求 `>=22.19.0`，各镜像还可能采用不同固定版本 |
| Bun | `1.3.9`，React `web/package.json` 的 packageManager 和 `web/bun.lock` |
| Python | CI `3.12`；GIS pyproject 声明 `^3.11`，具体包按锁文件 |
| uv | CI `0.13.4` |
| PostgreSQL/PostGIS | Docker `postgis/postgis:17-3.5`；已有原生测试另固定 Debian PG17.11/PostGIS3.5.2，二者证据不能混用 |
| Redis | `redis:7.4.2-alpine` |
| 浏览器/nginx | 使用真实可运行 Linux 浏览器和 nginx，记录实际版本；不同 runner/镜像版本不冒称相同 |

这些是仓库配置，不是“你本机已安装”。先执行 `go version`、`node --version`、`bun --version`、`python3.12 --version`、`uv --version`。缺工具时依官方渠道安装所需版本，不执行来源不明的一键远程脚本。编译 Go race/cgo 还需适用的本机 C 工具链；缺失应明确报告。

### 实际农业前端构建

以下命令来自当前 CI/Dockerfile，在干净工作区运行会安装锁定依赖并生成忽略产物：

```bash
(cd web && bun install --frozen-lockfile --ignore-scripts && bun run build:agriculture)
(cd services/agri-gis/frontend && npm ci --ignore-scripts --no-audit --no-fund && npm run build:agriculture)
```

React 不使用 npm/pnpm 替换 Bun 锁文件；Vue 不用随意 `npm install` 更新依赖。原前端 `build` 与 `build:agriculture` 不是同一个产物。测试需要的 Pi/Loom 依赖也必须读取各自 `package.json`、锁文件和 workflow 再安装；不要在仓库根运行不存在的全项目 `npm install`。

### 哈希锁定的 Python 环境

以下来自 identity/parser CI。在首次新建的 `.local/identity-rebuild/venv` 中执行；已有环境先检查，不删除重建来掩盖失败：

```bash
export UV_CACHE_DIR="$PWD/.local/cache/uv"
mkdir -p .local/identity-rebuild
python3 scripts/agriculture/export-python-lock.py --group main --output .local/identity-rebuild/main.txt
python3 scripts/agriculture/export-python-lock.py --group dev --output .local/identity-rebuild/dev.txt
test ! -e .local/identity-rebuild/venv
uv venv --python python3.12 .local/identity-rebuild/venv
uv pip install --python .local/identity-rebuild/venv/bin/python \
  --require-hashes --no-deps --only-binary :all: --index-url https://pypi.org/simple \
  -r .local/identity-rebuild/main.txt -r .local/identity-rebuild/dev.txt \
  -r scripts/agriculture/python-test-requirements.txt
uv pip check --python .local/identity-rebuild/venv/bin/python
export AGRI_IDENTITY_PYTHON="$PWD/.local/identity-rebuild/venv/bin/python"
```

不要去掉 `--require-hashes` 或为了装上包重写锁文件。若固定 wheel 在你的架构不可用，记录平台差异并审查可复现方案；不是放宽供应链验证的理由。

## 7 已存在的隔离验证入口

所有命令从仓库根运行，先读取对应脚本的前置条件。脚本会创建、启动并清理**本次独占**的随机测试资源，不连接你的正式数据库。不并行运行会覆盖同名 `.local/evidence` 输出的测试；每轮后保存日期/源码匹配的脱敏结果。

| 命令 | 实际范围与前置 |
| --- | --- |
| `bash scripts/agriculture/ci-smoke.sh core` | 真实 Docker PostGIS/Redis、角色与依赖探针；不是 GIS 业务验收 |
| `bash scripts/agriculture/ci-smoke.sh migrations` | 上述基础上执行平台/Loom 真迁移和重放；不是全产品启动 |
| `bash scripts/agriculture/membership-ci.sh` | 真实 PostgreSQL 平台成员/隔离/旧 Flow 等回归；需要 Go/Python/Docker |
| `bash scripts/agriculture/parser-docker-ci.sh` | 真实受限 parser Docker；需要非 root 用户、锁定 Python、Docker、`.local`；不是上传全生命周期 |
| `bash scripts/agriculture/identity-postgres-ci.sh` | 当前平台/GIS/PostGIS/Redis/浏览器链的集中入口；必须先按下面顺序配置，不默认具备浏览器条件 |

identity 完整门禁前，应按当前 `.github/workflows/agriculture-identity.yml` 完成两套前端构建、Go/Python 工具链、真实 nginx 与 Linux Chromium/Chrome，设置已实际核验的 `NGINX_BIN`、`NGINX_MIME_TYPES`、`CHROME_BIN` 绝对路径。不从 Windows 浏览器路径猜一个 Linux 可执行文件；不把任意 headless 测试当成 sandbox 正常。

先用独立新库验证正式 GIS 迁移，然后再运行另一轮独立新库的完整链：

```bash
# AGRI_IDENTITY_PYTHON、NGINX_BIN、NGINX_MIME_TYPES、CHROME_BIN 已核验。
AGRI_GEO_MIGRATION_ONLY=1 AGRI_IDENTITY_CHAIN_REQUIRED=0 \
  bash scripts/agriculture/identity-postgres-ci.sh

AGRI_GEO_MIGRATION_ONLY=0 AGRI_IDENTITY_CHAIN_REQUIRED=1 \
  bash scripts/agriculture/identity-postgres-ci.sh
```

第二轮会真实要求 farm/parcel/asset 浏览器链、真实 Redis、双数据库、race 和严格归约器；不是可选浏览器演示。缺 nginx/Chromium 时标 `not_run` 或明确环境阻塞，不改为 component-only 后声称门禁成功。保存迁移轮的脱敏报告后再运行第二轮，避免前一轮同名输出被初始化覆盖。

六条 GitHub workflow 的结果分别保留。普通 `go test` 的集成 `skip`、runner 单测、纯 HTTP、SQLite fixture、本地浏览器、远程 CI 各有独立证据层级。DT-026/041 必须核对当前候选，不挪用 DT-025 的历史成功。若发布版本有新的 CI 成果，以实际 SHA/run/attempt/provenance 为准。

原生无 Docker 路线见 `docs/agriculture/NATIVE_POSTGRES_TESTING.md` 等，仅针对它声明的 Debian 13 amd64 包和共享运行库，**不是可直接照搬到所有 WSL Ubuntu 的通用安装器**。不要为复制云端测试去替换系统库。

## 8 可启动的本地基线不等于完整产品

审查时 `docker-compose.agriculture.yml` 仅定义 `web`、`postgres`、`redis`、`geo-readiness`、`platform-migrate`、`platform`、`loom-migrate`、`loom-engine`。没有生产 GIS API/农业分析 worker 完整编排；`geo-readiness` 不承载 GIS 领域路由。该缺口应交给本地 Agent 按原任务继续补齐，而不是写不存在的 `docker compose up agri-worker`。

第一次准备持久本地开发环境：

1. 用新项目/卷隔离已有 Flow 和用户数据。先检查 `.env.agriculture` 是否存在；存在就不覆盖。不存在时从 `deploy/agriculture/env.example` 复制，设私有权限 `0600`。
2. 用获准的本地秘密管理方式填写各不相同、至少 32 字符的 URL-safe 随机秘密，不能复用模板/原 AgriScope 示例密码。不要打印环境文件、完整 `docker compose config` 或 DSN。
3. 开发 HTTP 必须使用实际回环 origin：默认 `FLOW_AGRICULTURE_EXTERNAL_ORIGIN=http://127.0.0.1:8091`，并显式设 `FLOW_AGRICULTURE_ALLOW_HTTP_LOOPBACK_DEV=true`。这不允许公网 HTTP，不把 `localhost` 随意替代 literal loopback。
4. 按 `env.example`/`BROWSER_SECURITY.md` 配置独立的绝对路径 `FLOW_AGRICULTURE_CSRF_KEY_FILE`，指向**恰好 32 个随机原始字节**、权限 `0600` 的常规文件，不是 32 位文本或共享 JWT 密钥；由本地 Agent 安全新建且不得覆盖已有密钥。
5. 农业 GIS 签名身份、生产域 provider、farm dictionary、资产权利等配置按各自部署合同准备；模板故意为空、不构成可用业务数据。缺配置应明确关闭，不自动插入虚构地区/真实客户/账户。

配置完成后，现有命令为：

```bash
docker compose --env-file .env.agriculture -f docker-compose.agriculture.yml \
  --profile core --profile platform --profile web config --quiet
docker compose --env-file .env.agriculture -f docker-compose.agriculture.yml \
  --profile core --profile platform --profile web up -d --build
```

默认 `http://127.0.0.1:8091` 是同源 React/Vue/平台网关，`http://127.0.0.1:8090/health/ready` 只是依赖探针。静态首页可打开、容器 running、healthcheck 成功都不代表 GIS API、登录业务、worker 或农业闭环已经可用。平台默认关闭公开注册；没有经验证的账户初始化流程时让 Agent补齐/核验正确流程，不为演示开放注册或写入管理员绕过授权。

`knowledge` profile 可按现有部署说明单独评估，但其 `LOOM_ROLE=api` 可能启动既有 collection worker；这不是农业 W06 分析 worker。当前 Compose 未配置完整 `LOOM_ENGINE_URL`、平台反向授权/预算/回调链，internal 网络与空白测试库也不代表真实外部采集已接通。不要导入生产任务或凭据到这个基线。

停止自己启动的环境，不删数据卷：

```bash
docker compose --env-file .env.agriculture -f docker-compose.agriculture.yml \
  --profile core --profile platform --profile web stop
```

若实际启用了 `knowledge`，使用同一组 profile 停止。不得以 `down -v`、Docker prune、删数据库目录或重跑首启脚本来“修复”迁移；已有卷秘密变更需受控轮换，修改 env 不会自动更新数据库密码。

### 已确认的文档陈旧点

在审查源码 `7c6e0229…` 上：

- `deploy/agriculture/README.md` 尾部引用的 `scripts/agriculture/doctor_test.py` 不存在；原 `doctor.py` 同样不存在，不应尝试运行。
- `docs/agriculture/GIS_MIGRATION_COMMAND.md` 引用的 `PYTHON_ENVIRONMENT.md` 未在完整项目中找到；使用当前 workflow 的锁定导出/安装方式，而不是寻找假定存在的文件。
- 旧入口里“全部任务未开始”、旧网关/角色文档里“CI 待运行”等是对应历史时点，不应覆盖最新 `tasks.json` 和证据；也不能反过来从历史测试通过推断当前分支全部通过。
- 当前完整生产 GIS/worker/对象存储和真实用户闭环不是本 Compose 已交付能力。不能用旧 AgriScope `docker-compose.yml` 和默认账号绕过农业可信身份边界。

本指南对命令做的是**源码存在性和配置核对**，没有在你的 Windows/WSL 电脑执行。后续 Agent 应把实际首次运行结果及修复同步到正式运行文档。

## 9 暂停与再次打开会话

每轮至少保存：本地路径、分支/HEAD/tree、未提交改动、已完成任务、测试命令与退出码、真实失败/未运行、脱敏证据位置、运行进程/本次卷、外部阻塞、下一依赖就绪任务。原始日志、数据库、环境文件和真实数据留在忽略的私有目录，不提交。

下次会话可以直接说：

> 继续本仓库 WeaveSense-DigitalTwin。先读取根和相关 AGENTS、specs/agriculture/v1.0.0/AGENT_MASTER_PROMPT.md、tasks.json、docs/implementation/STATUS.md、最新工作包证据，以及 docs/agriculture/handoff/2026-11-02/LOCAL-AGENT-HANDOFF-PROMPT.md。核对当前分支/HEAD和用户未提交修改，从检查点继续全部剩余工作；不重做已验证任务，不降低真实 PostgreSQL/浏览器/worker/许可及外部门禁。缺外部条件只阻塞对应项，其余持续实施。

完成判定仍按完整 PRD 和 G0-G5，不按“能启动”或任务数量。真实数据、模型预算、支付、生产发布、现场设备等缺项由你提供具体输入/授权后再解除，不要让 Agent 为凑全完成状态自行选择或伪造。

