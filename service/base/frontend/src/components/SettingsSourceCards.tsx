import type { SettingsData, SourceSyncStatus, SubscriptionStatus } from '../types'
import type { UpdateSettingsField } from './SettingsGeneralCards'

interface SettingsSourceCardsProps {
  settings: SettingsData
  updateField: UpdateSettingsField
  sourceSyncStatus: SourceSyncStatus | null
  subStatus: SubscriptionStatus | null
  subRefreshing: boolean
  isDirty: boolean
  newFallbackUrl: string
  setNewFallbackUrl: (value: string) => void
  addFallbackSubscription: () => void
  removeFallbackSubscription: (index: number) => void
  newSubUrl: string
  setNewSubUrl: (value: string) => void
  addSubscription: () => void
  removeSubscription: (index: number) => void
  handleSubRefresh: () => void
}

export default function SettingsSourceCards(props: SettingsSourceCardsProps) {
  const {
    settings, updateField, sourceSyncStatus, subStatus, subRefreshing, isDirty,
    newFallbackUrl, setNewFallbackUrl, addFallbackSubscription, removeFallbackSubscription,
    newSubUrl, setNewSubUrl, addSubscription, removeSubscription, handleSubRefresh,
  } = props
  return (
    <>
      {/* ===== 订阅刷新 ===== */}
      <div className="rounded-2xl border border-base-300/50 bg-base-100 p-6 lg:p-8 space-y-5 shadow-sm transition-shadow hover:shadow-md">
        <div className="flex items-center gap-3 mb-2 border-b border-base-200 pb-4">
          <div className="w-10 h-10 rounded-xl bg-warning/10 flex items-center justify-center text-warning shrink-0">
            <svg xmlns="http://www.w3.org/2000/svg" className="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
            </svg>
          </div>
          <div>
            <h3 className="font-bold text-lg text-base-content">订阅自动刷新</h3>
            <p className="text-xs text-base-content/50 font-medium">配置订阅定时更新及健康检查</p>
          </div>
        </div>

        <label className="flex items-center justify-between cursor-pointer gap-4 bg-base-200/30 p-4 rounded-xl border border-base-200 hover:border-base-300 transition-colors">
          <span className="font-semibold text-base-content/90">启用定时刷新</span>
          <input
            type="checkbox"
            className="toggle toggle-primary toggle-md"
            checked={settings.sub_refresh_enabled}
            onChange={(e) => updateField('sub_refresh_enabled', e.target.checked)}
          />
        </label>

        {settings.sub_refresh_enabled && (
          <div className="space-y-4 pt-2 animate-in fade-in slide-in-from-top-2">
            <div className="grid grid-cols-2 gap-4">
              <fieldset className="fieldset">
                <legend className="fieldset-legend font-semibold text-base-content/80">刷新间隔</legend>
                <input
                  type="text"
                  className="input input-md w-full bg-base-200/50 focus:bg-base-100 transition-colors focus:border-primary/50"
                  placeholder="1h"
                  value={settings.sub_refresh_interval}
                  onChange={(e) => updateField('sub_refresh_interval', e.target.value)}
                />
              </fieldset>
              <fieldset className="fieldset">
                <legend className="fieldset-legend font-semibold text-base-content/80">获取超时</legend>
                <input
                  type="text"
                  className="input input-md w-full bg-base-200/50 focus:bg-base-100 transition-colors focus:border-primary/50"
                  placeholder="30s"
                  value={settings.sub_refresh_timeout}
                  onChange={(e) => updateField('sub_refresh_timeout', e.target.value)}
                />
              </fieldset>
            </div>

            <div className="grid grid-cols-2 gap-4">
              <fieldset className="fieldset">
                <legend className="fieldset-legend font-semibold text-base-content/80">健康检查超时</legend>
                <input
                  type="text"
                  className="input input-md w-full bg-base-200/50 focus:bg-base-100 transition-colors focus:border-primary/50"
                  placeholder="1m"
                  value={settings.sub_refresh_health_check_timeout}
                  onChange={(e) => updateField('sub_refresh_health_check_timeout', e.target.value)}
                />
              </fieldset>
              <fieldset className="fieldset">
                <legend className="fieldset-legend font-semibold text-base-content/80">排空超时</legend>
                <input
                  type="text"
                  className="input input-md w-full bg-base-200/50 focus:bg-base-100 transition-colors focus:border-primary/50"
                  placeholder="30s"
                  value={settings.sub_refresh_drain_timeout}
                  onChange={(e) => updateField('sub_refresh_drain_timeout', e.target.value)}
                />
              </fieldset>
            </div>

            <fieldset className="fieldset">
              <legend className="fieldset-legend font-semibold text-base-content/80">最少可用节点数</legend>
              <input
                type="number"
                className="input input-md w-full bg-base-200/50 focus:bg-base-100 transition-colors focus:border-primary/50"
                value={settings.sub_refresh_min_available_nodes}
                onChange={(e) => updateField('sub_refresh_min_available_nodes', parseInt(e.target.value) || 1)}
                min={0}
              />
              <p className="label text-base-content/50 mt-1">低于此值时不切换新节点</p>
            </fieldset>
          </div>
        )}
      </div>

      {/* ===== Source Sync ===== */}
      <div className="rounded-2xl border border-base-300/50 bg-base-100 p-6 lg:p-8 space-y-5 shadow-sm transition-shadow hover:shadow-md lg:col-span-2">
        <div className="flex flex-col lg:flex-row items-start lg:items-center justify-between gap-4 border-b border-base-200 pb-4 mb-2">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 rounded-xl bg-secondary/10 flex items-center justify-center text-secondary shrink-0">
              <svg xmlns="http://www.w3.org/2000/svg" className="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M8 9l4-4 4 4m0 6l-4 4-4-4M4 12h16" />
              </svg>
            </div>
            <div>
              <h3 className="font-bold text-lg text-base-content">Source Sync</h3>
              <p className="text-xs text-base-content/50 font-medium">从 MiSub 拉取统一 manifest，并在失败时切到 aggregator 回退订阅</p>
            </div>
          </div>

          {sourceSyncStatus && (
            <div className="flex flex-wrap items-center gap-2">
              <span className={`badge badge-sm border-none ${sourceSyncStatus.manifest_healthy ? 'bg-success/20 text-success' : 'bg-error/20 text-error'}`}>
                {sourceSyncStatus.manifest_healthy ? 'Manifest 正常' : 'Manifest 异常'}
              </span>
              {sourceSyncStatus.fallback_active && (
                <span className="badge badge-warning badge-sm border-none bg-warning/20 text-warning-content">回退已激活</span>
              )}
              <span className="badge badge-ghost badge-sm">本地 {sourceSyncStatus.local_source_count || 0}</span>
              <span className="badge badge-ghost badge-sm">远端 {sourceSyncStatus.manifest_source_count || 0}</span>
              <span className="badge badge-ghost badge-sm">回退 {sourceSyncStatus.fallback_source_count || 0}</span>
            </div>
          )}
        </div>

        <label className="flex items-center justify-between cursor-pointer gap-4 bg-base-200/30 p-4 rounded-xl border border-base-200 hover:border-base-300 transition-colors">
          <div>
            <span className="font-semibold text-base-content/90 block mb-0.5">启用 Source Sync</span>
            <p className="text-xs text-base-content/50 m-0">启用后，本地会按周期请求 MiSub manifest；失败时仅启用回退订阅</p>
          </div>
          <input
            type="checkbox"
            className="toggle toggle-primary toggle-md"
            checked={settings.source_sync_enabled}
            onChange={(e) => updateField('source_sync_enabled', e.target.checked)}
          />
        </label>

        {sourceSyncStatus?.last_error && (
          <div role="alert" className="alert alert-error alert-soft text-sm">
            <span>{sourceSyncStatus.last_error}</span>
          </div>
        )}

        {settings.source_sync_enabled && (
          <div className="space-y-4 pt-2 animate-in fade-in slide-in-from-top-2">
            <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
              <fieldset className="fieldset">
                <legend className="fieldset-legend font-semibold text-base-content/80">Manifest URL</legend>
                <input
                  type="text"
                  className="input input-md w-full bg-base-200/50 focus:bg-base-100 transition-colors focus:border-primary/50"
                  placeholder="https://misub.example.com/api/manifest/profile-id"
                  value={settings.source_sync_manifest_url}
                  onChange={(e) => updateField('source_sync_manifest_url', e.target.value)}
                />
              </fieldset>
              <fieldset className="fieldset">
                <legend className="fieldset-legend font-semibold text-base-content/80">Manifest Token</legend>
                <input
                  type="text"
                  className="input input-md w-full bg-base-200/50 focus:bg-base-100 transition-colors focus:border-primary/50"
                  placeholder="Bearer token for /api/manifest/:profileId"
                  value={settings.source_sync_manifest_token}
                  onChange={(e) => updateField('source_sync_manifest_token', e.target.value)}
                />
              </fieldset>
            </div>

            <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
              <fieldset className="fieldset">
                <legend className="fieldset-legend font-semibold text-base-content/80">同步间隔</legend>
                <input
                  type="text"
                  className="input input-md w-full bg-base-200/50 focus:bg-base-100 transition-colors focus:border-primary/50"
                  value={settings.source_sync_refresh_interval}
                  onChange={(e) => updateField('source_sync_refresh_interval', e.target.value)}
                />
              </fieldset>
              <fieldset className="fieldset">
                <legend className="fieldset-legend font-semibold text-base-content/80">请求超时</legend>
                <input
                  type="text"
                  className="input input-md w-full bg-base-200/50 focus:bg-base-100 transition-colors focus:border-primary/50"
                  value={settings.source_sync_request_timeout}
                  onChange={(e) => updateField('source_sync_request_timeout', e.target.value)}
                />
              </fieldset>
              <fieldset className="fieldset">
                <legend className="fieldset-legend font-semibold text-base-content/80">裸代理默认协议</legend>
                <select
                  className="select select-md w-full bg-base-200/50 focus:bg-base-100 transition-colors focus:border-primary/50"
                  value={settings.source_sync_default_direct_proxy_scheme}
                  onChange={(e) => updateField('source_sync_default_direct_proxy_scheme', e.target.value)}
                >
                  <option value="http">http</option>
                  <option value="socks5">socks5</option>
                </select>
              </fieldset>
            </div>

            <div className="grid grid-cols-1 xl:grid-cols-[1fr_auto] gap-3 items-end">
              <fieldset className="fieldset">
                <legend className="fieldset-legend font-semibold text-base-content/80">Fallback 订阅 URL</legend>
                <input
                  type="text"
                  className="input input-md w-full bg-base-200/50 focus:bg-base-100 transition-colors focus:border-primary/50"
                  placeholder="https://r2.example.com/fallback-profile.txt"
                  value={newFallbackUrl}
                  onChange={(e) => setNewFallbackUrl(e.target.value)}
                  onKeyDown={(e) => e.key === 'Enter' && addFallbackSubscription()}
                />
              </fieldset>
              <button className="btn btn-md btn-primary shadow-sm" onClick={addFallbackSubscription} disabled={!newFallbackUrl.trim()}>
                添加回退
              </button>
            </div>

            {settings.source_sync_fallback_subscriptions.length > 0 ? (
              <div className="space-y-3">
                {settings.source_sync_fallback_subscriptions.map((url, index) => (
                  <div key={index} className="flex items-center gap-3 p-3 rounded-xl border border-base-200 bg-base-200/30 hover:bg-base-200/60 transition-colors group">
                    <code className="text-sm font-mono text-base-content/80 break-all flex-1">{url}</code>
                    <button
                      className="btn btn-sm btn-square btn-ghost text-base-content/40 hover:text-error hover:bg-error/10 shrink-0 opacity-0 group-hover:opacity-100 transition-all"
                      onClick={() => removeFallbackSubscription(index)}
                      title="删除回退订阅"
                    >
                      <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                        <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" />
                      </svg>
                    </button>
                  </div>
                ))}
              </div>
            ) : (
              <div className="text-sm text-base-content/50 rounded-xl border border-dashed border-base-300 bg-base-200/20 p-4">
                当前没有配置 fallback 订阅。当 Manifest 不可用且本地也没有其他订阅时，将不会有保底订阅可用。
              </div>
            )}

            {sourceSyncStatus && (
              <div className="grid grid-cols-1 md:grid-cols-2 gap-4 text-sm text-base-content/60">
                <div className="rounded-xl bg-base-200/30 p-4 border border-base-200">
                  <div>最后同步: {sourceSyncStatus.last_sync || '-'}</div>
                  <div>最后成功: {sourceSyncStatus.last_success || '-'}</div>
                </div>
                <div className="rounded-xl bg-base-200/30 p-4 border border-base-200">
                  <div>Manifest URL: {sourceSyncStatus.manifest_url || '-'}</div>
                  <div>运行状态: {sourceSyncStatus.manifest_healthy ? 'Healthy' : (sourceSyncStatus.fallback_active ? 'Fallback' : 'Error')}</div>
                </div>
              </div>
            )}
          </div>
        )}
      </div>

      {/* ===== 订阅管理 (full width) ===== */}
      <div className="rounded-2xl border border-base-300/50 bg-base-100 p-6 lg:p-8 space-y-5 shadow-sm transition-shadow hover:shadow-md lg:col-span-2">
        <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4 border-b border-base-200 pb-4 mb-2">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 rounded-xl bg-error/10 flex items-center justify-center text-error shrink-0">
              <svg xmlns="http://www.w3.org/2000/svg" className="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M13.828 10.172a4 4 0 00-5.656 0l-4 4a4 4 0 105.656 5.656l1.102-1.101m-.758-4.899a4 4 0 005.656 0l4-4a4 4 0 00-5.656-5.656l-1.1 1.1" />
              </svg>
            </div>
            <div>
              <h3 className="font-bold text-lg text-base-content">订阅链接管理</h3>
              <p className="text-xs text-base-content/50 font-medium">配置节点获取来源</p>
            </div>
          </div>

          {/* Show refresh button when subscriptions exist (saved or in current settings) */}
          {(subStatus?.has_subscriptions || settings.subscriptions.length > 0) && (
            <div className="flex items-center gap-3 bg-base-200/50 px-3 py-1.5 rounded-lg border border-base-300/50">
              {subStatus && subStatus.node_count != null && subStatus.node_count > 0 && (
                <span className="text-sm font-medium text-base-content/70">
                  节点: <strong className="text-base-content">{subStatus.node_count}</strong>
                </span>
              )}
              {subStatus?.enabled && (
                <span className="badge badge-success badge-sm border-none bg-success/20 text-success font-semibold">自动刷新</span>
              )}
              <div className="w-px h-4 bg-base-300 mx-1"></div>
              <button
                className="btn btn-sm btn-ghost hover:bg-primary/10 hover:text-primary gap-1.5 px-2"
                onClick={handleSubRefresh}
                disabled={subRefreshing || subStatus?.is_refreshing || isDirty}
                title={isDirty ? '请先保存设置并重载配置' : '立即刷新订阅'}
              >
                {subRefreshing || subStatus?.is_refreshing ? (
                  <span className="loading loading-spinner loading-xs"></span>
                ) : (
                  <>
                    <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                      <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
                    </svg>
                    立即刷新
                  </>
                )}
              </button>
            </div>
          )}
        </div>

        {subStatus?.last_error && (
          <div role="alert" className="alert alert-error alert-soft text-sm py-3 animate-in fade-in">
            <svg xmlns="http://www.w3.org/2000/svg" className="h-5 w-5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M10 14l2-2m0 0l2-2m-2 2l-2-2m2 2l2 2m7-2a9 9 0 11-18 0 9 9 0 0118 0z" />
            </svg>
            <span>{subStatus.last_error}</span>
          </div>
        )}

        {/* Add subscription */}
        <div className="flex gap-2">
          <div className="relative flex-1">
            <div className="absolute inset-y-0 left-0 pl-3 flex items-center pointer-events-none text-base-content/40">
              <svg xmlns="http://www.w3.org/2000/svg" className="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M13.828 10.172a4 4 0 00-5.656 0l-4 4a4 4 0 105.656 5.656l1.102-1.101m-.758-4.899a4 4 0 005.656 0l4-4a4 4 0 00-5.656-5.656l-1.1 1.1" />
              </svg>
            </div>
            <input
              type="text"
              className="input input-md w-full pl-10 font-mono text-sm bg-base-200/50 focus:bg-base-100 transition-colors focus:border-primary/50"
              placeholder="https://example.com/subscribe?token=xxx"
              value={newSubUrl}
              onChange={(e) => setNewSubUrl(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && addSubscription()}
            />
          </div>
          <button
            className="btn btn-md btn-primary shadow-sm"
            onClick={addSubscription}
            disabled={!newSubUrl.trim()}
          >
            <svg xmlns="http://www.w3.org/2000/svg" className="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M12 4v16m8-8H4" />
            </svg>
            添加
          </button>
        </div>

        {/* Subscription list */}
        {settings.subscriptions.length > 0 ? (
          <div className="space-y-3 mt-4">
            {settings.subscriptions.map((url, index) => (
              <div key={index} className="flex items-center gap-3 p-3 lg:p-4 rounded-xl border border-base-200 bg-base-200/30 hover:bg-base-200/60 transition-colors group">
                <div className="flex-1 min-w-0">
                  <code className="text-sm font-mono text-base-content/80 break-all">{url}</code>
                </div>
                <button
                  className="btn btn-sm btn-square btn-ghost text-base-content/40 hover:text-error hover:bg-error/10 shrink-0 opacity-0 group-hover:opacity-100 transition-all"
                  onClick={() => removeSubscription(index)}
                  title="删除订阅"
                >
                  <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" />
                  </svg>
                </button>
              </div>
            ))}
          </div>
        ) : (
          <div className="flex flex-col items-center justify-center py-10 px-4 text-center rounded-xl border border-dashed border-base-300 bg-base-200/20">
            <div className="w-12 h-12 rounded-full bg-base-200 flex items-center justify-center text-base-content/30 mb-3">
              <svg xmlns="http://www.w3.org/2000/svg" className="h-6 w-6" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M13.828 10.172a4 4 0 00-5.656 0l-4 4a4 4 0 105.656 5.656l1.102-1.101m-.758-4.899a4 4 0 005.656 0l4-4a4 4 0 00-5.656-5.656l-1.1 1.1" />
              </svg>
            </div>
            <p className="text-base font-medium text-base-content/60">暂无订阅链接</p>
            <p className="text-sm text-base-content/40 mt-1">在上方输入框添加您的节点订阅地址</p>
          </div>
        )}

        <p className="text-xs text-base-content/40 text-center mt-4">⚠️ 添加或删除订阅后，需点击顶部「保存设置」并「重载配置」才能生效</p>
      </div>
    </>
  )
}
