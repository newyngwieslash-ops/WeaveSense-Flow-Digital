# DT-022 后台作业身份及范围中间层

最终冻结源码 `bc07d1c7`；原始报告记录的 `c8106dfc` 仅文档前移，1459源码
哈希已由作者、交付负责人、独立审查分别对固定Git对象核实一致。
实际 fresh PostgreSQL 17.11/PostGIS 3.5.2：7 pass、0 fail、0 skip、退出及关闭0。

命令：
`python scripts/agriculture/native-postgres-test.py --package-root .local/native-postgres-current/root --python .local/identity-rebuild/venv/bin/python --phase jobs --go .local/toolchains/go/bin/go`

真实Go授权与签名HTTP→Python验证→PG不可变快照与独立租约fixture→反向Go重新授权。
Go主测试及4个子项严格全部pass；平台使用SQLite，不称平台PG。验证篡改范围、资源
替换、撤权/撤回、attempt/fencing重试及真实数据库锁等待跨有效期后无成功记录。
worker只能读取快照、追加检查；没有领域全权或可复用系统凭据。

独立审查允许按原任务完成。本组件不创建W06队列，不执行真实影像，不代表生产
execution provider已接入。未来结果发布必须真实事务内原子fencing/CAS，不能用先验
成功检查掩盖发布竞态；缺provider保持503。不是remoteCI或完整AG-AC/W02/G0验收。
历史并发工作树报告保留在产品证据目录，不能替代此最终冻结证明。
