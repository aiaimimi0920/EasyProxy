import { useMemo } from 'react'
import type { ConfigNodeConfig, ConfigNodePayload, NodeSnapshot, NodesResponse } from '../types'

export interface MergedNode extends ConfigNodeConfig {
  configManaged: boolean
  runtimeStatus: 'normal' | 'unavailable' | 'blacklisted' | 'pending' | 'disabled'
  latency_ms: number
  region?: string
  country?: string
  active_connections: number
  success_count: number
  failure_count: number
  tag?: string
}

export type ManageSortKey = 'name' | 'status' | 'latency' | 'region' | 'port' | 'source'
export type SortDir = 'asc' | 'desc'
export type StatusFilter = '' | 'normal' | 'unavailable' | 'blacklisted' | 'pending' | 'disabled'

export const emptyPayload: ConfigNodePayload = {
  name: '',
  uri: '',
  port: 0,
  username: '',
  password: '',
}

function statusOrder(status: MergedNode['runtimeStatus']): number {
  switch (status) {
    case 'normal': return 0
    case 'pending': return 1
    case 'unavailable': return 2
    case 'blacklisted': return 3
    case 'disabled': return 4
    default: return 5
  }
}

function snapshotStatus(snapshot: NodeSnapshot): MergedNode['runtimeStatus'] {
  if (snapshot.blacklisted) return 'blacklisted'
  if (!snapshot.initial_check_done) return 'pending'
  return snapshot.available ? 'normal' : 'unavailable'
}

function compareManageNodes(a: MergedNode, b: MergedNode, key: ManageSortKey, dir: SortDir): number {
  let comparison = 0
  switch (key) {
    case 'name':
      comparison = a.name.localeCompare(b.name)
      break
    case 'status':
      comparison = statusOrder(a.runtimeStatus) - statusOrder(b.runtimeStatus)
      break
    case 'latency': {
      const left = a.latency_ms < 0 ? Infinity : a.latency_ms
      const right = b.latency_ms < 0 ? Infinity : b.latency_ms
      comparison = left - right
      break
    }
    case 'region':
      comparison = (a.region || a.country || '').localeCompare(b.region || b.country || '')
      break
    case 'port':
      comparison = (a.port || 0) - (b.port || 0)
      break
    case 'source':
      comparison = (a.source || '').localeCompare(b.source || '')
      break
  }
  return dir === 'asc' ? comparison : -comparison
}

export function latencyColor(milliseconds: number): string {
  if (milliseconds < 0) return 'text-base-content/50'
  if (milliseconds <= 100) return 'text-success'
  if (milliseconds <= 300) return 'text-warning'
  return 'text-error'
}

export function regionFlag(region?: string): string {
  const flags: Record<string, string> = {
    hk: '🇭🇰', jp: '🇯🇵', kr: '🇰🇷', us: '🇺🇸', tw: '🇹🇼',
    sg: '🇸🇬', de: '🇩🇪', gb: '🇬🇧', fr: '🇫🇷', ca: '🇨🇦',
    au: '🇦🇺', in: '🇮🇳', br: '🇧🇷', ru: '🇷🇺', nl: '🇳🇱',
  }
  return flags[region?.toLowerCase() || ''] || '🌐'
}

export function sourceLabel(source?: string): string {
  switch (source) {
    case 'inline': return '配置文件'
    case 'nodes_file': return '节点文件'
    case 'subscription': return '订阅'
    case 'manual': return '手动添加'
    default: return source || '-'
  }
}

interface ManageNodeViewOptions {
  configNodes: ConfigNodeConfig[]
  monitorData: NodesResponse | null
  filter: string
  statusFilter: StatusFilter
  regionFilter: string
  sourceFilter: string
  sortKey: ManageSortKey
  sortDir: SortDir
  selectedNodes: Set<string>
}

export function useManageNodeView(options: ManageNodeViewOptions) {
  const mergedNodes = useMemo((): MergedNode[] => {
    const snapshots = options.monitorData?.nodes || []
    const snapshotByName = new Map(snapshots.map(snapshot => [snapshot.name, snapshot]))
    const configNames = new Set(options.configNodes.map(node => node.name))
    const configured = options.configNodes.map((config): MergedNode => {
      const snapshot = snapshotByName.get(config.name)
      if (config.disabled || !snapshot) {
        return {
          ...config,
          configManaged: true,
          runtimeStatus: config.disabled ? 'disabled' : 'pending',
          latency_ms: -1,
          region: undefined,
          country: undefined,
          active_connections: 0,
          success_count: 0,
          failure_count: 0,
          tag: undefined,
        }
      }
      return {
        ...config,
        configManaged: true,
        runtimeStatus: snapshotStatus(snapshot),
        latency_ms: snapshot.last_latency_ms,
        region: snapshot.region,
        country: snapshot.country,
        active_connections: snapshot.active_connections,
        success_count: typeof snapshot.success_count === 'number' ? snapshot.success_count : 0,
        failure_count: snapshot.failure_count,
        tag: snapshot.tag,
      }
    })
    const runtimeOnly = snapshots
      .filter(snapshot => !configNames.has(snapshot.name))
      .map((snapshot): MergedNode => ({
        name: snapshot.name,
        uri: snapshot.uri,
        port: snapshot.port || 0,
        username: '',
        password: '',
        source: snapshot.source_kind || 'runtime',
        disabled: false,
        configManaged: false,
        runtimeStatus: snapshotStatus(snapshot),
        latency_ms: snapshot.last_latency_ms,
        region: snapshot.region,
        country: snapshot.country,
        active_connections: snapshot.active_connections,
        success_count: typeof snapshot.success_count === 'number' ? snapshot.success_count : 0,
        failure_count: snapshot.failure_count,
        tag: snapshot.tag,
      }))
    return [...configured, ...runtimeOnly]
  }, [options.configNodes, options.monitorData])

  const regions = useMemo(() => Array.from(new Set(
    mergedNodes.map(node => node.region).filter((region): region is string => Boolean(region)),
  )).sort(), [mergedNodes])
  const sources = useMemo(() => Array.from(new Set(
    mergedNodes.map(node => node.source).filter((source): source is string => Boolean(source)),
  )).sort(), [mergedNodes])
  const filteredNodes = useMemo(() => mergedNodes.filter(node => {
    if (options.filter) {
      const query = options.filter.toLowerCase()
      if (!node.name.toLowerCase().includes(query)
        && !node.uri.toLowerCase().includes(query)
        && !(node.country || '').toLowerCase().includes(query)
        && !(node.region || '').toLowerCase().includes(query)) return false
    }
    if (options.statusFilter && node.runtimeStatus !== options.statusFilter) return false
    if (options.regionFilter && node.region !== options.regionFilter) return false
    if (options.sourceFilter && node.source !== options.sourceFilter) return false
    return true
  }), [mergedNodes, options.filter, options.statusFilter, options.regionFilter, options.sourceFilter])
  const sortedNodes = useMemo(
    () => [...filteredNodes].sort((a, b) => compareManageNodes(a, b, options.sortKey, options.sortDir)),
    [filteredNodes, options.sortKey, options.sortDir],
  )
  const selectedConfigNodes = useMemo(
    () => mergedNodes.filter(node => node.configManaged && options.selectedNodes.has(node.uri)),
    [mergedNodes, options.selectedNodes],
  )

  return {
    mergedNodes,
    filteredNodes,
    sortedNodes,
    selectedConfigNodes,
    regions,
    sources,
    disabledCount: mergedNodes.filter(node => node.runtimeStatus === 'disabled').length,
    blacklistedCount: mergedNodes.filter(node => node.runtimeStatus === 'blacklisted').length,
    normalCount: mergedNodes.filter(node => node.runtimeStatus === 'normal').length,
  }
}
