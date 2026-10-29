# DT-006 合成黄金样本恢复

本批为工作区恢复后**新构造**的测试数据，不声称与丢失的旧 W00 样本字节一致。生成器、两期真实 GeoTIFF、小规模坐标数据与 SHA256 清单位于 `tests/agriculture/fixtures/golden/`。数据及生成代码由本项目自编，按 MIT 提供，没有下载第三方影像、没有客户数据、没有启用外部来源。

清单分别登记 `synthetic`、`public_real` 和 `customer_operational`。后两组为 `not_provided`，不得用本样本替代真实影像/农场/商业运行验收。两个租户拥有相同坐标和相同局部 parcel ID，用于后续隔离负例输入；本测试**本身不验证 API/RLS 租户隔离**。

## 人工真值

面积参考算法限定为本 fixture 的 `fixture-equal-area-shoelace-v1`，不是尚未实施的全产品面积服务。EPSG:6933 等积投影坐标单位米，矩形横向与纵向各差 100，所以面积 `100×100=10000 m²=1 ha=15 亩`。源投影坐标和显式经纬度源样本同时保留；对外 Polygon 均为 EPSG:4326、经度在前纬度在后。测试把对外坐标转换回已声明的等积 CRS，再用平移后的鞋带公式复算，容差 `0.01 m²`，绝不用经纬度平方度作为面积。缺少 CRS 的影像必须拒绝，不能猜测。

两期 GeoTIFF 每期 3×3 像元、10 m 分辨率，波段顺序 red/nir/quality。反射率计算 `DN×0.0001−0.1`；先过滤 nodata=-9999、cloud=1、shadow=2，再过滤零分母。质量波段 scale=1、offset=0，来源为自编掩膜。产品级别、观测时间、CRS、轴序、单位、scale/offset 和质量语义可检查。

第一期 (row=0,column=0)：red DN=2000→0.1，nir DN=6000→0.5，NDVI=`(0.5−0.1)/(0.5+0.1)=2/3`。忽略 offset 将错误得到 1/2，因此测试能检测该错误。第一期有效值依次 `2/3,1/3,1/2,0,3/4`，均值 `9/20=0.45`；第二期为 `1/2,1/2,1/3,1/4,1/2`，均值 `5/12`；均值变化 `−1/30`。掩膜余下四像元分别为 nodata、云、阴影、反射率和为零。数值容差 `1e−12`。

全云时参考结果为 `insufficient_data`、数值 null；不同格网或产品级别不比较，不能输出正常或确定的农学结论。测试中的参考计算只用于检查样本真值，**不冒充生产分析管线实现**。后续生产算法必须复用这些样本另行做契约/结果对比。

## 复现

在仓库根目录，使用已按锁文件安装的 GIS 依赖环境：

```bash
.local/agri-identity-venv/bin/python tests/agriculture/fixtures/golden/generate.py
.local/agri-identity-venv/bin/python -m pytest -q tests/agriculture/test_golden_fixtures.py
```

生成器不访问网络；SHA256 与字节数覆盖所有数据文件。测试在临时目录重新生成并逐字节比对，包括清单。确定性以清单记录的 Rasterio/GDAL 环境为界；升级工具链改变 TIFF 字节时须审查差异并新增证据，不能自动接受新摘要掩盖变化。
