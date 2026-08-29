import type { ManageSortKey, MergedNode, SortDir } from './managePanelModel'
import { latencyColor, regionFlag, sourceLabel } from './managePanelModel'

function SortIcon({ active, dir }: { active: boolean; dir: SortDir }) {
  if (!active) {
    return <svg xmlns="http://www.w3.org/2000/svg" className="h-3 w-3 opacity-30 ml-0.5 inline" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M7 16V4m0 0L3 8m4-4l4 4m6 0v12m0 0l4-4m-4 4l-4-4" /></svg>
  }
  return dir === 'asc'
    ? <svg xmlns="http://www.w3.org/2000/svg" className="h-3 w-3 opacity-70 ml-0.5 inline" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M5 15l7-7 7 7" /></svg>
    : <svg xmlns="http://www.w3.org/2000/svg" className="h-3 w-3 opacity-70 ml-0.5 inline" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M19 9l-7 7-7-7" /></svg>
}

function StatusBadge({ status }: { status: MergedNode['runtimeStatus'] }) {
  switch (status) {
    case 'normal': return <span className="badge badge-success badge-sm border-none bg-success/15 text-success font-medium flex gap-1 items-center px-2 py-3.5"><div className="w-1.5 h-1.5 rounded-full bg-success"></div>正常</span>
    case 'unavailable': return <span className="badge badge-error badge-sm border-none bg-error/15 text-error font-medium flex gap-1 items-center px-2 py-3.5"><div className="w-1.5 h-1.5 rounded-full bg-error"></div>不可用</span>
    case 'blacklisted': return <span className="badge badge-error badge-sm border-none bg-error/30 text-error font-bold flex gap-1 items-center px-2 py-3.5"><svg xmlns="http://www.w3.org/2000/svg" className="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2.5" d="M18.364 18.364A9 9 0 005.636 5.636m12.728 12.728A9 9 0 015.636 5.636m12.728 12.728L5.636 5.636" /></svg>黑名单</span>
    case 'pending': return <span className="badge badge-warning badge-sm border-none bg-warning/15 text-warning-content font-medium flex gap-1 items-center px-2 py-3.5"><div className="w-1.5 h-1.5 rounded-full bg-warning animate-pulse"></div>待检查</span>
    case 'disabled': return <span className="badge badge-ghost badge-sm border-none bg-base-300/50 text-base-content/50 font-medium px-2 py-3.5">已禁用</span>
    default: return <span className="badge badge-ghost badge-sm border-none px-2 py-3.5">未知</span>
  }
}

interface ManageNodeTableProps {
  nodes: MergedNode[]
  selectedNodes: Set<string>
  sortKey: ManageSortKey
  sortDir: SortDir
  hasFilters: boolean
  probingTag: string | null
  toggling: string | null
  onSort: (key: ManageSortKey) => void
  onToggleSelectAll: () => void
  onToggleSelectNode: (uri: string) => void
  onProbe: (tag: string) => void
  onRelease: (tag: string) => void
  onToggle: (node: MergedNode) => void
  onEdit: (node: MergedNode) => void
  onDelete: (node: MergedNode) => void
}

export default function ManageNodeTable(props: ManageNodeTableProps) {
  const thClass = 'font-semibold cursor-pointer select-none hover:text-primary transition-colors'
  return (
    <div className="rounded-2xl border border-base-300/50 bg-base-100 shadow-sm overflow-hidden">
      <div className="overflow-x-auto overflow-y-auto max-h-[calc(100vh-280px)] min-h-[400px]">
        <table className="table table-md table-pin-rows">
          <thead>
            <tr className="bg-base-200/50 border-b border-base-300/50 shadow-sm text-base-content/70">
              <th className="w-8">
                <input
                  type="checkbox"
                  className="checkbox checkbox-xs"
                  checked={props.nodes.length > 0 && props.selectedNodes.size === props.nodes.length}
                  onChange={props.onToggleSelectAll}
                  ref={element => {
                    if (element) element.indeterminate = props.selectedNodes.size > 0 && props.selectedNodes.size < props.nodes.length
                  }}
                />
              </th>
              <th className={thClass} onClick={() => props.onSort('name')}>名称 <SortIcon active={props.sortKey === 'name'} dir={props.sortDir} /></th>
              <th className={thClass} onClick={() => props.onSort('status')}>状态 <SortIcon active={props.sortKey === 'status'} dir={props.sortDir} /></th>
              <th className={thClass} onClick={() => props.onSort('latency')}>延迟 <SortIcon active={props.sortKey === 'latency'} dir={props.sortDir} /></th>
              <th className={`hidden md:table-cell ${thClass}`} onClick={() => props.onSort('region')}>区域 <SortIcon active={props.sortKey === 'region'} dir={props.sortDir} /></th>
              <th className={`hidden md:table-cell ${thClass}`} onClick={() => props.onSort('port')}>端口 <SortIcon active={props.sortKey === 'port'} dir={props.sortDir} /></th>
              <th className={`hidden lg:table-cell ${thClass}`} onClick={() => props.onSort('source')}>来源 <SortIcon active={props.sortKey === 'source'} dir={props.sortDir} /></th>
              <th className="font-semibold">操作</th>
            </tr>
          </thead>
          <tbody>
            {props.nodes.length === 0 ? (
              <tr>
                <td colSpan={8} className="h-[300px] p-0">
                  <div className="flex flex-col items-center justify-center h-full w-full opacity-60">
                    <div className="w-16 h-16 bg-base-200 rounded-full flex items-center justify-center mb-4">
                      <svg xmlns="http://www.w3.org/2000/svg" className="h-8 w-8 text-base-content/40" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.5" d="M20 13V6a2 2 0 00-2-2H6a2 2 0 00-2 2v7m16 0v5a2 2 0 01-2 2H6a2 2 0 01-2-2v-5m16 0h-2.586a1 1 0 00-.707.293l-2.414 2.414a1 1 0 01-.707.293h-3.172a1 1 0 01-.707-.293l-2.414-2.414A1 1 0 006.586 13H4" /></svg>
                    </div>
                    <p className="text-base font-medium text-base-content">{props.hasFilters ? '未找到匹配的节点数据' : '暂无节点'}</p>
                    {!props.hasFilters && <p className="text-sm text-base-content/50 mt-1">请点击右上角「添加节点」或导入配置以开始</p>}
                  </div>
                </td>
              </tr>
            ) : props.nodes.map(node => (
              <tr
                key={node.uri}
                className={`transition-colors border-b border-base-200/50 last:border-none group ${node.runtimeStatus === 'disabled' ? 'opacity-50 grayscale-[0.5]' : ''} ${node.runtimeStatus === 'blacklisted' ? 'opacity-80' : ''} ${props.selectedNodes.has(node.uri) ? 'bg-primary/5' : 'hover:bg-base-200/40'}`}
              >
                <td className="w-8"><input type="checkbox" className="checkbox checkbox-sm" checked={props.selectedNodes.has(node.uri)} onChange={() => props.onToggleSelectNode(node.uri)} /></td>
                <td><div className="font-semibold text-sm flex items-center gap-2">{node.region && <span className="text-lg leading-none filter drop-shadow-sm">{regionFlag(node.region)}</span>}<span className="truncate max-w-[200px]" title={node.name}>{node.name}</span></div></td>
                <td><StatusBadge status={node.runtimeStatus} /></td>
                <td className={`font-mono text-sm font-medium ${latencyColor(node.latency_ms)}`}>{node.latency_ms < 0 ? <span className="text-base-content/30">-</span> : `${node.latency_ms} ms`}</td>
                <td className="hidden md:table-cell text-sm text-base-content/70">{node.country || node.region ? <div className="badge badge-ghost badge-sm">{node.country || node.region}</div> : '-'}</td>
                <td className="hidden md:table-cell font-mono text-sm text-base-content/70">{node.port || '-'}</td>
                <td className="hidden lg:table-cell"><div className="badge badge-ghost badge-sm opacity-70 bg-transparent border-base-300">{sourceLabel(node.source)}</div></td>
                <td>
                  <div className="flex gap-1.5 opacity-60 group-hover:opacity-100 transition-opacity">
                    {!node.disabled && node.tag && (
                      <button className="btn btn-sm btn-square btn-ghost text-primary hover:bg-primary/10" onClick={() => props.onProbe(node.tag!)} disabled={props.probingTag === node.tag} title="探测延迟">
                        {props.probingTag === node.tag ? <span className="loading loading-spinner loading-xs"></span> : <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M13 10V3L4 14h7v7l9-11h-7z" /></svg>}
                      </button>
                    )}
                    {node.runtimeStatus === 'blacklisted' && node.tag && (
                      <button className="btn btn-sm btn-square btn-ghost text-warning hover:bg-warning/10" onClick={() => props.onRelease(node.tag!)} title="解除黑名单">
                        <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M8 11V7a4 4 0 118 0m-4 8v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2z" /></svg>
                      </button>
                    )}
                    {node.configManaged && (
                      <>
                        <button className={`btn btn-sm btn-square btn-ghost ${node.disabled ? 'text-success hover:bg-success/10' : 'text-warning hover:bg-warning/10'}`} onClick={() => props.onToggle(node)} disabled={props.toggling === node.uri} title={node.disabled ? '启用该节点' : '禁用该节点'}>
                          {props.toggling === node.uri ? <span className="loading loading-spinner loading-xs"></span> : node.disabled ? <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M5 13l4 4L19 7" /></svg> : <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M18.364 18.364A9 9 0 005.636 5.636m12.728 12.728A9 9 0 015.636 5.636m12.728 12.728L5.636 5.636" /></svg>}
                        </button>
                        <button className="btn btn-sm btn-square btn-ghost text-info hover:bg-info/10" onClick={() => props.onEdit(node)} title="编辑节点配置"><svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 00-2-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z" /></svg></button>
                        <button className="btn btn-sm btn-square btn-ghost text-error hover:bg-error/10" onClick={() => props.onDelete(node)} title="删除节点"><svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" /></svg></button>
                      </>
                    )}
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
