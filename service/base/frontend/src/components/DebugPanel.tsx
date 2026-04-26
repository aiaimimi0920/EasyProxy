import { useState, useEffect, useCallback, useRef } from 'react'
import type { DebugResponse, DebugNode } from '../types'
import { fetchDebug } from '../api/client'
import { formatBytes } from '../utils/format'

function formatTime(isoStr: string): string {
  if (!isoStr || isoStr === '0001-01-01T00:00:00Z') return '-'
  try {
    return new Date(isoStr).toLocaleString('zh-CN', {
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    })
  } catch {
    return isoStr
  }
}

function formatLogTime(isoStr: string): string {
  if (!isoStr || isoStr === '0001-01-01T00:00:00Z') return '--:--:--'
  try {
    const d = new Date(isoStr)
    const pad = (n: number) => n.toString().padStart(2, '0')
    return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
  } catch {
    return isoStr
  }
}

export default function DebugPanel() {
  const [data, setData] = useState<DebugResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [autoRefresh, setAutoRefresh] = useState(true)
  const [filter, setFilter] = useState('')
  const [expandedNode, setExpandedNode] = useState<string | null>(null)
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const loadData = useCallback(async () => {
    try {
      setError('')
      const res = await fetchDebug()
      setData(res)
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    loadData()
  }, [loadData])

  // Auto refresh
  useEffect(() => {
    if (autoRefresh) {
      intervalRef.current = setInterval(loadData, 5000)
    }
    return () => {
      if (intervalRef.current) {
        clearInterval(intervalRef.current)
        intervalRef.current = null
      }
    }
  }, [autoRefresh, loadData])

  const filteredNodes = (data?.nodes || []).filter((node: DebugNode) => {
    if (!filter) return true
    return (
      node.name.toLowerCase().includes(filter.toLowerCase()) ||
      node.tag.toLowerCase().includes(filter.toLowerCase()) ||
      (node.last_error || '').toLowerCase().includes(filter.toLowerCase())
    )
  })

  // Sort: nodes with errors first, then by failure count desc
  const sortedNodes = [...filteredNodes].sort((a, b) => {
    if (a.blacklisted !== b.blacklisted) return a.blacklisted ? -1 : 1
    if (a.failure_count !== b.failure_count) return b.failure_count - a.failure_count
    return a.name.localeCompare(b.name)
  })

  if (loading && !data) {
    return (
      <div className="flex items-center justify-center h-64">
        <span className="loading loading-spinner loading-lg text-primary"></span>
      </div>
    )
  }

  return (
    <div className="flex flex-col min-h-full animate-in fade-in duration-500">
      {/* Header */}
      <div className="sticky top-0 z-30 bg-base-100/80 backdrop-blur-xl px-4 lg:px-8 py-4 border-b border-base-300/60 shadow-sm">
        <div className="flex flex-col md:flex-row justify-between items-start md:items-center gap-4 max-w-[1600px] mx-auto w-full">
          <div>
            <h2 className="text-2xl font-bold flex items-center gap-3">
              <div className="w-10 h-10 rounded-xl bg-primary/10 flex items-center justify-center text-primary shrink-0 border border-primary/20">
                <svg xmlns="http://www.w3.org/2000/svg" className="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2.5" d="M10 20l4-16m4 4l4 4-4 4M6 16l-4-4 4-4" />
                </svg>
              </div>
              调试面板
            </h2>
            <p className="text-sm text-base-content/50 mt-1.5 ml-[3.25rem]">运行时内部状态及错误信息分析</p>
          </div>
          <div className="flex items-center gap-3">
            <label className="flex items-center gap-2 cursor-pointer bg-base-200/50 px-3 py-1.5 rounded-lg border border-base-300/50 hover:border-base-300 transition-colors">
              <span className="text-sm font-medium">自动刷新 (5s)</span>
              <input
                type="checkbox"
                className="toggle toggle-primary toggle-sm"
                checked={autoRefresh}
                onChange={(e) => setAutoRefresh(e.target.checked)}
              />
            </label>
            <button
              className="btn btn-sm lg:btn-md btn-primary shadow-sm gap-2"
              onClick={loadData}
              disabled={loading}
            >
              {loading ? (
                <span className="loading loading-spinner loading-sm"></span>
              ) : (
                <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
                </svg>
              )}
              手动刷新
            </button>
          </div>
        </div>
      </div>

      <div className="p-4 lg:p-8 space-y-6 flex-1 pb-10 max-w-[1600px] mx-auto w-full">
        {/* Error */}
        {error && (
          <div role="alert" className="alert alert-error alert-soft text-sm">
            <span>{error}</span>
          </div>
        )}

        {/* Overview Stats */}
      {data && (
        <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
          <div className="rounded-xl bg-base-200/50 border border-base-300/30 p-4 hover:border-primary/30 transition-colors">
            <div className="text-xs text-base-content/50 mb-1">总调用数</div>
            <div className="text-2xl font-bold tabular-nums">{data.total_calls.toLocaleString()}</div>
          </div>
          <div className="rounded-xl bg-base-200/50 border border-base-300/30 p-4 hover:border-primary/30 transition-colors">
            <div className="text-xs text-base-content/50 mb-1">成功数</div>
            <div className="text-2xl font-bold tabular-nums text-success">{data.total_success.toLocaleString()}</div>
          </div>
          <div className="rounded-xl bg-base-200/50 border border-base-300/30 p-4 hover:border-primary/30 transition-colors">
            <div className="text-xs text-base-content/50 mb-1">失败数</div>
            <div className="text-2xl font-bold tabular-nums text-error">{(data.total_calls - data.total_success).toLocaleString()}</div>
          </div>
          <div className="rounded-xl bg-base-200/50 border border-base-300/30 p-4 hover:border-primary/30 transition-colors">
            <div className="text-xs text-base-content/50 mb-1">成功率</div>
            <div className={`text-2xl font-bold tabular-nums ${data.success_rate >= 90 ? 'text-success' : data.success_rate >= 70 ? 'text-warning' : 'text-error'}`}>
              {data.success_rate.toFixed(1)}%
            </div>
          </div>
        </div>
      )}

      {/* Filter */}
      <label className="input input-sm w-full">
        <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4 opacity-50" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" /></svg>
        <input
          type="text"
          placeholder="搜索节点名称、标签或错误信息..."
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        />
      </label>

      {/* Node Debug List */}
      <div className="space-y-2 max-h-[60vh] overflow-y-auto pr-1">
        {sortedNodes.length === 0 ? (
          <div className="text-center text-base-content/50 py-8">
            {filter ? '没有匹配的节点' : '暂无调试数据'}
          </div>
        ) : (
          sortedNodes.map((node: DebugNode) => (
            <div
              key={node.tag}
              className={`collapse collapse-arrow bg-base-200/60 border rounded-xl transition-colors ${node.blacklisted ? 'border-error/30' : 'border-base-300/40 hover:border-base-300/70'}`}
            >
              <input
                type="checkbox"
                checked={expandedNode === node.tag}
                onChange={() => setExpandedNode(expandedNode === node.tag ? null : node.tag)}
              />
              <div className="collapse-title flex items-center gap-3 pr-12">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-medium text-sm truncate">{node.name || node.tag}</span>
                    {node.blacklisted && <span className="badge badge-error badge-xs">黑名单</span>}
                    <span className="badge badge-ghost badge-xs">{node.mode}</span>
                  </div>
                  <div className="flex gap-4 text-xs text-base-content/60 mt-0.5 flex-wrap">
                    <span>成功: <span className="text-success">{node.success_count}</span></span>
                    <span>失败: <span className="text-error">{node.failure_count}</span></span>
                    <span>活跃: {node.active_connections}</span>
                    {node.last_latency_ms > 0 && (
                      <span>延迟: {node.last_latency_ms}ms</span>
                    )}
                    {((node.total_upload || 0) + (node.total_download || 0)) > 0 && (
                      <span className="text-blue-400/70">↑{formatBytes(node.total_upload || 0)}</span>
                    )}
                    {((node.total_upload || 0) + (node.total_download || 0)) > 0 && (
                      <span className="text-green-400/70">↓{formatBytes(node.total_download || 0)}</span>
                    )}
                  </div>
                </div>
              </div>
              <div className="collapse-content">
                <div className="pt-2 space-y-3">
                  {/* Detail Info */}
                  <div className="grid grid-cols-2 md:grid-cols-4 gap-2 text-xs">
                    <div>
                      <span className="text-base-content/50">端口:</span>{' '}
                      <span className="font-mono">{node.port || '-'}</span>
                    </div>
                    <div>
                      <span className="text-base-content/50">最后成功:</span>{' '}
                      <span>{formatTime(node.last_success)}</span>
                    </div>
                    <div>
                      <span className="text-base-content/50">最后失败:</span>{' '}
                      <span>{formatTime(node.last_failure)}</span>
                    </div>
                    <div>
                      <span className="text-base-content/50">延迟:</span>{' '}
                      <span className="font-mono">{node.last_latency_ms > 0 ? `${node.last_latency_ms}ms` : '-'}</span>
                    </div>
                    <div>
                      <span className="text-base-content/50">上传:</span>{' '}
                      <span className="font-mono text-blue-400">{formatBytes(node.total_upload || 0)}</span>
                    </div>
                    <div>
                      <span className="text-base-content/50">下载:</span>{' '}
                      <span className="font-mono text-green-400">{formatBytes(node.total_download || 0)}</span>
                    </div>
                    <div>
                      <span className="text-base-content/50">总流量:</span>{' '}
                      <span className="font-mono">{formatBytes((node.total_upload || 0) + (node.total_download || 0))}</span>
                    </div>
                  </div>

                  {/* Last Error */}
                  {node.last_error && (
                    <div className="bg-error/5 border border-error/20 rounded-lg p-2.5">
                      <span className="text-xs font-semibold text-error">最后错误: </span>
                      <span className="text-xs text-error/70 font-mono break-all">{node.last_error}</span>
                    </div>
                  )}

                  {/* Timeline */}
                  {node.timeline && node.timeline.length > 0 && (
                    <div>
                      <h4 className="text-xs font-medium mb-2 text-base-content/70">
                        运行日志 (最近 {node.timeline.length} 条)
                      </h4>
                      <div className="bg-[#1a1b26] rounded-lg p-3 overflow-x-auto max-h-56 overflow-y-auto font-mono text-xs leading-5 space-y-px">
                        {node.timeline.map((evt, i) => {
                          const ts = formatLogTime(evt.time)
                          const isOk = evt.success
                          const isTraffic = !!evt.destination
                          return (
                            <div key={i} className="whitespace-nowrap">
                              <span className="text-gray-500">{ts}</span>
                              {' '}
                              {isOk ? (
                                <span className="text-green-400">INFO </span>
                              ) : (
                                <span className="text-red-400">ERROR</span>
                              )}
                              {' '}
                              <span className="text-blue-300">[{node.name || node.tag}]</span>
                              {' '}
                              {isTraffic ? (
                                /* 流量事件：显示目标地址 */
                                isOk ? (
                                  <>
                                    <span className="text-gray-300">→ </span>
                                    <span className="text-cyan-300">{evt.destination}</span>
                                    <span className="text-green-300 ml-1">✓</span>
                                  </>
                                ) : (
                                  <>
                                    <span className="text-gray-300">→ </span>
                                    <span className="text-cyan-300">{evt.destination}</span>
                                    <span className="text-red-300 ml-1">✗</span>
                                    {evt.error && (
                                      <span className="text-red-400/70 ml-1">— {evt.error}</span>
                                    )}
                                  </>
                                )
                              ) : (
                                /* 探测事件：显示延迟 */
                                isOk ? (
                                  <>
                                    <span className="text-gray-300">probe </span>
                                    <span className="text-green-300">✓ success</span>
                                    {evt.latency_ms > 0 && (
                                      <span className={`ml-1 ${evt.latency_ms > 1000 ? 'text-yellow-400' : evt.latency_ms > 500 ? 'text-yellow-300' : 'text-gray-400'}`}>
                                        ({evt.latency_ms}ms)
                                      </span>
                                    )}
                                  </>
                                ) : (
                                  <>
                                    <span className="text-gray-300">probe </span>
                                    <span className="text-red-300">✗ failed</span>
                                    {evt.latency_ms > 0 && (
                                      <span className="text-gray-500 ml-1">({evt.latency_ms}ms)</span>
                                    )}
                                    {evt.error && (
                                      <span className="text-red-400/70 ml-1">— {evt.error}</span>
                                    )}
                                  </>
                                )
                              )}
                            </div>
                          )
                        })}
                      </div>
                    </div>
                  )}
                </div>
              </div>
            </div>
          ))
        )}
      </div>

      {/* Auto-refresh indicator */}
      {autoRefresh && (
        <div className="text-center text-xs text-base-content/30 flex items-center justify-center gap-2">
          <span className="w-1.5 h-1.5 rounded-full bg-success animate-pulse"></span>
          每 5 秒自动刷新 · {data?.nodes.length || 0} 个节点
        </div>
      )}
      </div>
    </div>
  )
}
