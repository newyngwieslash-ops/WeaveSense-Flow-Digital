# DT-023 验收检查点：in_progress

## 2026-11-01 第三轮已定位缺库，修复待容器复验

[实际运行 41275760183](https://github.com/newyngwieslash-ops/WeaveSense-Flow-Digital/actions/runs/41275760183) 对应 CI `8de35b37`、产品 `40338419`；仍 2 pass / 1 not_pass，固定诊断明确 `missing_library=libexpat.so.1`。
原报告、24 输入哈希映射和固定 Debian 包元数据保留于 `ci-8de35b37/`。
镜像仅补 `libexpat1=2.8.3-1~deb13u1` 并增加非 root Rasterio/Fiona import 构建检查；保留原运行时网络/文件/权限/资源限制和所有验收断言。
本地修复后组件 77 passed、0 skipped（19.73 秒、退出 0）；reporter 3、wiring 5、prepare 8 项通过。独立代码审查通过；这里没有 Docker，因此实际三项复验由真实 CI 执行，通过前不标完成。

## 2026-11-01 第二轮诊断

CI `d77a5c36605b9da22f5a035412b3addb5c9e558e` 对应产品源码 `2cfe8d8ce89a524b9a5f236d10e5bfd000cd88c9`。
[实际运行 41275361944](https://github.com/newyngwieslash-ops/WeaveSense-Flow-Digital/actions/runs/41275361944) 的三项仍为 2 pass / 1 not_pass。
固定诊断为 valid_raster、ImportError、代码行 8、parser_rejected_input；即 rasterio 导入阶段失败，不能归因为外部凭据或未执行。
24 个解析源码哈希逐个匹配固定产品提交；原始 JSON 和独立核验保存在 `ci-d77a5c36/`。
下一诊断增量 `40338419` 仅允许输出四个预定系统动态库 basename，且必须完整匹配固定 loader 错误句；不输出任意异常，不更改生产沙箱或断言。
本地 wheel 的 readelf 确认这四项为实际外部依赖，但缺少哪一个必须由真实容器证明，不能猜装或声称修复成功。

## 2026-11-01 真实 CI 首轮失败

远端 CI `0040b606ec136c52b1d666c448a87a779411743c` 对应冻结产品 `ad4995c6e91b697dee396d933bcb7fa786dd3b9d`。
[实际运行 41274681614](https://github.com/newyngwieslash-ops/WeaveSense-Flow-Digital/actions/runs/41274681614)，job `124754346704`，artifact `13069649989`。
三项全部执行：宿主文件/网络隔离和超时清理通过，`test_docker_actual_raster_and_malformed_inputs` 失败，退出 1。
23 个解析输入与 assembly 382 overlay 均逐项匹配产品提交；原 JSON 保留于 `ci-0040b606/`。
报告原仅保存 not_pass，故追加测试专用固定阶段/异常类/代码行诊断后重新执行；不输出原始异常、日志、路径或输入，不修改生产隔离或放宽断言。
本地锁定 Python 77 项组件复验仍通过，不足以宣称容器通过。
原审批已解决，下一步是真实失败诊断和修复，不再等待旧卡，不进入 DT024。

## 2026-10-30 历史组件检查点（当时 blocked_external）

运行源码 `92f2146e2bb1989b0c3ad3ee07883e738de5a2b3`，最终本地报告 `components-final.json`：77 passed、0 skipped，退出0，31实际输入哈希逐项映射冻结提交。初版70项报告单列保留，不能替代修复后77项结果。独立审查关闭三个实际复现缺陷：几何业务标签、外业XLSX结构限额与最终来源权利复核跨总时限。

实际复用Rasterio/Fiona、原dataset检查/外业表格解析与文档解析；普通受限子进程证明CPU/内存/文件/输出/墙钟限制、净环境及清理，不证明宿主文件系统/网络隔离。真实自有loopback HTTP测试与固定来源策略证明下载边界，不代表真实外部来源已获授权或启用。

生产Docker适配器缺依赖则503，无普通进程回退。三项真实容器测试仍 **not_run**：本地无Docker daemon，普通远程CI发布受原单次清单上传审批阻塞。没有关闭主机安全措施或申请额外生产权限。不得标task_complete、W02/G0或完整AG-AC通过。

## 恢复步骤

1. 由唯一发布者 `/root/publish_digitaltwin_history` 先确认原待审批上传的实际结果；不盲重试，不发布旧候选覆盖新源。
2. 在最新冻结候选基础上，核对全部显式overlay和普通只读workflow，将正常进度发布到目标仓库CI分支（保留lease及历史，不强推）。本地完整产品历史与CI scaffold继续区分。
3. 运行 `.github/workflows/agriculture-parser.yml`：固定上游及显式overlay组装→hash-locked Python→`AGRI_IDENTITY_PYTHON=/absolute/locked/python bash scripts/agriculture/parser-docker-ci.sh`。必须取得三项实际Docker测试全部通过及精确源码/镜像证据，skip不算通过。
4. 读取真实终态和失败诊断；修复真实失败后重验。通过才更新023并进入024，不把组件77项或配置检查代替容器证明。

CI接线源 `3bcb94da` 的8项source-preparation和4项wiring测试已通过，权限仅contents:read、persist-credentials:false；无registry发布、privileged容器、生产部署或外部采集。两个已审入口/查询AST清单于 `78991992` 同步并通过检查及4+2测试；静态清单不是权限证明。
