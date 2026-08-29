import type { StatusFilter } from './managePanelModel'
import { regionFlag, sourceLabel } from './managePanelModel'

interface ManageHeaderProps {
  total: number
  normalCount: number
  blacklistedCount: number
  disabledCount: number
  needReload: boolean
  onCreate: () => void
  onImport: () => void
  onExport: () => void
  onReload: () => void
}

export function ManageHeader(props: ManageHeaderProps) {
  return (
    <div className="sticky top-0 z-30 bg-base-100/80 backdrop-blur-xl px-4 lg:px-8 py-4 border-b border-base-300/60 shadow-sm">
      <div className="flex flex-col md:flex-row justify-between items-start md:items-center gap-4 max-w-[1600px] mx-auto w-full">
        <div>
          <h2 className="text-2xl font-bold flex items-center gap-3">
            <div className="w-10 h-10 rounded-xl bg-primary/10 flex items-center justify-center text-primary shrink-0 border border-primary/20">
              <svg xmlns="http://www.w3.org/2000/svg" className="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2.5" d="M4 6h16M4 10h16M4 14h16M4 18h16" />
              </svg>
            </div>
            节点管理
          </h2>
          <div className="text-sm font-medium text-base-content/50 mt-1.5 ml-[3.25rem] flex items-center gap-2">
            <span>共 <strong className="text-base-content/80">{props.total}</strong> 个节点</span>
            {props.normalCount > 0 && <span className="badge badge-success badge-xs border-none bg-success/15 text-success">正常 {props.normalCount}</span>}
            {props.blacklistedCount > 0 && <span className="badge badge-error badge-xs border-none bg-error/15 text-error">黑名单 {props.blacklistedCount}</span>}
            {props.disabledCount > 0 && <span className="badge badge-ghost badge-xs bg-base-200 text-base-content/50">禁用 {props.disabledCount}</span>}
          </div>
        </div>
        <div className="flex items-center gap-2">
          <button className="btn btn-sm lg:btn-md btn-primary shadow-sm gap-2" onClick={props.onCreate}>
            <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2.5" d="M12 4v16m8-8H4" /></svg>
            添加节点
          </button>
          <div className="dropdown dropdown-end">
            <div tabIndex={0} role="button" className="btn btn-ghost border border-base-300 btn-sm lg:btn-md gap-2 shadow-sm">
              管理操作
              <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4 opacity-50" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M19 9l-7 7-7-7" /></svg>
            </div>
            <ul tabIndex={0} className="dropdown-content menu bg-base-100 border border-base-200 rounded-xl z-20 w-48 p-2 shadow-xl mt-2">
              <li><a onClick={props.onImport} className="hover:bg-primary/10 hover:text-primary gap-3"><svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-8l-4-4m0 0L8 8m4-4v12" /></svg> 导入节点配置</a></li>
              <li><a onClick={props.onExport} className="hover:bg-primary/10 hover:text-primary gap-3"><svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4" /></svg> 导出所有节点</a></li>
            </ul>
          </div>
          {props.needReload && (
            <button className="btn btn-warning btn-sm lg:btn-md shadow-sm gap-2 animate-pulse" onClick={props.onReload}>
              <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" /></svg>
              重载生效
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

export function ManageAlerts({ error, success, needReload, onClearError }: {
  error: string
  success: string
  needReload: boolean
  onClearError: () => void
}) {
  return (
    <>
      {error && (
        <div role="alert" className="alert alert-error alert-soft text-sm">
          <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M10 14l2-2m0 0l2-2m-2 2l-2-2m2 2l2 2m7-2a9 9 0 11-18 0 9 9 0 0118 0z" /></svg>
          <span>{error}</span>
          <button className="btn btn-ghost btn-xs" onClick={onClearError}>✕</button>
        </div>
      )}
      {success && (
        <div role="alert" className="alert alert-success alert-soft text-sm">
          <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z" /></svg>
          <span>{success}</span>
        </div>
      )}
      {needReload && (
        <div role="alert" className="alert alert-warning alert-soft text-sm">
          <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-2.5L13.732 4c-.77-.833-1.964-.833-2.732 0L4.082 16.5c-.77.833.192 2.5 1.732 2.5z" /></svg>
          <span>配置已变更，请点击「重载配置」使其生效</span>
        </div>
      )}
    </>
  )
}

interface ManageFiltersProps {
  filter: string
  statusFilter: StatusFilter
  regionFilter: string
  sourceFilter: string
  regions: string[]
  sources: string[]
  onFilterChange: (value: string) => void
  onStatusChange: (value: StatusFilter) => void
  onRegionChange: (value: string) => void
  onSourceChange: (value: string) => void
}

export function ManageFilters(props: ManageFiltersProps) {
  return (
    <div className="bg-base-100 border border-base-300/50 rounded-2xl p-4 shadow-sm">
      <div className="flex flex-col lg:flex-row gap-4 items-center">
        <div className="relative flex-1 w-full">
          <div className="absolute inset-y-0 left-0 pl-3.5 flex items-center pointer-events-none text-base-content/40">
            <svg xmlns="http://www.w3.org/2000/svg" className="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" /></svg>
          </div>
          <input type="text" className="input input-md w-full pl-11 bg-base-200/50 focus:bg-base-100 transition-colors focus:border-primary/50" placeholder="搜索节点名称、URI 或 地区..." value={props.filter} onChange={event => props.onFilterChange(event.target.value)} />
        </div>
        <div className="flex flex-wrap sm:flex-nowrap gap-3 w-full lg:w-auto">
          <select className="select select-md bg-base-200/50 focus:bg-base-100 flex-1 sm:w-36" value={props.statusFilter} onChange={event => props.onStatusChange(event.target.value as StatusFilter)}>
            <option value="">全部状态</option><option value="normal">✅ 正常运行</option><option value="unavailable">❌ 不可用</option><option value="blacklisted">🔴 黑名单</option><option value="pending">⚠️ 待检查</option><option value="disabled">🚫 已禁用</option>
          </select>
          {props.regions.length > 0 && (
            <select className="select select-md bg-base-200/50 focus:bg-base-100 flex-1 sm:w-32" value={props.regionFilter} onChange={event => props.onRegionChange(event.target.value)}>
              <option value="">全部地区</option>
              {props.regions.map(region => <option key={region} value={region}>{regionFlag(region)} {region.toUpperCase()}</option>)}
            </select>
          )}
          {props.sources.length > 1 && (
            <select className="select select-md bg-base-200/50 focus:bg-base-100 flex-1 sm:w-32" value={props.sourceFilter} onChange={event => props.onSourceChange(event.target.value)}>
              <option value="">全部来源</option>
              {props.sources.map(source => <option key={source} value={source}>{sourceLabel(source)}</option>)}
            </select>
          )}
        </div>
      </div>
    </div>
  )
}

export function ManageBatchBar({ selectedCount, selectedConfigCount, processing, progress, onProbe, onToggle, onDelete, onClear }: {
  selectedCount: number
  selectedConfigCount: number
  processing: boolean
  progress: { current: number; total: number } | null
  onProbe: () => void
  onToggle: (enabled: boolean) => void
  onDelete: () => void
  onClear: () => void
}) {
  return (
    <div className={`transition-all duration-300 overflow-hidden ${selectedCount > 0 ? 'max-h-24 opacity-100' : 'max-h-0 opacity-0'}`}>
      <div className="flex flex-col gap-3 px-5 py-4 bg-primary/5 border border-primary/20 rounded-2xl shadow-inner relative">
        <div className="absolute left-0 top-0 bottom-0 w-1.5 bg-primary rounded-l-2xl"></div>
        <div className="flex items-center gap-4 flex-wrap">
          <span className="text-base font-medium text-base-content/80 flex items-center gap-2"><span className="badge badge-primary badge-md font-bold">{selectedCount}</span> 项已选择</span>
          <div className="flex gap-2 ml-auto flex-wrap">
            <button className="btn btn-sm btn-primary shadow-sm gap-1.5" onClick={onProbe} disabled={processing} title="对选中的已启用节点逐个探测">
              {progress ? <><span className="loading loading-spinner loading-xs"></span> {progress.current}/{progress.total}</> : <><svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M13 10V3L4 14h7v7l9-11h-7z" /></svg> 批量探测</>}
            </button>
            <div className="w-px h-6 bg-base-300 mx-1 self-center"></div>
            <button className="btn btn-sm btn-success border-none bg-success/15 text-success hover:bg-success hover:text-success-content" onClick={() => onToggle(true)} disabled={processing || selectedConfigCount === 0}>启用</button>
            <button className="btn btn-sm btn-warning border-none bg-warning/15 text-warning-content hover:bg-warning hover:text-warning-content" onClick={() => onToggle(false)} disabled={processing || selectedConfigCount === 0}>禁用</button>
            <button className="btn btn-sm btn-error border-none bg-error/15 text-error hover:bg-error hover:text-error-content" onClick={onDelete} disabled={processing || selectedConfigCount === 0}>删除</button>
            <div className="w-px h-6 bg-base-300 mx-1 self-center"></div>
            <button className="btn btn-sm btn-ghost hover:bg-base-300" onClick={onClear} disabled={processing}>取消选择</button>
          </div>
        </div>
        {progress && <progress className="progress progress-primary w-full h-1.5 bg-primary/20" value={progress.current} max={progress.total}></progress>}
      </div>
    </div>
  )
}
