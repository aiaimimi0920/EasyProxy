export default function GeoIPAttribution() {
  return (
    <footer className="px-4 py-3 text-center text-xs text-base-content/70" aria-label="GeoIP 数据许可">
      配置 DB-IP 数据库时：{' '}
      <a className="link" href="https://db-ip.com" target="_blank" rel="noopener noreferrer">IP Geolocation by DB-IP</a>
      {' · '}
      <a className="link" href="https://creativecommons.org/licenses/by/4.0/" target="_blank" rel="noopener noreferrer">CC BY 4.0</a>
      {' · 节点服务器地理位置，不代表实际出口位置'}
    </footer>
  )
}
