import { useState, useEffect, useCallback } from 'react'
import type { SettingsData, SourceSyncStatus, SubscriptionStatus } from '../types'
import { fetchSettings, updateSettings, triggerReload, fetchSubscriptionStatus, fetchSourceSyncStatus, refreshSubscription } from '../api/client'
import LocalServerSettingsCard from './local-server/LocalServerSettingsCard'
import SettingsGeneralCards from './SettingsGeneralCards'
import SettingsSourceCards from './SettingsSourceCards'

const defaultSettings: SettingsData = {
  mode: 'pool',
  log_level: 'info',
  external_ip: '',
  skip_cert_verify: false,

  listener_address: '0.0.0.0',
  listener_port: 22323,
  listener_protocol: 'http',
  listener_username: '',
  listener_password: '',

  multi_port_address: '0.0.0.0',
  multi_port_base_port: 25000,
  multi_port_protocol: 'http',
  multi_port_username: '',
  multi_port_password: '',

  pool_mode: 'auto',
  pool_failure_threshold: 3,
  pool_blacklist_duration: '24h0m0s',

  management_enabled: true,
  management_listen: '0.0.0.0:29888',
  management_probe_target: '',
  management_password: '',
  management_health_check_interval: '2h0m0s',

  sub_refresh_enabled: false,
  sub_refresh_interval: '24h0m0s',
  sub_refresh_timeout: '30s',
  sub_refresh_health_check_timeout: '2m0s',
  sub_refresh_drain_timeout: '30s',
  sub_refresh_min_available_nodes: 1,

  source_sync_enabled: false,
  source_sync_manifest_url: '',
  source_sync_manifest_token: '',
  source_sync_refresh_interval: '1h0m0s',
  source_sync_request_timeout: '15s',
  source_sync_fallback_subscriptions: [],
  source_sync_default_direct_proxy_scheme: 'http',

  geoip_enabled: true,
  geoip_database_path: './GeoLite2-Country.mmdb',
  geoip_auto_update_enabled: true,
  geoip_auto_update_interval: '24h0m0s',

  subscriptions: [],
}

export default function SettingsPanel() {
  const [settings, setSettings] = useState<SettingsData>(defaultSettings)
  const [savedSettings, setSavedSettings] = useState<SettingsData>(defaultSettings)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [reloading, setReloading] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [needReload, setNeedReload] = useState(false)
  const [isDirty, setIsDirty] = useState(false)

  // Subscription status
  const [subStatus, setSubStatus] = useState<SubscriptionStatus | null>(null)
  const [sourceSyncStatus, setSourceSyncStatus] = useState<SourceSyncStatus | null>(null)
  const [subRefreshing, setSubRefreshing] = useState(false)

  // New subscription input
  const [newSubUrl, setNewSubUrl] = useState('')
  const [newFallbackUrl, setNewFallbackUrl] = useState('')
  const localServerMode = settings.local_server_enabled === true

  const refreshRuntimeStatus = useCallback(async () => {
    try {
      const [subData, sourceData] = await Promise.all([
        fetchSubscriptionStatus(),
        fetchSourceSyncStatus(),
      ])
      if (subData) setSubStatus(subData)
      if (sourceData) setSourceSyncStatus(sourceData)
    } catch {
      // ignore errors
    }
  }, [])

  useEffect(() => {
    const load = async () => {
      try {
        const [settingsData] = await Promise.all([
          fetchSettings(),
          refreshRuntimeStatus(),
        ])
        const subscriptions = settingsData.subscriptions || []
        const fallbackSubscriptions = settingsData.source_sync_fallback_subscriptions || []
        const merged = { ...defaultSettings, ...settingsData, subscriptions, source_sync_fallback_subscriptions: fallbackSubscriptions }
        setSettings(merged)
        setSavedSettings(merged)
        setIsDirty(false)
      } catch (err) {
        setError(err instanceof Error ? err.message : '加载设置失败')
      } finally {
        setLoading(false)
      }
    }
    load()
  }, [refreshRuntimeStatus])

  useEffect(() => {
    if (success) {
      const timer = setTimeout(() => setSuccess(''), 5000)
      return () => clearTimeout(timer)
    }
  }, [success])

  const handleSave = async () => {
    setSaving(true)
    setError('')
    setSuccess('')
    try {
      const res = await updateSettings(settings)
      setSuccess(res.message || '设置已保存')
      setSavedSettings({ ...settings })
      setIsDirty(false)
      if (res.need_reload) setNeedReload(true)
      // Refresh subscription status after saving (config may have changed)
      await refreshRuntimeStatus()
    } catch (err) {
      setError(err instanceof Error ? err.message : '保存失败')
    } finally {
      setSaving(false)
    }
  }

  const handleReload = async () => {
    setReloading(true)
    setError('')
    try {
      const res = await triggerReload()
      setSuccess(res.message || '重载成功')
      setNeedReload(false)
      // Refresh subscription status after reload (subscription manager config updated)
      await refreshRuntimeStatus()
    } catch (err) {
      setError(err instanceof Error ? err.message : '重载失败')
    } finally {
      setReloading(false)
    }
  }

  const handleSubRefresh = async () => {
    setSubRefreshing(true)
    setError('')
    try {
      const res = await refreshSubscription()
      setSuccess(`订阅刷新成功，共 ${res.node_count} 个节点`)
      await refreshRuntimeStatus()
    } catch (err) {
      setError(err instanceof Error ? err.message : '刷新订阅失败')
    } finally {
      setSubRefreshing(false)
    }
  }

  const addSubscription = () => {
    const url = newSubUrl.trim()
    if (!url) return
    if (settings.subscriptions.includes(url)) {
      setError('该订阅地址已存在')
      return
    }
    setSettings(s => {
      const updated = { ...s, subscriptions: [...s.subscriptions, url] }
      setIsDirty(JSON.stringify(updated) !== JSON.stringify(savedSettings))
      return updated
    })
    setNewSubUrl('')
  }

  const removeSubscription = (index: number) => {
    setSettings(s => {
      const updated = { ...s, subscriptions: s.subscriptions.filter((_, i) => i !== index) }
      setIsDirty(JSON.stringify(updated) !== JSON.stringify(savedSettings))
      return updated
    })
  }

  const addFallbackSubscription = () => {
    const url = newFallbackUrl.trim()
    if (!url) return
    if (settings.source_sync_fallback_subscriptions.includes(url)) {
      setError('该回退订阅地址已存在')
      return
    }
    setSettings(s => {
      const updated = {
        ...s,
        source_sync_fallback_subscriptions: [...s.source_sync_fallback_subscriptions, url],
      }
      setIsDirty(JSON.stringify(updated) !== JSON.stringify(savedSettings))
      return updated
    })
    setNewFallbackUrl('')
  }

  const removeFallbackSubscription = (index: number) => {
    setSettings(s => {
      const updated = {
        ...s,
        source_sync_fallback_subscriptions: s.source_sync_fallback_subscriptions.filter((_, i) => i !== index),
      }
      setIsDirty(JSON.stringify(updated) !== JSON.stringify(savedSettings))
      return updated
    })
  }

  const updateField = <K extends keyof SettingsData>(key: K, value: SettingsData[K]) => {
    setSettings(s => {
      const updated = { ...s, [key]: value }
      setIsDirty(JSON.stringify(updated) !== JSON.stringify(savedSettings))
      return updated
    })
  }

  // Warn before leaving with unsaved changes
  useEffect(() => {
    const handleBeforeUnload = (e: BeforeUnloadEvent) => {
      if (isDirty) {
        e.preventDefault()
        e.returnValue = ''
      }
    }
    window.addEventListener('beforeunload', handleBeforeUnload)
    return () => window.removeEventListener('beforeunload', handleBeforeUnload)
  }, [isDirty])

  if (loading) {
    return (
      <div className="flex items-center justify-center h-64">
        <span className="loading loading-spinner loading-lg text-primary"></span>
      </div>
    )
  }

  return (
    <div className="flex flex-col min-h-full animate-in fade-in duration-500">
      {/* Header - sticky */}
      <div className="sticky top-0 z-30 bg-base-100/80 backdrop-blur-xl px-4 lg:px-8 py-4 border-b border-base-300/60 shadow-sm">
        <div className="flex flex-col md:flex-row justify-between items-start md:items-center gap-4 max-w-[1200px] mx-auto w-full">
          <div>
            <h2 className="text-2xl font-bold flex items-center gap-3">
              <div className="w-10 h-10 rounded-xl bg-primary/10 flex items-center justify-center text-primary shrink-0 border border-primary/20">
                <svg xmlns="http://www.w3.org/2000/svg" className="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2.5" d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z" />
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2.5" d="M15 12a3 3 0 11-6 0 3 3 0 016 0z" />
                </svg>
              </div>
              系统设置
            </h2>
            <p className="text-sm text-base-content/50 mt-1.5 ml-[3.25rem]">管理系统所有配置项，修改后需保存生效</p>
          </div>
          <div className="flex gap-2">
            <button
              className={`btn btn-sm lg:btn-md gap-2 shadow-sm ${isDirty ? 'btn-primary' : 'btn-ghost border border-base-300'}`}
              onClick={handleSave}
              disabled={saving || !isDirty}
            >
              {saving ? <span className="loading loading-spinner loading-sm"></span> : isDirty ? (
                <>
                  <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M8 7H5a2 2 0 00-2 2v9a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-3m-1 4l-3 3m0 0l-3-3m3 3V4" />
                  </svg>
                  保存设置
                </>
              ) : '✅ 已保存'}
            </button>
            {needReload && (
              <button
                className="btn btn-warning btn-sm lg:btn-md gap-2 shadow-sm animate-pulse"
                onClick={handleReload}
                disabled={reloading}
              >
                {reloading ? <span className="loading loading-spinner loading-sm"></span> : (
                  <>
                    <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                      <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
                    </svg>
                    重载配置
                  </>
                )}
              </button>
            )}
          </div>
        </div>
      </div>

      <div className="p-4 lg:p-8 space-y-6 max-w-[1200px] mx-auto w-full pb-10 flex-1">
        <LocalServerSettingsCard
          onModeChange={(enabled) => setSettings((current) => ({ ...current, local_server_enabled: enabled }))}
        />

        {/* Alerts */}
        {error && (
        <div role="alert" className="alert alert-error alert-soft text-sm">
          <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M10 14l2-2m0 0l2-2m-2 2l-2-2m2 2l2 2m7-2a9 9 0 11-18 0 9 9 0 0118 0z" />
          </svg>
          <span>{error}</span>
        </div>
      )}
      {success && (
        <div role="alert" className="alert alert-success alert-soft text-sm">
          <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z" />
          </svg>
          <span>{success}</span>
        </div>
      )}
      {needReload && (
        <div role="alert" className="alert alert-warning alert-soft text-sm">
          <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-2.5L13.732 4c-.77-.833-1.964-.833-2.732 0L4.082 16.5c-.77.833.192 2.5 1.732 2.5z" />
          </svg>
          <div>
            <span>配置已保存。</span>
            <span className="font-medium">WebUI 密码、探测目标、外部 IP、SSL 验证</span>
            <span>已立即生效；其他配置（运行模式、监听端口、代理池等）需要点击「重载配置」才能生效。</span>
          </div>
        </div>
      )}

      {/* Settings Cards Grid */}
      <div className="grid gap-5 lg:grid-cols-2">

        <SettingsGeneralCards settings={settings} updateField={updateField} localServerMode={localServerMode} />
        <SettingsSourceCards
          settings={settings}
          updateField={updateField}
          sourceSyncStatus={sourceSyncStatus}
          subStatus={subStatus}
          subRefreshing={subRefreshing}
          isDirty={isDirty}
          newFallbackUrl={newFallbackUrl}
          setNewFallbackUrl={setNewFallbackUrl}
          addFallbackSubscription={addFallbackSubscription}
          removeFallbackSubscription={removeFallbackSubscription}
          newSubUrl={newSubUrl}
          setNewSubUrl={setNewSubUrl}
          addSubscription={addSubscription}
          removeSubscription={removeSubscription}
          handleSubRefresh={handleSubRefresh}
        />
      </div>

      </div>

    </div>
  )
}
