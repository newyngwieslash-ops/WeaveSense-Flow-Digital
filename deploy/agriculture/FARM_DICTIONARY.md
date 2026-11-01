# 农场字典与时区部署边界

`AGRI_FARM_DICTIONARY_CONFIG` 是独立可信 GIS 入口
`app.agriculture.main:app` 使用的容器内绝对路径，不是浏览器参数。
现有 `docker-compose.agriculture.yml` 的 `geo-readiness` 仍只是 W00 依赖探针，
没有真实农场服务。给探针添加该变量不会启用农场，不能作为部署验收。
原 `services/agri-gis/backend/Dockerfile` 默认入口仍是旧 `app.main:app`，
禁止直接把它当成农业服务公开；启用农业部署必须显式覆盖为可信入口，
配置服务身份、普通 GIS 数据库角色并完成迁移与回调网络验收。

## 字典文件

模板 `farm-dictionary.template.json` 故意 `regions=[]`，加载时应失败，
不提供默认行政区、时区选择或伪造正式区域数据。运营方创建独立配置文件，
经审核后填写以下严格结构，不覆盖旧版本文件：

- 根字段仅 `schema_version`、`dictionary_version`、`regions`。
- schema_version 固定 `agriculture-farm-dictionary/1`。
- dictionary_version 为非空、最长64字符的 ASCII 字母数字/点/下划线/短横线版本。
- regions 为1至1000项，每项仅 code、name、timezones、area_units。
- code 使用同一版本编码规则且唯一；name 为1至128字符的非空名称。
- timezones 为1至32个不重复的有效 IANA 时区名；不得由机器默认时区猜测。
- area_units 为不重复的 `m2`、`ha`、`mu` 子集。

文件必须是普通文件、非符号链接、不超过1MiB，不能有重复 JSON 键。
配置中不放身份、角色、凭据或客户端可决定的 tenant_id。
区域代码只是部署字典选择，不证明边界位于某行政区，也不授权第三方数据。
版本变化后重启服务以加载新快照；旧版本历史数据保留原快照，
使用旧版本提交的新写请求会返回409，不能悄悄改成当前字典。

## 只读挂载

在已经配置并验证的真实 GIS 服务定义中加入如下片段；这不是完整可启动的
Compose 文件，也不要合并到 `geo-readiness`。宿主文件必须预先存在，
禁止 Docker 自动创建同名目录而掩盖配置错误。

```yaml
environment:
  AGRI_FARM_DICTIONARY_CONFIG: /run/agriculture/farm-dictionary.json
volumes:
  - type: bind
    source: ${AGRI_FARM_DICTIONARY_HOST_FILE:?configure reviewed existing dictionary file}
    target: /run/agriculture/farm-dictionary.json
    read_only: true
    bind:
      create_host_path: false
```

没有变量时农场依赖保持关闭，缺文件/格式错误时不得回退默认区域。
运行账户只需读取该配置，不给予写入挂载或迁移权限。

## IANA 时区数据库

原 GIS Poetry 锁文件的 `tzdata` 只覆盖 Windows，Linux 运行镜像必须拥有
系统 IANA 数据。后端 Dockerfile 在 runtime 层显式安装 `tzdata`，并在实际
运行 venv 下检查 `ZoneInfo('UTC')` 和 `ZoneInfo('Asia/Shanghai')`。
两个检查项只是验证数据库存在，不是默认区域或时区选择。
若使用其他镜像，须在该镜像中验证已配置的全部时区，缺失即阻断部署。

现有上游镜像标签/系统包尚非完整供应链 digest 固定，不能把上述修正当作
最终生产 SBOM 或容器启动验收；实际镜像构建、签名回调及农场浏览器闭环
须另行执行并记录。该配置说明不授权生产部署。
