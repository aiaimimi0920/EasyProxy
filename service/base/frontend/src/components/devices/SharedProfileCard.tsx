import type { ProfileResource } from '../../types'

interface SharedProfileCardProps {
  resource: ProfileResource
  onEdit(): void
}

export default function SharedProfileCard({ resource, onEdit }: SharedProfileCardProps) {
  const profile = resource.profile
  return (
    <section className="rounded-lg border border-base-300/50 bg-base-100 p-5 shadow-sm">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h2 className="text-lg font-bold">共享 Profile</h2>
          <p className="text-sm text-base-content/50">没有独立配置的设备使用此完整策略</p>
        </div>
        <button type="button" className="btn btn-sm btn-primary" onClick={onEdit}>编辑共享配置</button>
      </div>
      <div className="mt-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
        <Metric label="状态" value={profile.enabled ? '启用' : 'DIRECT'} />
        <Metric label="选节点策略" value={profile.default_strategy} />
        <Metric label="最终策略" value={profile.final_policy} />
        <Metric label="规则" value={`${profile.rules.length} 条规则`} />
        <Metric label="版本" value={`修订 ${resource.revision}`} />
      </div>
    </section>
  )
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg bg-base-200/50 px-3 py-2" role="group" aria-label={`${label} ${value}`}>
      <div className="text-xs text-base-content/45">{label}</div>
      <div className="mt-1 font-semibold break-words">{value}</div>
    </div>
  )
}
