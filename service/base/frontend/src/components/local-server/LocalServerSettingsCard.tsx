import { useEffect, useState } from 'react'

import { fetchLocalServerConfig, updateLocalServerConfig } from '../../api/localServer'
import type { LocalServerConfig } from '../../types/localServer'

const emptyConfig: LocalServerConfig = {
  enabled: false,
  listen: '',
  auth_username: '',
  password_set: false,
  shared_revision: 0,
  credential_generation: 0,
}

interface LocalServerSettingsCardProps {
  onModeChange?: (enabled: boolean) => void
}

export default function LocalServerSettingsCard({ onModeChange }: LocalServerSettingsCardProps) {
  const [config, setConfig] = useState<LocalServerConfig>(emptyConfig)
  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')

  useEffect(() => {
    let active = true
    const load = async () => {
      try {
        const loaded = await fetchLocalServerConfig()
        if (active) {
          setConfig(loaded)
          setPassword('')
        }
      } catch (err) {
        if (active) setError(err instanceof Error ? err.message : '加载本地服务器设置失败')
      } finally {
        if (active) setLoading(false)
      }
    }
    void load()
    return () => {
      active = false
    }
  }, [])

  const save = async (event: React.FormEvent) => {
    event.preventDefault()
    setSaving(true)
    setError('')
    setMessage('')
    try {
      const update: {
        enabled: boolean
        listen: string
        auth_username: string
        auth_password?: string
      } = {
        enabled: config.enabled,
        listen: config.listen,
        auth_username: config.auth_username,
      }
      if (password !== '') update.auth_password = password
      const result = await updateLocalServerConfig(update)
      const savedConfig = result.resource ?? config
      setConfig(savedConfig)
      onModeChange?.(savedConfig.enabled)
      setPassword('')
      setMessage(result.need_reload ? '设置已保存，重载后生效' : '设置已保存')
    } catch (err) {
      setError(err instanceof Error ? err.message : '保存本地服务器设置失败')
    } finally {
      setSaving(false)
    }
  }

  if (loading) {
    return (
      <div className="rounded-lg border border-base-300/50 bg-base-100 p-6" aria-label="加载本地服务器设置">
        <span className="loading loading-spinner loading-md text-primary" />
      </div>
    )
  }

  return (
    <form onSubmit={save} className="rounded-lg border border-base-300/50 bg-base-100 p-6 lg:p-8 space-y-5 shadow-sm">
      <div className="flex flex-col gap-2 border-b border-base-200 pb-4">
        <div className="flex items-center justify-between gap-4">
          <div>
            <h3 className="font-bold text-lg text-base-content">本地服务器</h3>
            <p className="text-xs text-base-content/50 font-medium">统一管理 Web、HTTP 和 SOCKS5 凭证</p>
          </div>
          <input
            type="checkbox"
            className="toggle toggle-primary"
            aria-label="启用本地服务器"
            checked={config.enabled}
            onChange={(event) => setConfig((current) => ({ ...current, enabled: event.target.checked }))}
          />
        </div>
        <p className="text-sm text-base-content/60">
          旧凭证字段由本地服务器凭证派生，在本地服务器模式下不可单独编辑。
        </p>
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        <fieldset className="fieldset">
          <label htmlFor="local-server-listen" className="fieldset-legend font-semibold text-base-content/80">
            监听地址
          </label>
          <input
            id="local-server-listen"
            className="input input-md w-full bg-base-200/50"
            value={config.listen}
            onChange={(event) => setConfig((current) => ({ ...current, listen: event.target.value }))}
            placeholder="留空则复用代理监听地址"
          />
        </fieldset>

        <fieldset className="fieldset">
          <label htmlFor="local-server-username" className="fieldset-legend font-semibold text-base-content/80">
            用户名
          </label>
          <input
            id="local-server-username"
            className="input input-md w-full bg-base-200/50"
            value={config.auth_username}
            onChange={(event) => setConfig((current) => ({ ...current, auth_username: event.target.value }))}
          />
        </fieldset>
      </div>

      <fieldset className="fieldset">
        <label htmlFor="local-server-password" className="fieldset-legend font-semibold text-base-content/80">
          新密码
        </label>
        <input
          id="local-server-password"
          type="password"
          autoComplete="new-password"
          className="input input-md w-full bg-base-200/50"
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          placeholder={config.password_set ? '留空以保留现有密码' : '请输入本地服务器密码'}
        />
        <p className="label text-base-content/50 mt-1">
          密码为只写字段，服务器不会返回或回填现有值。
        </p>
      </fieldset>

      {error && <div role="alert" className="alert alert-error alert-soft text-sm">{error}</div>}
      {message && <div role="status" className="alert alert-success alert-soft text-sm">{message}</div>}

      <div className="flex items-center justify-between gap-4">
        <span className="text-xs text-base-content/50">
          凭证代次 {config.credential_generation}，共享配置修订 {config.shared_revision}
        </span>
        <button type="submit" className="btn btn-primary" disabled={saving}>
          {saving && <span className="loading loading-spinner loading-xs" />}
          保存本地服务器设置
        </button>
      </div>
    </form>
  )
}
