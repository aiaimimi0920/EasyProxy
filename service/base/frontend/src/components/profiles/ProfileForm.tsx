import type { ForwardingProfile, RuleProvider } from '../../types'

interface ProfileFormProps {
  value: ForwardingProfile
  onChange(value: ForwardingProfile): void
  disabled?: boolean
}

function updateProvider(
  providers: RuleProvider[],
  index: number,
  update: Partial<RuleProvider>,
): RuleProvider[] {
  return providers.map((provider, current) => current === index ? { ...provider, ...update } : provider)
}

function commaList(value: string): string[] {
  return value.split(',').map((item) => item.trim()).filter(Boolean)
}

export default function ProfileForm({ value, onChange, disabled = false }: ProfileFormProps) {
  const patch = (update: Partial<ForwardingProfile>) => onChange({ ...value, ...update })
  const providers = value.rule_providers ?? []

  return (
    <div className="space-y-5">
      <label className="flex items-center justify-between gap-4 rounded-lg border border-base-300/50 bg-base-100 p-4">
        <span>
          <span className="block font-semibold">启用此配置</span>
          <span className="block text-xs text-base-content/50">关闭后选中此 Profile 的流量全部 DIRECT</span>
        </span>
        <input
          type="checkbox"
          className="toggle toggle-primary"
          aria-label="启用此配置"
          checked={value.enabled}
          disabled={disabled}
          onChange={(event) => patch({ enabled: event.target.checked })}
        />
      </label>

      <div className="grid gap-4 md:grid-cols-2">
        <fieldset className="fieldset">
          <label htmlFor="profile-strategy" className="fieldset-legend">默认选节点策略</label>
          <select
            id="profile-strategy"
            className="select select-bordered w-full"
            value={value.default_strategy}
            disabled={disabled}
            onChange={(event) => patch({ default_strategy: event.target.value as ForwardingProfile['default_strategy'] })}
          >
            <option value="stable">stable</option>
            <option value="session">session</option>
            <option value="auto">auto</option>
          </select>
        </fieldset>

        <fieldset className="fieldset">
          <label htmlFor="profile-final-policy" className="fieldset-legend">最终策略</label>
          <select
            id="profile-final-policy"
            className="select select-bordered w-full"
            value={value.final_policy}
            disabled={disabled}
            onChange={(event) => patch({ final_policy: event.target.value as ForwardingProfile['final_policy'] })}
          >
            <option value="PROXY">PROXY</option>
            <option value="DIRECT">DIRECT</option>
          </select>
        </fieldset>
      </div>

      <label className="flex items-center gap-3 cursor-pointer">
        <input
          type="checkbox"
          className="checkbox checkbox-sm checkbox-primary"
          checked={value.use_default_rules}
          disabled={disabled}
          onChange={(event) => patch({ use_default_rules: event.target.checked })}
        />
        <span className="text-sm">附加默认规则集</span>
      </label>

      <fieldset className="fieldset">
        <label htmlFor="profile-rules" className="fieldset-legend">规则列表</label>
        <textarea
          id="profile-rules"
          className="textarea textarea-bordered min-h-32 w-full font-mono text-sm"
          value={value.rules.join('\n')}
          disabled={disabled}
          onChange={(event) => patch({ rules: event.target.value.split('\n') })}
          placeholder="DOMAIN-SUFFIX,example.com,PROXY"
        />
      </fieldset>

      <div className="rounded-lg border border-base-300/50 bg-base-100 p-4 space-y-3">
        <div className="flex items-center justify-between gap-3">
          <div>
            <h3 className="font-semibold">远程规则订阅</h3>
            <p className="text-xs text-base-content/50">按 Profile 独立维护的规则提供方</p>
          </div>
          <button
            type="button"
            className="btn btn-sm btn-ghost"
            disabled={disabled}
            onClick={() => patch({ rule_providers: [...providers, { url: '', policy: 'DIRECT', behavior: 'domain', interval: '24h' }] })}
          >
            添加规则订阅
          </button>
        </div>
        {providers.map((provider, index) => (
          <div key={`${provider.url}-${index}`} className="grid gap-2 md:grid-cols-[1fr_auto_auto_auto_auto]">
            <input
              className="input input-sm input-bordered"
              aria-label={`规则订阅 ${index + 1} 地址`}
              value={provider.url}
              disabled={disabled}
              onChange={(event) => patch({ rule_providers: updateProvider(providers, index, { url: event.target.value }) })}
            />
            <select
              className="select select-sm select-bordered"
              aria-label={`规则订阅 ${index + 1} 策略`}
              value={provider.policy}
              disabled={disabled}
              onChange={(event) => patch({ rule_providers: updateProvider(providers, index, { policy: event.target.value as RuleProvider['policy'] }) })}
            >
              <option value="DIRECT">DIRECT</option>
              <option value="PROXY">PROXY</option>
            </select>
            <select
              className="select select-sm select-bordered"
              aria-label={`规则订阅 ${index + 1} 行为`}
              value={provider.behavior}
              disabled={disabled}
              onChange={(event) => patch({ rule_providers: updateProvider(providers, index, { behavior: event.target.value as RuleProvider['behavior'] }) })}
            >
              <option value="domain">domain</option>
              <option value="ipcidr">ipcidr</option>
              <option value="classical">classical</option>
            </select>
            <input
              className="input input-sm input-bordered"
              aria-label={`规则订阅 ${index + 1} 间隔`}
              value={provider.interval}
              disabled={disabled}
              onChange={(event) => patch({ rule_providers: updateProvider(providers, index, { interval: event.target.value }) })}
            />
            <button
              type="button"
              className="btn btn-sm btn-square btn-ghost text-error"
              aria-label={`删除规则订阅 ${index + 1}`}
              disabled={disabled}
              onClick={() => patch({ rule_providers: providers.filter((_, current) => current !== index) })}
            >
              ×
            </button>
          </div>
        ))}
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        <fieldset className="fieldset">
          <label htmlFor="profile-countries" className="fieldset-legend">国家筛选</label>
          <input
            id="profile-countries"
            className="input input-bordered w-full"
            value={value.node_filter.countries.join(', ')}
            disabled={disabled}
            onChange={(event) => patch({ node_filter: { ...value.node_filter, countries: commaList(event.target.value) } })}
          />
        </fieldset>
        <fieldset className="fieldset">
          <label htmlFor="profile-regions" className="fieldset-legend">地区筛选</label>
          <input
            id="profile-regions"
            className="input input-bordered w-full"
            value={value.node_filter.regions.join(', ')}
            disabled={disabled}
            onChange={(event) => patch({ node_filter: { ...value.node_filter, regions: commaList(event.target.value) } })}
          />
        </fieldset>
      </div>

      <div className="grid gap-4 md:grid-cols-3">
        <fieldset className="fieldset">
          <label htmlFor="profile-long-lived" className="fieldset-legend">长效节点筛选</label>
          <select
            id="profile-long-lived"
            className="select select-bordered w-full"
            value={value.node_filter.long_lived === null ? 'any' : value.node_filter.long_lived ? 'true' : 'false'}
            disabled={disabled}
            onChange={(event) => patch({ node_filter: { ...value.node_filter, long_lived: event.target.value === 'any' ? null : event.target.value === 'true' } })}
          >
            <option value="any">不限</option>
            <option value="true">仅长效</option>
            <option value="false">排除长效</option>
          </select>
        </fieldset>
        <fieldset className="fieldset">
          <label htmlFor="profile-min-uptime" className="fieldset-legend">长效最低在线时长</label>
          <input
            id="profile-min-uptime"
            className="input input-bordered w-full"
            value={value.long_lived.min_uptime}
            disabled={disabled}
            onChange={(event) => patch({ long_lived: { ...value.long_lived, min_uptime: event.target.value } })}
          />
        </fieldset>
        <fieldset className="fieldset">
          <label htmlFor="profile-min-rate" className="fieldset-legend">长效最低成功率</label>
          <input
            id="profile-min-rate"
            type="number"
            min="0"
            max="1"
            step="0.01"
            className="input input-bordered w-full"
            value={value.long_lived.min_success_rate}
            disabled={disabled}
            onChange={(event) => patch({ long_lived: { ...value.long_lived, min_success_rate: Number(event.target.value) } })}
          />
        </fieldset>
      </div>

      <fieldset className="fieldset">
        <label htmlFor="profile-session-ttl" className="fieldset-legend">会话 TTL</label>
        <input
          id="profile-session-ttl"
          className="input input-bordered w-full"
          value={value.session.ttl}
          disabled={disabled}
          onChange={(event) => patch({ session: { ttl: event.target.value } })}
        />
      </fieldset>
    </div>
  )
}
