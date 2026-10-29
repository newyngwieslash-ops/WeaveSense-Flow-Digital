# DT003 初始依赖及权利清单（环境重置后重新生成）

本目录恢复 W00 已完成工作的**当前可追溯证据**，不是把 DT003 重置为未开发，
也不是最终商用发布许可证证明。生成器只读取实际源码、锁、已安装元数据、
构建图和许可原文，不执行外部依赖代码、不修改锁文件、不把根 MIT 扩散到依赖。

## 范围与实测

- 固定上游：Flow `206c82af7ae007f45da4c582f373fbabf027f1b8`；
  AgriScope `571a2dcd9abf8b8a22b66c750ee710b61d482051`。
- `initial-sbom.json`：带原始路径/版本/锁哈希、通知文本哈希及内容寻址副本。
  `notices/` 原字节保留 MIT、Apache、BSD、OFL 等实际文本；不替换、删改版权署名。
- React 当前农业构建（含DT014已冻结导航）：1,237 模块、53 个唯一名称/版本包；安装树有1,906个
  package.json位置，实际构建模块定位到77个位置（嵌套重复依赖不是77个独立产品）。
  Vue农业会话/导航构建12条最终模块记录，仅4个Vue运行时包；
  安装树365个位置仍含旧GIS工具和开发依赖，不能将安装树等同发布可达图。
- Go锁图332模块；本机Linux server导入图71模块。已从精确版本缓存读取源码通知。
  该图是 `go list -deps`，**不是最终链接器或容器完整闭包**。缓存补齐在隔离
  go.mod/go.sum副本执行，原后端锁未改变；没有运行依赖脚本。
- Python独立恢复venv实际57个分发包（主依赖、开发及测试工具共存）；每个条目
  保留wheel元数据哈希及实际通知。Rasterio 1.4.4，内置GDAL 3.10.3，
  实际注册155个驱动。驱动列表**不证明**各驱动、编解码器或动态链接库权利已审结。
- Pi锁230条；Loom锁482条，其中341条非dev。这里是**声明锁图**，没有冒充
  本环境已装配的Node服务或实际生产镜像；Loom镜像是否包含dev依赖须按最终镜像复查。
- 本轮恢复用Debian PostgreSQL/PostGIS工具只属于测试环境，不作为本产品生产软件分发。

## 决策与未决项

| 项目 | 当前决策 | 发布条件 |
| --- | --- | --- |
| tldraw及其editor/store生态 | 农业实际模块图排除；不存在“根MIT覆盖tldraw”结论 | 重新启用前独立授权及构建复审 |
| Vue旧GIS业务、OpenLayers/Cesium | 当前农业构建不包含；保留源代码许可 | 未来业务工作台启用时重新产生可达图、通知和服务权利清单 |
| Go LICENSE/NOTICE | 直接保留实际版本原文；不从仓库名推断SPDX | 最终71模块及链接原生库逐项审阅，最终镜像生成发布SBOM |
| `github.com/chzyer/logex` | 当前模块包无根通知，server导入图未引用 | 不能因其他Go依赖MIT而补写；若分发该源码或启用需补精确来源证据 |
| `@ant-design/icons-svg@4.5.0` | npm包缺根通知；现构建使用恢复的版本专用LICENSE并已打入THIRD_PARTY_NOTICES | 上游精确commit/版本取得来源记录需补回，不能把恢复文件当作重新在线核验 |
| `is-mobile`、`kr/logfmt`等README内许可 | 完整授权与免责声明同时存在时保存整个实际README | 不是仅靠“MIT”字样猜测或自动撰写许可证 |
| 安装树4个缺license字段的包 | 见机器清单，当前农业可达图不引用 | 启用前审阅；不能借整个应用MIT推断 |
| 字体OFL、依赖Apache/MIT/BSD通知 | 原字节保留及随当前React构建打包 | 最终部署验证署名、字体单独分发/修改条件 |
| GDAL、Rasterio、PostGIS、native libraries | 各自许可独立；元数据/驱动注册证据不替代链接审计 | 最终镜像、编解码器和商业使用/分发复核 |

`inventory-gaps.json` 枚举缺声明、缺包内通知、无可用源码等，不隐瞒未决项。
其中“缺根通知”不必然表示侵权；也不能据license字段声称全部分发义务已满足。
所有Go模块仍标注文本需人工审查，而非自动生成法律批准。

## 数据、地图、模型与服务

权利登记模板复用 `contracts/agriculture/source-rights.template.json` 与对应schema。
实际 `deploy/agriculture/sources.json` 的底图、影像、行政边界、客户数据、模型权重、
知识来源6类均 `enabled=false`、权利未知，`requested_uses={}`。
没有将Sentinel/Landsat公开可获取、客户口头提供、模型代码开源视为全面商业再分发许可。

启用登记须包含权利人、来源/产品版本、授权证据、允许用途、商业/再分发条件、
署名文本、有效期和评审引用，并显式选择用途。既有source-rights门禁会拒绝缺失
许可、过期、未允许用途及不完整派生来源；本轮22项测试实际重新通过。
这是部署配置门禁，不声称所有未来业务数据通道运行时已接入（W03及后续任务负责）。

## 重建与验证

使用已按原Poetry锁和hash重建的环境，不使用系统Python包凑数：

```sh
.local/agri-identity-venv/bin/python scripts/agriculture/sbom.py
.local/agri-identity-venv/bin/python scripts/agriculture/sbom.test.py
.local/agri-identity-venv/bin/python -m pytest tests/agriculture/test_source_rights.py -q
.local/agri-identity-venv/bin/python scripts/agriculture/check-source-policy.py \
  --profile agriculture --config deploy/agriculture/sources.json
```

前置必须已有真实 `web/dist-agriculture/agriculture-build-graph.json` 与
`services/agri-gis/frontend/dist-agriculture/agriculture-profile.json`、对应安装树、Go精确
版本源码缓存。缺图、缺强制归属文件、可达未知许可、tldraw或Vue旧业务模块会失败。
Go缓存为空时先在独立模块副本下载锁定版本；生成器自身 `GOPROXY=off`，不联网解依赖。

Go下载最初因默认GOPATH下sumdb路径不可写失败；改为工作区 `.local/go-path`
且保持正常sumdb校验后精确版本下载成功，未关闭验证来掩盖下载失败。
生成器离线读取已验证缓存时 `GOSUMDB=off` 仅避免再次访问网络，不表示此前未验完整性。

新验证结果与哈希见 `recovery-validation.json`。历史通过记录不得冒充本次新执行；
本目录不更新任务状态，不证明全部PRD、实际试点或商用上线已通过。
