import { useState } from 'react'

import type { IPMapping } from '../../types'

type MappingInput = Omit<IPMapping, 'mapping_id' | 'revision'>

interface IPMappingsPanelProps {
  mappings: IPMapping[]
  onCreate(input: MappingInput): void | Promise<void>
  onUpdate(mapping: IPMapping, input: MappingInput): void | Promise<void>
  onDelete(mapping: IPMapping): void | Promise<void>
}

export default function IPMappingsPanel({ mappings, onCreate, onUpdate, onDelete }: IPMappingsPanelProps) {
  const [cidr, setCIDR] = useState('')
  const [deviceId, setDeviceId] = useState('')
  const [priority, setPriority] = useState(100)

  const create = async () => {
    if (!cidr.trim() || !deviceId.trim()) return
    await onCreate({ cidr: cidr.trim(), device_id: deviceId.trim().toLowerCase(), priority, enabled: true })
    setCIDR('')
    setDeviceId('')
  }

  return (
    <section className="rounded-lg border border-base-300/50 bg-base-100 p-5 shadow-sm space-y-4">
      <div>
        <h2 className="text-lg font-bold">IP / CIDR 映射</h2>
        <p className="mt-1 text-sm text-warning">
          IP 映射仅作为回退；Docker、NAT、DHCP 或 IPv6 隐私地址可能让多个设备共享或漂移源地址。
        </p>
      </div>

      <div className="grid gap-2 md:grid-cols-[1fr_1fr_7rem_auto]">
        <input className="input input-sm input-bordered" aria-label="IP 或 CIDR" placeholder="192.168.1.10/32" value={cidr} onChange={(event) => setCIDR(event.target.value)} />
        <input className="input input-sm input-bordered" aria-label="映射设备 ID" placeholder="laptop" value={deviceId} onChange={(event) => setDeviceId(event.target.value)} />
        <input className="input input-sm input-bordered" aria-label="映射优先级" type="number" value={priority} onChange={(event) => setPriority(Number(event.target.value))} />
        <button type="button" className="btn btn-sm btn-primary" disabled={!cidr.trim() || !deviceId.trim()} onClick={() => void create()}>添加映射</button>
      </div>

      <div className="space-y-2">
        {mappings.map((mapping) => (
          <div key={mapping.mapping_id} className="grid gap-3 rounded-lg bg-base-200/40 p-3 text-sm md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] md:items-center">
            <div className="min-w-0">
              <code className="block break-all">{mapping.cidr}</code>
              <span className="text-xs text-base-content/45 break-all">{mapping.mapping_id}</span>
            </div>
            <div>
              <div className="font-medium break-all">{mapping.device_id}</div>
              <div className="text-xs text-base-content/50">优先级 {mapping.priority} · 修订 {mapping.revision}</div>
            </div>
            <div className="flex flex-wrap gap-2 md:justify-end">
              <button type="button" className="btn btn-xs btn-ghost" onClick={() => void onUpdate(mapping, {
                cidr: mapping.cidr,
                device_id: mapping.device_id,
                priority: mapping.priority,
                enabled: !mapping.enabled,
              })}>{mapping.enabled ? '停用' : '启用'}</button>
              <button type="button" className="btn btn-xs btn-ghost text-error" onClick={() => void onDelete(mapping)}>删除</button>
            </div>
          </div>
        ))}
        {mappings.length === 0 && <div className="text-sm text-base-content/45">暂无映射</div>}
      </div>
    </section>
  )
}
