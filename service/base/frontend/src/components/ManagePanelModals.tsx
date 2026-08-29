import type { ChangeEvent, FormEvent } from 'react'
import type { ConfigNodePayload } from '../types'

export interface ManagePanelModalsProps {
  modalOpen: boolean
  editingNode: string | null
  editingNodeLabel: string | null
  form: ConfigNodePayload
  formError: string
  submitting: boolean
  onFormChange: (form: ConfigNodePayload) => void
  onSubmit: (event: FormEvent) => void
  onCloseEdit: () => void
  importModalOpen: boolean
  importContent: string
  importing: boolean
  importError: string
  importResult: { message: string; imported: number; errors?: string[] } | null
  onFileImport: (event: ChangeEvent<HTMLInputElement>) => void
  onImportContentChange: (content: string) => void
  onImport: () => void
  onCloseImport: () => void
  deleteTarget: string | null
  deleteTargetLabel: string | null
  deleting: boolean
  onDelete: () => void
  onCloseDelete: () => void
  batchDeleteConfirm: boolean
  selectedConfigCount: number
  batchProcessing: boolean
  onBatchDelete: () => void
  onCloseBatchDelete: () => void
}

export default function ManagePanelModals(props: ManagePanelModalsProps) {
  return (
    <>
      {props.modalOpen && (
        <div className="modal modal-open">
          <div className="modal-box">
            <h3 className="font-bold text-xl mb-4">{props.editingNodeLabel ? `编辑节点: ${props.editingNodeLabel}` : '添加节点'}</h3>
            <form onSubmit={props.onSubmit}>
              {props.formError && <div className="alert alert-error mb-3 py-2 text-sm"><span>{props.formError}</span></div>}
              <fieldset className="fieldset mb-3">
                <legend className="fieldset-legend">名称 *</legend>
                <input type="text" className="input input-sm w-full" placeholder="节点名称" value={props.form.name} onChange={event => props.onFormChange({ ...props.form, name: event.target.value })} disabled={Boolean(props.editingNode)} />
              </fieldset>
              <fieldset className="fieldset mb-3">
                <legend className="fieldset-legend">URI *</legend>
                <input type="text" className="input input-sm w-full font-mono text-xs" placeholder="trojan://password@host:port?..." value={props.form.uri} onChange={event => props.onFormChange({ ...props.form, uri: event.target.value })} />
              </fieldset>
              <fieldset className="fieldset mb-3">
                <legend className="fieldset-legend">本地代理端口</legend>
                <input type="number" className="input input-sm w-full" placeholder="0 = 自动分配" value={props.form.port || ''} onChange={event => props.onFormChange({ ...props.form, port: parseInt(event.target.value) || 0 })} min={0} max={65535} />
              </fieldset>
              <div className="grid grid-cols-2 gap-3 mb-4">
                <fieldset className="fieldset">
                  <legend className="fieldset-legend">用户名</legend>
                  <input type="text" className="input input-sm w-full" placeholder="可选" value={props.form.username} onChange={event => props.onFormChange({ ...props.form, username: event.target.value })} />
                </fieldset>
                <fieldset className="fieldset">
                  <legend className="fieldset-legend">密码</legend>
                  <input type="text" className="input input-sm w-full" placeholder="可选" value={props.form.password} onChange={event => props.onFormChange({ ...props.form, password: event.target.value })} />
                </fieldset>
              </div>
              <div className="modal-action">
                <button type="button" className="btn btn-ghost" onClick={props.onCloseEdit}>取消</button>
                <button type="submit" className="btn btn-primary" disabled={props.submitting}>
                  {props.submitting ? <span className="loading loading-spinner loading-xs"></span> : (props.editingNode ? '更新' : '添加')}
                </button>
              </div>
            </form>
          </div>
          <form method="dialog" className="modal-backdrop" onClick={props.onCloseEdit}><button>close</button></form>
        </div>
      )}

      {props.importModalOpen && (
        <div className="modal modal-open">
          <div className="modal-box max-w-2xl">
            <h3 className="font-bold text-xl mb-4">导入节点</h3>
            {props.importError && <div className="alert alert-error mb-3 py-2 text-sm"><span>{props.importError}</span></div>}
            {props.importResult && (
              <div className={`alert mb-3 py-2 text-sm ${props.importResult.imported > 0 ? 'alert-success' : 'alert-warning'}`}>
                <div>
                  <span>{props.importResult.message}</span>
                  {props.importResult.errors && props.importResult.errors.length > 0 && (
                    <details className="mt-2">
                      <summary className="cursor-pointer text-xs opacity-70">{props.importResult.errors.length} 个错误</summary>
                      <ul className="text-xs mt-1 space-y-0.5">{props.importResult.errors.map((error, index) => <li key={index} className="opacity-70">• {error}</li>)}</ul>
                    </details>
                  )}
                </div>
              </div>
            )}
            <p className="text-sm text-base-content/60 mb-3">
              每行一个代理 URI（支持 trojan://、vless://、vmess://、ss://、hysteria2:// 等），可以直接粘贴导出文件的内容或从文件导入。
            </p>
            <div className="mb-3">
              <label className="btn btn-soft btn-sm">
                <svg xmlns="http://www.w3.org/2000/svg" className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M7 16a4 4 0 01-.88-7.903A5 5 0 1115.9 6L16 6a5 5 0 011 9.9M15 13l-3-3m0 0l-3 3m3-3v12" /></svg>
                选择文件
                <input type="file" accept=".txt,.conf,.list" className="hidden" onChange={props.onFileImport} />
              </label>
            </div>
            <textarea className="textarea textarea-bordered w-full font-mono text-xs h-48" placeholder={'trojan://password@host:port?sni=example.com#节点名称\nvless://uuid@host:port?encryption=none#另一个节点\n...'} value={props.importContent} onChange={event => props.onImportContentChange(event.target.value)} />
            <div className="text-xs text-base-content/40 mt-1">
              {props.importContent.trim() ? `${props.importContent.trim().split('\n').filter(line => line.trim() && !line.trim().startsWith('#')).length} 行有效内容` : '等待输入...'}
            </div>
            <div className="modal-action">
              <button type="button" className="btn btn-ghost" onClick={props.onCloseImport}>{props.importResult?.imported ? '完成' : '取消'}</button>
              <button type="button" className="btn btn-primary" onClick={props.onImport} disabled={props.importing || !props.importContent.trim()}>{props.importing ? <span className="loading loading-spinner loading-xs"></span> : '导入'}</button>
            </div>
          </div>
          <form method="dialog" className="modal-backdrop" onClick={() => !props.importing && props.onCloseImport()}><button>close</button></form>
        </div>
      )}

      {props.deleteTarget && (
        <div className="modal modal-open">
          <div className="modal-box max-w-sm">
            <h3 className="font-bold text-lg mb-2">确认删除</h3>
            <p className="text-base-content/70">确定要删除节点 <strong>{props.deleteTargetLabel || props.deleteTarget}</strong> 吗？此操作不可撤销。</p>
            <div className="modal-action">
              <button className="btn btn-ghost" onClick={props.onCloseDelete} disabled={props.deleting}>取消</button>
              <button className="btn btn-error" onClick={props.onDelete} disabled={props.deleting}>{props.deleting ? <span className="loading loading-spinner loading-xs"></span> : '删除'}</button>
            </div>
          </div>
          <form method="dialog" className="modal-backdrop" onClick={() => !props.deleting && props.onCloseDelete()}><button>close</button></form>
        </div>
      )}

      {props.batchDeleteConfirm && (
        <div className="modal modal-open">
          <div className="modal-box max-w-sm">
            <h3 className="font-bold text-lg mb-2">确认批量删除</h3>
            <p className="text-base-content/70">确定要删除选中的 <strong>{props.selectedConfigCount}</strong> 个配置节点吗？此操作不可撤销。</p>
            <div className="modal-action">
              <button className="btn btn-ghost" onClick={props.onCloseBatchDelete} disabled={props.batchProcessing}>取消</button>
              <button className="btn btn-error" onClick={props.onBatchDelete} disabled={props.batchProcessing}>{props.batchProcessing ? <span className="loading loading-spinner loading-xs"></span> : `删除 ${props.selectedConfigCount} 个节点`}</button>
            </div>
          </div>
          <form method="dialog" className="modal-backdrop" onClick={() => !props.batchProcessing && props.onCloseBatchDelete()}><button>close</button></form>
        </div>
      )}
    </>
  )
}
