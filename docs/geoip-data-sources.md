# GeoIP 数据来源与署名

## 可选官方 DB-IP Country Lite

- 来源：https://db-ip.com/db/download/ip-to-country-lite
- 2026-11 官方下载：https://download.db-ip.com/free/dbip-country-lite-2026-11.mmdb.gz
- 许可：Creative Commons Attribution 4.0 International，https://creativecommons.org/licenses/by/4.0/
- 数据作者：DB-IP，https://db-ip.com/
- 格式说明：https://db-ip.com/db/format/ip-to-country-lite/mmdb.html

免费数据降低覆盖与准确度、每月更新。生产国家策略只采用节点服务器地址的国家，不代表实测公网出口。
本项目不附带该数据库，不把第三方镜像的名称当作官方来源证明；操作者需从官方地址取得文件。
应用使用该数据须署名，显示或使用结果的 web 页面须含 DB-IP 回链。管理界面通用页脚提供
“IP Geolocation by DB-IP”和 CC BY 4.0 链接，注明仅配置 DB-IP 时适用。
数据库原内容未修改，仅解压 gzip。随部署数据保留本文件或等价 NOTICE。

## 2026-11-11 准备验证记录（未部署）

- gzip：4,205,904 字节；SHA-256 `75fe430010f6d38a8c4331c202bf0df6ce3221a6e6d823b9e5a6d951e4cf9758`
- MMDB：8,515,063 字节；SHA-256 `145ce5dfb812d8de28547e6289bb5c5f8b3df2f1dea21d324d5b575e0431311f`
- 数据类型 `DBIP-Country-Lite`，1,410,860 nodes，IPVersion 6，BuildEpoch 1793501259。
- 使用本项目实际 `geoip.New` / `LookupIP` reader 验证 IPv4、IPv6，返回有效 ISO 国家码和英文名。
- 样例返回：8.8.8.8→US，1.1.1.1→AU，114.114.114.114→CN，2001:4860:4860::8888→CA。
  这只是所下载数据库的记录，不是出口位置测量或互联网节点实际所在地保证。

不得把测试 MMDB 或仅含中国记录的第三方 Country.mmdb 用作全球国家策略数据库。
启用 GeoIP、指向下载文件和重载生产均须走单独确认与现有回滚步骤。
如果自动更新仍使用旧第三方默认地址，必须保持关闭，避免后来悄悄替换已确认数据来源。
