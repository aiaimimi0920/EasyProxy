import { useCallback, useEffect, useState } from 'react'

import {
  copySharedProfile,
  createIPMapping,
  deleteDeviceProfile,
  deleteIPMapping,
  fetchDevices,
  fetchIPMappings,
  fetchSharedProfile,
  putDeviceProfile,
  setDeviceProfileEnabled,
  updateIPMapping,
} from '../api/localServer'
import { requestAppNavigation } from '../hooks/useUnsavedChangesGuard'
import type { DeviceSummary, ForwardingProfile, IPMapping, ProfileResource } from '../types'
import DeviceTable from './devices/DeviceTable'
import IPMappingsPanel from './devices/IPMappingsPanel'
import SharedProfileCard from './devices/SharedProfileCard'
import ProfileEditor from './profiles/ProfileEditor'

const defaultProfile = (): ForwardingProfile => ({
  schema_version: 1,
  enabled: true,
  default_strategy: 'stable',
  use_default_rules: true,
  final_policy: 'PROXY',
  rules: [],
  rule_providers: [],
  node_filter: { countries: [], regions: [], long_lived: null },
  long_lived: { min_uptime: '2h', min_success_rate: 0.9 },
  session: { ttl: '10m' },
})

type EditorScope = { kind: 'shared' } | { kind: 'device'; deviceId: string }

export default function DevicesPanel() {
  const [shared, setShared] = useState<ProfileResource | null>(null)
  const [devices, setDevices] = useState<DeviceSummary[]>([])
  const [mappings, setMappings] = useState<IPMapping[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [creationTarget, setCreationTarget] = useState<DeviceSummary | null>(null)
  const [editorScope, setEditorScope] = useState<EditorScope | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [sharedResource, deviceResponse, mappingResponse] = await Promise.all([
        fetchSharedProfile(),
        fetchDevices(),
        fetchIPMappings(),
      ])
      setShared(sharedResource)
      setDevices(deviceResponse.devices)
      setMappings(mappingResponse.mappings)
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载设备策略失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void Promise.resolve().then(load)
  }, [load])

  const openEditor = (scope: EditorScope) => {
    if (!requestAppNavigation()) return
    setEditorScope(scope)
  }

  const createBlank = async () => {
    if (!creationTarget) return
    try {
      await putDeviceProfile(creationTarget.device_id, defaultProfile(), 0)
      setNotice('已使用默认配置创建独立 Profile。复制当前值，后续不联动。')
      setCreationTarget(null)
      await load()
    } catch (err) {
      setError(err instanceof Error ? err.message : '创建独立配置失败')
    }
  }

  const copyShared = async () => {
    if (!creationTarget) return
    try {
      await copySharedProfile(creationTarget.device_id)
      setNotice('已复制共享 Profile 当前值，后续不联动。')
      setCreationTarget(null)
      await load()
    } catch (err) {
      setError(err instanceof Error ? err.message : '复制共享配置失败')
    }
  }

  const deleteProfile = async (device: DeviceSummary) => {
    if (!device.profile_revision) return
    try {
      await deleteDeviceProfile(device.device_id, device.profile_revision)
      setNotice(`${device.display_name || device.device_id} 已回到共享配置`)
      await load()
    } catch (err) {
      setError(err instanceof Error ? err.message : '删除独立配置失败')
    }
  }

  const toggleProfile = async (device: DeviceSummary) => {
    if (!device.profile_revision) return
    try {
      await setDeviceProfileEnabled(device.device_id, !device.effective_enabled, device.profile_revision)
      await load()
    } catch (err) {
      setError(err instanceof Error ? err.message : '更新配置状态失败')
    }
  }

  const createMapping = async (input: Omit<IPMapping, 'mapping_id' | 'revision'>) => {
    try {
      await createIPMapping(input)
      await load()
    } catch (err) {
      setError(err instanceof Error ? err.message : '创建映射失败')
    }
  }

  const updateMapping = async (mapping: IPMapping, input: Omit<IPMapping, 'mapping_id' | 'revision'>) => {
    try {
      await updateIPMapping(mapping.mapping_id, input, mapping.revision)
      await load()
    } catch (err) {
      setError(err instanceof Error ? err.message : '更新映射失败')
    }
  }

  const removeMapping = async (mapping: IPMapping) => {
    try {
      await deleteIPMapping(mapping.mapping_id, mapping.revision)
      await load()
    } catch (err) {
      setError(err instanceof Error ? err.message : '删除映射失败')
    }
  }

  if (loading) {
    return <div className="p-6 flex min-h-[60vh] items-center justify-center"><span className="loading loading-spinner loading-lg text-primary" /></div>
  }

  return (
    <div className="mx-auto max-w-6xl space-y-6 p-4 lg:p-6">
      <header>
        <h1 className="text-2xl font-black">设备策略</h1>
        <p className="mt-1 text-sm text-base-content/50">集中管理共享和每设备独立 Forwarding Profile</p>
      </header>

      {error && <div role="alert" className="alert alert-error text-sm">{error}</div>}
      {notice && <div role="status" className="alert alert-success text-sm">{notice}</div>}

      {shared && <SharedProfileCard resource={shared} onEdit={() => openEditor({ kind: 'shared' })} />}
      <DeviceTable
        devices={devices}
        onCreateProfile={setCreationTarget}
        onEditProfile={(device) => openEditor({ kind: 'device', deviceId: device.device_id })}
        onDeleteProfile={(device) => void deleteProfile(device)}
        onToggleProfile={(device) => void toggleProfile(device)}
      />
      <IPMappingsPanel mappings={mappings} onCreate={createMapping} onUpdate={updateMapping} onDelete={removeMapping} />

      {creationTarget && (
        <div role="dialog" aria-modal="true" aria-label="创建独立配置" className="fixed inset-0 z-[120] flex items-center justify-center bg-black/50 p-4">
          <div className="w-full max-w-lg rounded-lg bg-base-100 p-6 shadow-2xl space-y-4">
            <div>
              <h2 className="text-lg font-bold">为 {creationTarget.display_name || creationTarget.device_id} 创建独立 Profile</h2>
              <p className="mt-1 text-sm text-base-content/60">复制共享配置只复制当前值，后续不联动。</p>
            </div>
            <div className="flex flex-wrap justify-end gap-2">
              <button type="button" className="btn btn-ghost" onClick={() => setCreationTarget(null)}>取消</button>
              <button type="button" className="btn btn-ghost" onClick={() => void copyShared()}>复制共享配置</button>
              <button type="button" className="btn btn-primary" onClick={() => void createBlank()}>使用默认配置</button>
            </div>
          </div>
        </div>
      )}

      {editorScope && (
        <div role="dialog" aria-modal="true" aria-label="编辑 Profile" className="fixed inset-0 z-[120] overflow-y-auto bg-black/50 p-4 lg:p-8">
          <div className="mx-auto max-w-4xl rounded-lg bg-base-100 p-5 shadow-2xl">
            <ProfileEditor scope={editorScope} onClose={() => {
              setEditorScope(null)
              void load()
            }} />
          </div>
        </div>
      )}
    </div>
  )
}
