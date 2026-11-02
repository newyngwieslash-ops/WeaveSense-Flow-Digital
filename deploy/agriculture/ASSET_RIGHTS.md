# 资产目录权利配置

这是DT041的服务端配置，不代表已有实际客户数据许可。农业入口仍需显式选择 `app.agriculture.main:app`，旧GIS默认入口和readiness不是农业业务API。

- `AGRI_ASSET_RIGHTS_REGISTRY`：运营方已审W00 source-rights JSON登记文件。
- `AGRI_ASSET_SOURCE_SCOPES`：逐source_id映射的授权范围JSON，格式如下；每个父来源也必须显式映射。
- `AGRI_ASSET_RIGHTS_CONTRACT_ROOT`：本项目固定 `contracts/agriculture` 的只读挂载目录，内含 `source-rights.py` 和 `source-rights.schema.json`。源码运行默认使用仓库原路径。loader验证两文件固定release SHA，不接受任意替代Python模块。

```json
{
  "operator-reviewed-source-id": [
    {
      "tenant_id": "actual-platform-tenant-id",
      "workspace_id": "actual-platform-workspace-id",
      "source_kind": "customer_data"
    }
  ]
}
```

上例只是格式，不能作为真实授权证据。source_kind为customer_data/public_imagery/third_party；不支持通配tenant/workspace，不因为公共来源而默认所有租户可用。

两份JSON必须同时配置，使用非symlink普通文件、禁止group/world可写、有界2MiB，重复键拒绝。容器应只读bind mount并设置 `create_host_path: false`；不要将真实授权附件、用户数据、凭据或registry提交到Git。原GIS Docker镜像不会自动包含仓库外contracts，本说明不声称其已部署。

未配置两份JSON时允许metadata登记，但权利保持pending，正式使用/分发失败关闭。配置错误启动失败，不回退成approved。每次用途检查重新加载registry/scopes及核对contract，当前撤回/过期不被旧快照覆盖。替换registry应原子发布已审核文件；临时读取错误只会拒绝授权。

客户端rights_declaration不是批准；asset ready还需要后续DT042可信实体发布。此处不提供上传绕过、许可审批按钮、公开对象链接或真实付费来源接入。
