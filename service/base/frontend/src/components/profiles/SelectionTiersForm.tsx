import { useId } from 'react'
import type { SelectionTier } from '../../types'

interface Props {
  value?: SelectionTier[]
  onChange(value: SelectionTier[]): void
  disabled?: boolean
}

export default function SelectionTiersForm({ value = [], onChange, disabled = false }: Props) {
  const id = useId()
  const update = (index: number, tier?: SelectionTier) => {
    const next = value.map((item) => ({ ...item }))
    if (tier) next[index] = tier
    else next.splice(index, 1)
    onChange(next)
  }

  return (
    <section className="space-y-3 rounded-lg border border-base-300/50 bg-base-100 p-4" aria-label="三级节点选择">
      <h3 className="font-semibold">三级节点选择</h3>
      <p className="text-xs text-base-content/60">
        按顺序选择可用节点，当前级没有可用节点才进入下一级。国家优先使用节点服务器地址的地理位置，不要求出口 IP 匹配。
      </p>
      <div className="grid gap-4 md:grid-cols-2">
        {[0, 1].map((index) => {
          const tier = value[index]
          const label = index === 0 ? '第一级策略' : '第二级策略'
          return (
            <fieldset className="fieldset" key={index}>
              <label className="fieldset-legend" htmlFor={`${id}-${index}`}>{label}</label>
              <select
                id={`${id}-${index}`}
                className="select select-bordered w-full"
                value={tier?.kind ?? 'disabled'}
                disabled={disabled || (index === 1 && !value[0])}
                onChange={(event) => {
                  const kind = event.target.value
                  update(index, kind === 'disabled' ? undefined : kind === 'country'
                    ? { kind, country: '' } : { kind: kind as SelectionTier['kind'] })
                }}
              >
                <option value="disabled">不启用</option>
                <option value="country">国家策略 · 指定国家优先</option>
                <option value="latency">延时策略 · 最小延迟优先</option>
                <option value="stability">稳定策略 · 最小波动优先</option>
              </select>
              {tier?.kind === 'country' && (
                <label className="space-y-1">
                  <span>{label}优先国家（两位代码）</span>
                  <input
                    className="input input-bordered w-full"
                    value={tier.country ?? ''}
                    placeholder="例如 JP、US、SG"
                    maxLength={2}
                    disabled={disabled}
                    onChange={(event) => update(index, { kind: 'country', country: event.target.value.trim().toUpperCase() })}
                  />
                </label>
              )}
              {tier?.kind === 'country' && !/^[A-Z]{2}$/.test(tier.country ?? '') && (
                <p className="text-xs text-warning" role="status">请填写两位国家代码后保存。</p>
              )}
            </fieldset>
          )
        })}
      </div>
      <button type="button" className="btn btn-sm btn-ghost" disabled={disabled || value.length !== 2} onClick={() => onChange([value[1], value[0]])}>
        交换一二级顺序
      </button>
      <p className="text-xs text-base-content/60">停用某级后，后续策略前移；两级均停用时保留原有选节点行为。延时或波动测量不足、过期时跳过该级。</p>
      <p className="text-xs text-base-content/60">延时采用最近一次成功探测的正延迟；稳定性采用最近最多 8 次成功探测的延迟标准差，至少需要 3 次。样本需足够新，默认每 2 小时探测时需要等待积累。</p>
      <div className="rounded-lg bg-base-200/50 p-3 font-semibold">第三级（固定）：回退到通用池</div>
      <p className="text-xs text-base-content/60">国家优先失败可以回退；下方国家、地区和长效筛选是硬限制，对所有级别（含通用池）始终生效。国家级和通用池使用 stable / session / auto；延时级、稳定级严格按测量指标排序，不使用会话绑定或手动置顶。</p>
    </section>
  )
}
