import { useCallback, useEffect, useState } from 'react'

import {
  fetchRoutingConfig,
  fetchRoutingStatus,
  fetchGatewayStatus,
  triggerReload,
  updateRoutingConfig,
} from '../api/client'
import type { GatewayStatus, RoutingConfig, RoutingStatus } from '../types'
import { useUnsavedChangesGuard } from '../hooks/useUnsavedChangesGuard'
import { profileToRoutingConfig, routingConfigToProfile } from './profiles/profileAdapters'
import ProfileForm from './profiles/ProfileForm'
import type { ForwardingProfile } from '../types/localServer'

export default function RoutingPanel() {
  const [cfg, setCfg] = useState<RoutingConfig | null>(null)
  const [profile, setProfile] = useState<ForwardingProfile | null>(null)
  const [status, setStatus] = useState<RoutingStatus | null>(null)
  const [gateway, setGateway] = useState<GatewayStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [needReload, setNeedReload] = useState(false)
  const [dirty, setDirty] = useState(false)
  const { confirmNavigation } = useUnsavedChangesGuard(dirty)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [routing, routingStatus, gatewayStatus] = await Promise.all([
        fetchRoutingConfig(),
        fetchRoutingStatus().catch(() => null),
        fetchGatewayStatus().catch(() => null),
      ])
      setCfg(routing)
      setProfile(routingConfigToProfile(routing))
      setStatus(routingStatus)
      setGateway(gatewayStatus)
      setDirty(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载分流配置失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void Promise.resolve().then(load)
  }, [load])

  useEffect(() => {
    const id = window.setInterval(() => {
      void fetchRoutingStatus().then(setStatus).catch(() => undefined)
      void fetchGatewayStatus().then(setGateway).catch(() => undefined)
    }, 5000)
    return () => window.clearInterval(id)
  }, [])

  const save = async () => {
    if (!cfg || !profile) return
    setSaving(true)
    setError('')
    setNotice('')
    try {
      const nextConfig = profileToRoutingConfig(profile, cfg)
      const result = await updateRoutingConfig(nextConfig)
      setCfg(nextConfig)
      setDirty(false)
      setNeedReload(result.need_reload)
      setNotice(result.message || '配置已保存')
      void fetchRoutingStatus().then(setStatus).catch(() => undefined)
      void fetchGatewayStatus().then(setGateway).catch(() => undefined)
    } catch (err) {
      setError(err instanceof Error ? err.message : '保存分流配置失败')
    } finally {
      setSaving(false)
    }
  }

  const reload = async () => {
    setSaving(true)
    setError('')
    try {
      await triggerReload()
      setNeedReload(false)
      setNotice('已触发重载，配置生效中')
      window.setTimeout(() => void load(), 1500)
    } catch (err) {
      setError(err instanceof Error ? err.message : '重载配置失败')
    } finally {
      setSaving(false)
    }
  }

  const closeDirty = () => confirmNavigation()

  if (loading) {
    return <div className="p-6 flex min-h-[60vh] items-center justify-center"><span className="loading loading-spinner loading-lg text-primary" /></div>
  }

  if (!profile || !cfg) {
    return <div className="p-6"><div role="alert" className="alert alert-error">{error || '加载分流配置失败'}</div></div>
  }

  return (
    <div className="p-4 lg:p-6 max-w-4xl mx-auto space-y-5">
      <div className="flex items-center justify-between flex-wrap gap-3">
        <div>
          <h1 className="text-2xl font-black tracking-tight">智能分流</h1>
          <p className="text-sm text-base-content/50 mt-0.5">共享 Profile 的规则、节点策略和属性筛选</p>
        </div>
        {status?.enabled ? (
          <span className="badge badge-success py-3">运行中 · {status.rule_count} 条规则</span>
        ) : (
          <span className="badge badge-ghost py-3">未启用</span>
        )}
      </div>

      {error && <div role="alert" className="alert alert-error text-sm">{error}</div>}
      {notice && (
        <div role="status" className="alert alert-success text-sm flex items-center justify-between gap-3">
          <span>{notice}</span>
          {needReload && <button type="button" className="btn btn-sm btn-warning" onClick={() => void reload()} disabled={saving}>点击重载</button>}
        </div>
      )}

      <ProfileForm
        value={profile}
        disabled={saving}
        onChange={(next) => {
          setProfile(next)
          setDirty(true)
          setNotice('')
        }}
      />

      {status?.enabled && (status.sticky_buckets || status.sticky_sessions) && (
        <div className="rounded-lg border border-base-300/50 bg-base-100 p-5 shadow-sm">
          <div className="font-bold mb-3">当前粘性绑定<span className="text-xs font-normal text-base-content/40 ml-2">（实时）</span></div>
          <div className="grid gap-4 text-xs font-mono md:grid-cols-2">
            <div>
              <div className="text-base-content/50 mb-1">stable 桶 → 节点</div>
              {Object.keys(status.sticky_buckets ?? {}).length === 0 && <div className="text-base-content/30 italic">（无）</div>}
              {Object.entries(status.sticky_buckets ?? {}).map(([key, node]) => (
                <div key={key} className="flex justify-between gap-2 py-0.5">
                  <span className="text-base-content/50 truncate">{key}</span>
                  <span className="text-primary">{node}</span>
                </div>
              ))}
            </div>
            <div>
              <div className="text-base-content/50 mb-1">session → 节点</div>
              {Object.keys(status.sticky_sessions ?? {}).length === 0 && <div className="text-base-content/30 italic">（无）</div>}
              {Object.entries(status.sticky_sessions ?? {}).map(([key, node]) => (
                <div key={key} className="flex justify-between gap-2 py-0.5">
                  <span className="text-base-content/50 truncate">{key}</span>
                  <span className="text-primary">{node}</span>
                </div>
              ))}
            </div>
          </div>
        </div>
      )}

      {gateway && (
        <div className="rounded-lg border border-base-300/50 bg-base-100 p-5 shadow-sm">
          <div className="flex items-center justify-between gap-3 mb-3">
            <div className="font-bold">局域网网关</div>
            <span className={`badge ${gateway.applied ? 'badge-success' : gateway.enabled ? 'badge-warning' : 'badge-ghost'}`}>
              {gateway.applied ? '运行中' : gateway.enabled ? '错误' : '未启用'}
            </span>
          </div>
          <div className="grid gap-3 text-sm md:grid-cols-4">
            <div><div className="text-base-content/50">模式</div><div className="font-mono">{gateway.mode === 'tun' ? 'Native TUN' : 'Transparent'}</div></div>
            <div><div className="text-base-content/50">接口 / 监听</div><div className="font-mono">{gateway.mode === 'tun' ? gateway.interface || '未就绪' : gateway.listen || '未配置'}</div></div>
            <div><div className="text-base-content/50">栈 / MTU</div><div>{gateway.mode === 'tun' ? `${gateway.stack || 'unknown'} / ${gateway.mtu || '-'}` : '-'}</div></div>
            <div><div className="text-base-content/50">活动连接</div><div>{gateway.active_connections ?? 0}</div></div>
            <div><div className="text-base-content/50">DIRECT / PROXY</div><div>{gateway.direct_connections ?? 0} / {gateway.proxy_connections ?? 0}</div></div>
            <div><div className="text-base-content/50">故障策略</div><div>{gateway.direct_fallbacks ?? 0} 次 DIRECT 回退</div></div>
          </div>
          {gateway.mode === 'tun' && (
            <div className="mt-4 flex flex-wrap gap-2 text-xs">
              <span className={`badge badge-sm ${gateway.tun_ready ? 'badge-success' : 'badge-error'}`}>TUN {gateway.tun_ready ? '就绪' : '未就绪'}</span>
              <span className={`badge badge-sm ${gateway.ipv4 ? 'badge-success' : 'badge-ghost'}`}>IPv4</span>
              <span className={`badge badge-sm ${gateway.ipv6 ? 'badge-success' : 'badge-ghost'}`}>IPv6</span>
              <span className={`badge badge-sm ${gateway.tcp ? 'badge-success' : 'badge-ghost'}`}>TCP</span>
              <span className={`badge badge-sm ${gateway.udp ? 'badge-success' : 'badge-ghost'}`}>UDP / QUIC</span>
              <span className={`badge badge-sm ${gateway.dns ? 'badge-success' : 'badge-ghost'}`}>DNS 劫持</span>
            </div>
          )}
          {gateway.last_error && <div className="text-xs text-error mt-3">{gateway.last_error}</div>}
        </div>
      )}

      <div className="sticky bottom-0 bg-base-200/80 backdrop-blur-md px-4 py-3 border-t border-base-300/40 flex items-center justify-end gap-3">
        <button type="button" className="btn btn-ghost btn-sm" onClick={() => { if (closeDirty()) void load() }} disabled={saving}>重置</button>
        <button type="button" className="btn btn-primary btn-sm" onClick={() => void save()} disabled={saving || !dirty}>
          {saving && <span className="loading loading-spinner loading-xs" />}
          保存配置
        </button>
      </div>
    </div>
  )
}
