import type { DeviceSummary } from '../../types'

interface DeviceTableProps {
  devices: DeviceSummary[]
  onCreateProfile(device: DeviceSummary): void
  onEditProfile(device: DeviceSummary): void
  onDeleteProfile(device: DeviceSummary): void
  onToggleProfile(device: DeviceSummary): void
}

function effectiveLabel(device: DeviceSummary): string {
  if (device.effective_state === 'DIRECT') return 'DIRECT'
  return device.profile_mode === 'independent' ? '独立配置' : '共享配置'
}

export default function DeviceTable({
  devices,
  onCreateProfile,
  onEditProfile,
  onDeleteProfile,
  onToggleProfile,
}: DeviceTableProps) {
  return (
    <section className="space-y-3">
      <div>
        <h2 className="text-lg font-bold">设备策略</h2>
        <p className="text-sm text-base-content/50">设备 ID 只选择策略，不提供独立授权</p>
      </div>
      {devices.length === 0 && <div className="rounded-lg border border-dashed border-base-300 p-6 text-center text-base-content/50">尚未观察到设备</div>}
      <div className="space-y-2">
        {devices.map((device) => (
          <article key={device.device_id} className="rounded-lg border border-base-300/50 bg-base-100 p-4 shadow-sm">
            <div className="grid gap-4 lg:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)_auto] lg:items-center">
              <div className="min-w-0">
                <div className="font-semibold break-words">{device.display_name || device.device_id}</div>
                <code className="text-xs text-base-content/50 break-all">{device.device_id}</code>
                <div className="mt-2 text-xs text-base-content/50">
                  {device.identity_source ? `身份来源：${device.identity_source}` : '身份来源：未观察'}
                  {device.last_seen_ip && ` · ${device.last_seen_ip}`}
                  {device.last_seen_at && ` · ${new Date(device.last_seen_at).toLocaleString()}`}
                </div>
              </div>
              <div className="grid grid-cols-2 gap-2 text-sm">
                <div>
                  <div className="text-xs text-base-content/45">Profile 模式</div>
                  <div className="font-medium">{device.profile_mode === 'independent' ? '独立' : '共享'}</div>
                </div>
                <div>
                  <div className="text-xs text-base-content/45">有效状态</div>
                  <div
                    className={`font-semibold ${device.effective_state === 'DIRECT' ? 'text-warning' : 'text-success'}`}
                    role="status"
                    aria-label={`有效状态 ${effectiveLabel(device)}`}
                  >
                    {effectiveLabel(device)}
                  </div>
                </div>
                <div className="text-xs text-base-content/50">设备修订 {device.revision}</div>
                <div className="text-xs text-base-content/50">映射 {device.mapping_count}</div>
              </div>
              <div className="flex flex-wrap gap-2 lg:justify-end">
                {device.profile_mode === 'shared' ? (
                  <button type="button" className="btn btn-sm btn-primary" onClick={() => onCreateProfile(device)}>创建独立配置</button>
                ) : (
                  <>
                    <button type="button" className="btn btn-sm btn-ghost" onClick={() => onEditProfile(device)}>编辑配置</button>
                    <button type="button" className="btn btn-sm btn-ghost" onClick={() => onToggleProfile(device)}>
                      {device.effective_enabled ? '切换为 DIRECT' : '启用配置'}
                    </button>
                    <button type="button" className="btn btn-sm btn-ghost text-error" onClick={() => onDeleteProfile(device)}>删除独立配置</button>
                  </>
                )}
              </div>
            </div>
          </article>
        ))}
      </div>
    </section>
  )
}
