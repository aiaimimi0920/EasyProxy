package routerule

// DefaultRules is the built-in "China-direct, everything-else-proxy" rule set
// shipped as the factory default. It mirrors the common Clash/mihomo baseline:
//
//   - private / loopback / CGNAT ranges → DIRECT
//   - mainland-China GeoIP → DIRECT
//   - a curated list of well-known Chinese domains → DIRECT (so they resolve
//     direct even before any IP is known, avoiding pointless proxying)
//   - FINAL → PROXY
//
// Users may append their own rules via config; those are evaluated before this
// set so they can override individual entries. The FINAL policy here is the
// ultimate fallback when no rule matches.
func DefaultRules() []string {
	return []string{
		// ── Private / local networks → DIRECT ──
		"IP-CIDR,127.0.0.0/8,DIRECT",
		"IP-CIDR,10.0.0.0/8,DIRECT",
		"IP-CIDR,172.16.0.0/12,DIRECT",
		"IP-CIDR,192.168.0.0/16,DIRECT",
		"IP-CIDR,169.254.0.0/16,DIRECT",
		"IP-CIDR,100.64.0.0/10,DIRECT", // CGNAT
		"IP-CIDR6,::1/128,DIRECT",
		"IP-CIDR6,fc00::/7,DIRECT",
		"IP-CIDR6,fe80::/10,DIRECT",

		// ── Local / intranet domains → DIRECT ──
		"DOMAIN-SUFFIX,local,DIRECT",
		"DOMAIN-SUFFIX,localhost,DIRECT",
		"DOMAIN-SUFFIX,lan,DIRECT",

		// ── Common Chinese services → DIRECT ──
		"DOMAIN-SUFFIX,cn,DIRECT",
		"DOMAIN-KEYWORD,baidu,DIRECT",
		"DOMAIN-SUFFIX,baidu.com,DIRECT",
		"DOMAIN-SUFFIX,bdstatic.com,DIRECT",
		"DOMAIN-KEYWORD,qq,DIRECT",
		"DOMAIN-SUFFIX,qq.com,DIRECT",
		"DOMAIN-SUFFIX,tencent.com,DIRECT",
		"DOMAIN-SUFFIX,gtimg.com,DIRECT",
		"DOMAIN-KEYWORD,taobao,DIRECT",
		"DOMAIN-SUFFIX,taobao.com,DIRECT",
		"DOMAIN-SUFFIX,tmall.com,DIRECT",
		"DOMAIN-SUFFIX,alicdn.com,DIRECT",
		"DOMAIN-SUFFIX,aliyun.com,DIRECT",
		"DOMAIN-SUFFIX,alipay.com,DIRECT",
		"DOMAIN-KEYWORD,jd,DIRECT",
		"DOMAIN-SUFFIX,jd.com,DIRECT",
		"DOMAIN-SUFFIX,360buyimg.com,DIRECT",
		"DOMAIN-KEYWORD,bilibili,DIRECT",
		"DOMAIN-SUFFIX,bilibili.com,DIRECT",
		"DOMAIN-SUFFIX,hdslb.com,DIRECT",
		"DOMAIN-KEYWORD,weibo,DIRECT",
		"DOMAIN-SUFFIX,weibo.com,DIRECT",
		"DOMAIN-SUFFIX,sina.com.cn,DIRECT",
		"DOMAIN-KEYWORD,163,DIRECT",
		"DOMAIN-SUFFIX,163.com,DIRECT",
		"DOMAIN-SUFFIX,126.net,DIRECT",
		"DOMAIN-SUFFIX,netease.com,DIRECT",
		"DOMAIN-SUFFIX,music.163.com,DIRECT",
		"DOMAIN-KEYWORD,bytedance,DIRECT",
		"DOMAIN-SUFFIX,douyin.com,DIRECT",
		"DOMAIN-SUFFIX,douyincdn.com,DIRECT",
		"DOMAIN-SUFFIX,bytedance.com,DIRECT",
		"DOMAIN-SUFFIX,zhihu.com,DIRECT",
		"DOMAIN-SUFFIX,zhimg.com,DIRECT",
		"DOMAIN-SUFFIX,meituan.com,DIRECT",
		"DOMAIN-SUFFIX,meituan.net,DIRECT",
		"DOMAIN-SUFFIX,xiaomi.com,DIRECT",
		"DOMAIN-SUFFIX,mi.com,DIRECT",
		"DOMAIN-SUFFIX,gov.cn,DIRECT",
		"DOMAIN-SUFFIX,edu.cn,DIRECT",

		// ── Mainland China GeoIP → DIRECT (literal-IP destinations only) ──
		"GEOIP,CN,DIRECT",

		// ── Everything else → PROXY ──
		"FINAL,PROXY",
	}
}
