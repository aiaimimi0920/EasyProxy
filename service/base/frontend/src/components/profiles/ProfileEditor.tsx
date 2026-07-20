import { useCallback, useEffect, useState } from 'react'

import { ApiError } from '../../api/client'
import {
  fetchDevice,
  fetchSharedProfile,
  putDeviceProfile,
  putSharedProfile,
} from '../../api/localServer'
import { useUnsavedChangesGuard } from '../../hooks/useUnsavedChangesGuard'
import type { ForwardingProfile, ProfileResource } from '../../types'
import ProfileForm from './ProfileForm'

interface ProfileEditorProps {
  scope: { kind: 'shared' } | { kind: 'device'; deviceId: string }
  onClose(): void
}

function requestedRevision(scope: ProfileEditorProps['scope'], resource: ProfileResource): number {
  if (scope.kind === 'device' && resource.profile_scope === 'shared') return 0
  return resource.revision
}

export default function ProfileEditor({ scope, onClose }: ProfileEditorProps) {
  const [profile, setProfile] = useState<ForwardingProfile | null>(null)
  const [revision, setRevision] = useState(0)
  const [dirty, setDirty] = useState(false)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const { confirmNavigation } = useUnsavedChangesGuard(dirty)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    setMessage('')
    try {
      const resource = scope.kind === 'shared'
        ? await fetchSharedProfile()
        : (await fetchDevice(scope.deviceId)).profile
      if (!resource) throw new Error('设备配置不存在')
      setProfile(resource.profile)
      setRevision(requestedRevision(scope, resource))
      setDirty(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载配置失败')
    } finally {
      setLoading(false)
    }
  }, [scope])

  useEffect(() => {
    void Promise.resolve().then(load)
  }, [load])

  const save = async () => {
    if (!profile) return
    setSaving(true)
    setError('')
    setMessage('')
    try {
      const result = scope.kind === 'shared'
        ? await putSharedProfile(profile, revision)
        : await putDeviceProfile(scope.deviceId, profile, revision)
      if (result.resource) {
        setProfile(result.resource.profile)
        setRevision(result.resource.revision)
      } else {
        setRevision(result.revision)
      }
      setDirty(false)
      setMessage('配置已保存')
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        setError('配置已被其他操作修改，请重新加载后再保存')
      } else {
        setError(err instanceof Error ? err.message : '保存配置失败')
      }
    } finally {
      setSaving(false)
    }
  }

  const close = () => {
    if (confirmNavigation()) onClose()
  }

  if (loading) {
    return <div className="p-6"><span className="loading loading-spinner loading-md text-primary" /></div>
  }

  if (!profile) {
    return (
      <div className="space-y-4 p-6">
        <div role="alert" className="alert alert-error">{error || '配置不可用'}</div>
        <button type="button" className="btn btn-ghost" onClick={close}>关闭</button>
      </div>
    )
  }

  return (
    <div className="space-y-5">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h2 className="text-xl font-bold">
            {scope.kind === 'shared' ? '共享配置' : `设备配置 · ${scope.deviceId}`}
          </h2>
          <p className="text-sm text-base-content/50">当前修订 {revision}</p>
        </div>
        <button type="button" className="btn btn-sm btn-ghost" onClick={close}>关闭</button>
      </div>

      {error && <div role="alert" className="alert alert-error text-sm">{error}</div>}
      {message && <div role="status" className="alert alert-success text-sm">{message}</div>}

      <ProfileForm
        value={profile}
        disabled={saving}
        onChange={(next) => {
          setProfile(next)
          setDirty(true)
          setMessage('')
        }}
      />

      <div className="sticky bottom-0 flex justify-end gap-3 border-t border-base-300/50 bg-base-100/90 py-3 backdrop-blur">
        <button type="button" className="btn btn-ghost" disabled={saving} onClick={() => { if (confirmNavigation()) void load() }}>
          重新加载
        </button>
        <button type="button" className="btn btn-primary" disabled={saving || !dirty} onClick={() => void save()}>
          {saving && <span className="loading loading-spinner loading-xs" />}
          保存配置
        </button>
      </div>
    </div>
  )
}
