# GIS 不可信输入与来源下载边界（DT023）

## 已实现范围与未开放范围

这是实际解析、来源下载和隔离执行组件，不是新增上传业务、STAC 商业接入或 MCP 任意 URL 工具。农业产品没有自动注册旧上传、公开影像下载路由；没有启用任何真实外部来源，也不代替后续持久作业、数据目录、农业影像实体或许可验收。

代码位于 `services/agri-gis/backend/app/agriculture/ingestion/`。解析进程直接调用既有 `app/core/dataset_files.py:inspect_dataset_file`，外业表格进一步调用既有 `FieldVerificationWorkbookParser.parse`，不另造返回成功的模拟解析器。

审查中发现的旧入口：

| 旧代码 | 原行为与新边界 |
|---|---|
| `services/dataset_asset_service.py` | 原上传暂存、流量限额和签名检查仍保留；新农业接入应调用 `SafeParser`，不能直接绕过隔离调用旧解析。旧路由仍关闭。 |
| `core/dataset_files.py` | 原 TIFF 缺像元/波段上限、允许无 CRS；新固定子进程先执行严格预检与实际窗口解码，再复用原检查。 |
| `services/field_workbook_parser.py` | 复用真实业务字段验证；外加 OOXML 解压、外部关系和进程限额。 |
| `services/public_imagery_client.py` | 原 urllib 跳转/读取方式不能作为农业安全下载入口。 |
| `services/public_imagery_engine.py` | 原 Rasterio 直接打开远程 URL 不进入新安全路径；农业来源必须先经批准下载为受控本地文件，再隔离解析。 |

## 生产隔离与资源限额

`configured_parser(owned_scratch_root)` 只接受管理员配置 `AGRI_PARSER_IMAGE` 的不可变 Docker image ID/digest。没有 Docker、没有固定镜像或无法清理容器是依赖错误，`IngestionDenied.status_code == 503`；**没有普通 Popen 的生产回退**。调用者只得到固定错误码，不返回原始路径、URL、解析器堆栈。

`DockerSandbox` 使用网络 none、只读根文件系统、唯一只读输入目录、无 capability、no-new-privileges、32 PID、内存与 swap 同上限、1 CPU、非 root UID/GID、受限 tmpfs。不会挂载整个项目、宿主家目录、数据库、Docker socket 或父进程秘密。镜像是受信管理员构建物；不可变 digest 并不意味着任意第三方镜像自动可信。

固定 Python 入口使用 `-I`、固定源目录、净环境和 `RLIMIT_AS/CPU/FSIZE/NOFILE/CORE`。默认输入 16 MiB、展开 64 MiB、1000 成员、压缩比 100、1600 万像元、16 波段、10000 行/要素、100 万坐标/单元格、768 MiB 地址空间、10 CPU 秒、15 秒墙钟、64 KiB 结果；`ParseLimits` 有不可超出的硬上界。每次随机命名私有临时目录；异常、超时或取消杀死专属进程组并删除本次容器、输入和产物。

解析入口只接受已经完成的本地普通文件或内存字节流，不在启动监督前读取任意网络流/管道；上传传输自身的限流与超时是调用端的独立责任。文件与源代码由可信服务进程拥有，不声称防御同时控制该服务 UID 的宿主攻击者。

普通非 root 子进程加 rlimit **不是内核文件系统/网络沙箱**。本地 `LocalResourceFixture` 和资源探针只在测试代码中显式装配，标签明确区分，不可当作生产隔离证据。当前执行环境无 Docker，正常 bwrap 隔离尝试被环境权限拒绝；未绕过该限制。2026-11-01 只读 CI 首次实际运行三项容器测试，隔离与超时清理通过，栅格输入项失败，完整容器验收尚未通过；历史组件证据不替代该失败。

支持的收窄格式：GTiff、GeoJSON/GPKG、CSV、XLSX、DOCX、PDF、仅检查不解包的 ZIP。无 CRS 拒绝；不猜单位或轴序；拒绝 VRT、远程子数据集等不在固定名单的格式。实际 GDAL/Rasterio/Fiona 解码在限制内进行。ZIP 逐字节累计实际展开大小、CRC 校验、拒绝路径穿越/符号链接/重复名/嵌套归档/外部 OOXML 关系/DTD/实体；不向宿主解压文件。Office/PDF 实际解析并返回有界摘要，不执行宏、公式或文档动作。

## 固定来源下载

入口 `SafeDownloader.download(source_id, artifact_id, owned_directory)` 不接受用户 URL、Cookie、Authorization、代理设置或任意请求头。`ApprovedSource` 由受信服务代码从经批准的来源目录构造，包含确切 HTTPS origin、批准路径前缀、命名 artifact→URL、方法、审批引用与有效期；MCP/浏览器只能提交 ID。当前生产来源为空/禁用，缺配置不得自动联网。

`source_policy.RegistryRights` 复用 `contracts/agriculture/source-rights.py:authorize`（该既有引擎 profile 为 `production`，不是产品名 `agriculture`）。部署装配必须显式提供已有权利引擎及锁定依赖、可信 registry loader；缺失依赖失败，不内嵌宽松许可替代。每一跳和最终原子发布前重新加载并检查权利/谱系/用途/有效期；只接受返回 `allowed is True`，None/False 不算批准。测试中的批准清单是自编合成策略，不是获得实际数据授权。

所有请求逐跳拒绝非 HTTPS、非 443、userinfo、片段、查询凭据、编码路径别名、点路径、未批准主机/路径。DNS 在净环境固定子进程中限时解析；所有结果都必须是公网单播，拒绝混合公网/私网、云 metadata、IPv4-mapped/6to4/Teredo/带 zone IPv6。连接钉住已验证数字 IP，TLS 证书与 SNI 仍校验原批准主机；重定向重新解析和检查，不使用第二次不受控 DNS，不继承代理环境。

默认 16 MiB 响应和 15 秒总时限，最多 3 次跳转，禁止压缩响应，校验实际字节数与 Content-Length。临时文件是 0600，下载完成后再次校验权利再无覆盖原子发布；失败清理本次部分和未成功发布的完整文件。返回的本地路径只供内部处理，不能直接序列化为浏览器对象 URL。真实 loopback 测试仅注入测试传输，不存在允许私网的生产环境变量。

## 可复现验证与证据边界

解析镜像原生依赖：Debian trixie `libexpat1=2.8.3-1~deb13u1`（Expat MIT 许可，保留包内 copyright），通过已签名 APT 元数据安装固定版本。
实际 CI `41275760183` 已确认 GDAL wheel 缺少外部 `libexpat.so.1` 导致 Rasterio 导入失败；修复不改变网络、文件系统、权限或资源限制。
镜像在非 root 用户下执行 Rasterio/Fiona import 构建检查；它只证明动态库可加载，不能代替三项真实容器验收。

锁定 GIS Python 环境下，从 `services/agri-gis/backend` 运行：

```sh
python -m pytest -q tests/test_agriculture_ingestion_parser.py \
  tests/test_agriculture_ingestion_limits.py tests/test_agriculture_ingestion_download.py
```

测试覆盖实际 Rasterio/Fiona/原外业 XLSX/DOCX/PDF、异常 CRS/损坏栅格/大像元、归档炸弹、实际进程 CPU/内存/文件/输出/墙钟、实际自有 HTTP 字节流、超额/慢速/恶意重定向/权利撤回清理。DNS/TLS 钉住参数有单独契约测试，不伪称连接了真实外部 STAC。

支持 Docker 的非 root 开发/只读 CI 节点运行：

```sh
AGRI_IDENTITY_PYTHON=/absolute/locked/venv/bin/python \
  bash scripts/agriculture/parser-docker-ci.sh
```

脚本沿用官方锁文件导出工具和 hashes，构造显式白名单临时 build context；官方 Python 版本镜像先解析 digest 后构建，实际执行使用本地不可变 image ID。产物 `.local/evidence/parser-docker.json` 包含真实镜像、代码哈希和三项严格测试，不上传原始输入或日志。三项必须执行通过，skip 不算通过；包括实际容器解析/异常清理、未挂载宿主 canary 不可读、宿主自有 loopback 服务不可达、无默认路由、输入/根只读及超时清理。本地无 Docker 时此证据为 **not_run**，不能以配置检查替代；已运行的远程失败单独保留，不写回 not_run。失败诊断仅记录固定阶段、白名单异常类和固定代码内行号，测试探针不替换生产路径结果或原异常。

参照：[Python resource](https://docs.python.org/3/library/resource.html)、[Docker run](https://docs.docker.com/reference/cli/docker/container/run/)。rlimit 证明与容器隔离证明分别记录；没有宣称 VM 隔离、实时外部采集、业务上传端到端或商业可发布。
