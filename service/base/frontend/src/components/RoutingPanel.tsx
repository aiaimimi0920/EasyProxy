import { useState, useEffect, useCallback } from 'react'
import {
  fetchRoutingConfig,
  updateRoutingConfig,
  fetchRoutingStatus,
  triggerReload,
} from '../api/client'
import type { RoutingConfig, RoutingStatus, RoutingProviderConfig } from '../types'

const STRATEGIES = [
  { value: 'stable', label: 'stable（稳定 · 防封号）', desc: '同类流量共用一个长效节点，节点挂了自动提升下一个' },
  { value: 'session', label: 'session（会话粘性 · 爬虫）', desc: '按会话 key（或来源 IP）保持同一出口，TTL 空闲过期' },
  { value: 'auto', label: 'auto（健康调度）', desc: '沿用连接池原有的健康度调度' },
]

const FINAL_POLICIES = [
  { value: 'PROXY', label: 'PROXY（默认走代理）' },
  { value: 'DIRECT', label: 'DIRECT（默认直连）' },
]

const emptyProvider = (): RoutingProviderConfig => ({ url: '', policy: 'DIRECT', behavior: 'domain', interval: '24h' })

export default function RoutingPanel() {
  const [cfg, setCfg] = useState<RoutingConfig | null>(null)
  const [status, setStatus] = useState<RoutingStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [needReload, setNeedReload] = useState(false)
  const [rulesText, setRulesText] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [c, s] = await Promise.all([fetchRoutingConfig(), fetchRoutingStatus().catch(() => null)])
      setCfg(c)
      setRulesText((c.rules || []).join('\n'))
      setStatus(s)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { load() }, [load])

  // Refresh live status periodically so sticky bindings/rule count stay current.
  useEffect(() => {
    const id = setInterval(() => {
      fetchRoutingStatus().then(setStatus).catch(() => {})
    }, 5000)
    return () => clearInterval(id)
  }, [])

  const patch = (p: Partial<RoutingConfig>) => setCfg((c) => (c ? { ...c, ...p } : c))

  const handleSave = async () => {
    if (!cfg) return
    setSaving(true)
    setError(null)
    setNotice(null)
    try {
      const rules = rulesText.split('\n').map((l) => l.trim()).filter((l) => l.length > 0)
      const payload: RoutingConfig = { ...cfg, rules }
      const res = await updateRoutingConfig(payload)
      setNotice(res.message || '已保存')
      setNeedReload(res.need_reload)
      // Refresh status to reflect hot-applied changes.
      fetchRoutingStatus().then(setStatus).catch(() => {})
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  const handleReload = async () => {
    setSaving(true)
    try {
      await triggerReload()
      setNotice('已触发重载，配置生效中…')
      setNeedReload(false)
      setTimeout(load, 1500)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  if (loading) {
    return (
      <div className="p-6 flex items-center justify-center min-h-[60vh]">
        <span className="loading loading-spinner loading-lg text-primary"></span>
      </div>
    )
  }

  if (!cfg) {
    return (
      <div className="p-6">
        <div className="alert alert-error">
          <span>加载分流配置失败：{error || '未知错误'}</span>
        </div>
      </div>
    )
  }

  const providers = cfg.rule_providers || []

  return (
    <div className="p-4 lg:p-6 max-w-4xl mx-auto space-y-5">
      {/* Header */}
      <div className="flex items-center justify-between flex-wrap gap-3">
        <div>
          <h1 className="text-2xl font-black tracking-tight">智能分流</h1>
          <p className="text-sm text-base-content/50 mt-0.5">
            规则分流（直连 / 代理）+ 选节点策略 + 属性筛选，接管默认代理入口
          </p>
        </div>
        <div className="flex items-center gap-2">
          {status?.enabled ? (
            <span className="badge badge-success gap-1.5 py-3">
              <span className="w-2 h-2 rounded-full bg-current"></span>运行中 · {status.rule_count} 条规则
            </span>
          ) : (
            <span className="badge badge-ghost py-3">未启用</span>
          )}
        </div>
      </div>

      {error && <div className="alert alert-error text-sm"><span>{error}</span></div>}
      {notice && (
        <div className="alert alert-success text-sm flex items-center justify-between">
          <span>{notice}</span>
          {needReload && (
            <button className="btn btn-sm btn-warning" onClick={handleReload} disabled={saving}>
              需要重载才能完全生效 · 点击重载
            </button>
          )}
        </div>
      )}

      {/* Master switch */}
      <div className="card bg-base-100 border border-base-300/50 shadow-sm">
        <div className="card-body p-5">
          <label className="flex items-center justify-between cursor-pointer">
            <div>
              <div className="font-bold">启用智能分流入口</div>
              <div className="text-xs text-base-content/50 mt-0.5">
                关闭时，代理入口保持原有连接池行为（不分流、不做策略选节点）。开关或监听地址变更需重载生效。
              </div>
            </div>
            <input
              type="checkbox"
              className="toggle toggle-primary"
              checked={cfg.enabled}
              onChange={(e) => patch({ enabled: e.target.checked })}
            />
          </label>
        </div>
      </div>

      {/* Strategy + final policy */}
      <div className="grid md:grid-cols-2 gap-4">
        <div className="card bg-base-100 border border-base-300/50 shadow-sm">
          <div className="card-body p-5">
            <div className="font-bold mb-1">默认选节点策略</div>
            <div className="text-xs text-base-content/50 mb-3">系统代理无参数访问时的策略</div>
            <select
              className="select select-bordered w-full"
              value={cfg.default_strategy || 'stable'}
              onChange={(e) => patch({ default_strategy: e.target.value })}
            >
              {STRATEGIES.map((s) => <option key={s.value} value={s.value}>{s.label}</option>)}
            </select>
            <div className="text-xs text-base-content/40 mt-2">
              {STRATEGIES.find((s) => s.value === (cfg.default_strategy || 'stable'))?.desc}
            </div>
          </div>
        </div>

        <div className="card bg-base-100 border border-base-300/50 shadow-sm">
          <div className="card-body p-5">
            <div className="font-bold mb-1">兜底策略 (FINAL)</div>
            <div className="text-xs text-base-content/50 mb-3">所有规则都未命中时的去向</div>
            <select
              className="select select-bordered w-full"
              value={cfg.final_policy || 'PROXY'}
              onChange={(e) => patch({ final_policy: e.target.value })}
            >
              {FINAL_POLICIES.map((p) => <option key={p.value} value={p.value}>{p.label}</option>)}
            </select>
            <label className="flex items-center gap-2 mt-3 cursor-pointer">
              <input
                type="checkbox"
                className="checkbox checkbox-sm checkbox-primary"
                checked={cfg.use_default_rules}
                onChange={(e) => patch({ use_default_rules: e.target.checked })}
              />
              <span className="text-sm">附加内置「中国直连」默认规则集</span>
            </label>
          </div>
        </div>
      </div>

      {/* Rules editor */}
      <div className="card bg-base-100 border border-base-300/50 shadow-sm">
        <div className="card-body p-5">
          <div className="font-bold mb-1">分流规则</div>
          <div className="text-xs text-base-content/50 mb-3">
            每行一条，按顺序优先于默认集。格式：<code className="text-primary">TYPE,value,POLICY</code>
            ，如 <code>DOMAIN-SUFFIX,google.com,PROXY</code> / <code>IP-CIDR,10.0.0.0/8,DIRECT</code> /
            <code> GEOIP,CN,DIRECT</code>。POLICY = DIRECT | PROXY。
          </div>
          <textarea
            className="textarea textarea-bordered w-full font-mono text-xs leading-relaxed"
            rows={10}
            spellCheck={false}
            value={rulesText}
            onChange={(e) => setRulesText(e.target.value)}
            placeholder={'DOMAIN-SUFFIX,example.com,PROXY\nIP-CIDR,192.168.0.0/16,DIRECT\nGEOIP,CN,DIRECT'}
          />
        </div>
      </div>

      {/* Rule providers */}
      <div className="card bg-base-100 border border-base-300/50 shadow-sm">
        <div className="card-body p-5">
          <div className="flex items-center justify-between mb-1">
            <div className="font-bold">远程规则订阅</div>
            <button
              className="btn btn-xs btn-ghost"
              onClick={() => patch({ rule_providers: [...providers, emptyProvider()] })}
            >
              + 添加
            </button>
          </div>
          <div className="text-xs text-base-content/50 mb-3">
            定时拉取的规则列表（domain 列表或 classical 规则），统一套用一个策略。
          </div>
          {providers.length === 0 && (
            <div className="text-xs text-base-content/40 italic py-2">暂无订阅</div>
          )}
          <div className="space-y-2">
            {providers.map((p, i) => (
              <div key={i} className="flex flex-wrap items-center gap-2 p-2 rounded-lg bg-base-200/40">
                <input
                  className="input input-bordered input-sm flex-1 min-w-[200px] font-mono text-xs"
                  placeholder="https://.../rules.txt"
                  value={p.url}
                  onChange={(e) => {
                    const next = [...providers]; next[i] = { ...p, url: e.target.value }; patch({ rule_providers: next })
                  }}
                />
                <select
                  className="select select-bordered select-sm"
                  value={p.policy}
                  onChange={(e) => { const next = [...providers]; next[i] = { ...p, policy: e.target.value }; patch({ rule_providers: next }) }}
                >
                  <option value="DIRECT">DIRECT</option>
                  <option value="PROXY">PROXY</option>
                </select>
                <select
                  className="select select-bordered select-sm"
                  value={p.behavior}
                  onChange={(e) => { const next = [...providers]; next[i] = { ...p, behavior: e.target.value }; patch({ rule_providers: next }) }}
                >
                  <option value="domain">domain</option>
                  <option value="classical">classical</option>
                </select>
                <input
                  className="input input-bordered input-sm w-20 font-mono text-xs"
                  placeholder="24h"
                  value={p.interval}
                  onChange={(e) => { const next = [...providers]; next[i] = { ...p, interval: e.target.value }; patch({ rule_providers: next }) }}
                />
                <button
                  className="btn btn-sm btn-ghost btn-square text-error"
                  onClick={() => patch({ rule_providers: providers.filter((_, j) => j !== i) })}
                  title="删除"
                >
                  ✕
                </button>
              </div>
            ))}
          </div>
        </div>
      </div>

      {/* Advanced thresholds */}
      <div className="card bg-base-100 border border-base-300/50 shadow-sm">
        <div className="card-body p-5">
          <div className="font-bold mb-3">高级阈值</div>
          <div className="grid sm:grid-cols-3 gap-4">
            <div>
              <label className="text-xs text-base-content/60">长效判定最短在线时长<span className="ml-1 text-success">实时</span></label>
              <input
                className="input input-bordered input-sm w-full mt-1 font-mono"
                placeholder="2h"
                value={cfg.long_lived_min_uptime}
                onChange={(e) => patch({ long_lived_min_uptime: e.target.value })}
              />
            </div>
            <div>
              <label className="text-xs text-base-content/60">长效判定最低成功率<span className="ml-1 text-success">实时</span></label>
              <input
                type="number" step="0.01" min="0" max="1"
                className="input input-bordered input-sm w-full mt-1 font-mono"
                placeholder="0.9"
                value={cfg.long_lived_min_success_rate}
                onChange={(e) => patch({ long_lived_min_success_rate: parseFloat(e.target.value) || 0 })}
              />
            </div>
            <div>
              <label className="text-xs text-base-content/60">会话粘性 TTL<span className="ml-1 text-warning">需重载</span></label>
              <input
                className="input input-bordered input-sm w-full mt-1 font-mono"
                placeholder="10m"
                value={cfg.session_ttl}
                onChange={(e) => patch({ session_ttl: e.target.value })}
              />
            </div>
          </div>
        </div>
      </div>

      {/* Live sticky bindings */}
      {status?.enabled && (status.sticky_buckets || status.sticky_sessions) && (
        <div className="card bg-base-100 border border-base-300/50 shadow-sm">
          <div className="card-body p-5">
            <div className="font-bold mb-3">当前粘性绑定<span className="text-xs font-normal text-base-content/40 ml-2">（实时）</span></div>
            <div className="grid sm:grid-cols-2 gap-4 text-xs font-mono">
              <div>
                <div className="text-base-content/50 mb-1">stable 桶 → 节点</div>
                {Object.keys(status.sticky_buckets || {}).length === 0 && <div className="text-base-content/30 italic">（无）</div>}
                {Object.entries(status.sticky_buckets || {}).map(([k, v]) => (
                  <div key={k} className="flex justify-between gap-2 py-0.5">
                    <span className="text-base-content/50 truncate">{k.slice(0, 12)}…</span>
                    <span className="text-primary">{v}</span>
                  </div>
                ))}
              </div>
              <div>
                <div className="text-base-content/50 mb-1">session → 节点</div>
                {Object.keys(status.sticky_sessions || {}).length === 0 && <div className="text-base-content/30 italic">（无）</div>}
                {Object.entries(status.sticky_sessions || {}).map(([k, v]) => (
                  <div key={k} className="flex justify-between gap-2 py-0.5">
                    <span className="text-base-content/50 truncate">{k}</span>
                    <span className="text-primary">{v}</span>
                  </div>
                ))}
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Save bar */}
      <div className="sticky bottom-0 bg-base-200/80 backdrop-blur-md -mx-4 lg:-mx-6 px-4 lg:px-6 py-3 border-t border-base-300/40 flex items-center justify-end gap-3">
        <button className="btn btn-ghost btn-sm" onClick={load} disabled={saving}>重置</button>
        <button className="btn btn-primary btn-sm" onClick={handleSave} disabled={saving}>
          {saving && <span className="loading loading-spinner loading-xs"></span>}
          保存配置
        </button>
      </div>
    </div>
  )
}
