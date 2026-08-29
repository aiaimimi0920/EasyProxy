import { useState, useEffect, useCallback } from 'react'
import type { ConfigNodeConfig, ConfigNodePayload, NodesResponse } from '../types'
import {
  fetchConfigNodes, createConfigNode, updateConfigNode, deleteConfigNode,
  toggleConfigNode, batchToggleConfigNodes, batchDeleteConfigNodes, triggerReload,
  importNodes, exportProxies,
  fetchNodes, probeNode, releaseNode,
} from '../api/client'
import ManageNodeTable from './ManageNodeTable'
import { ManageAlerts, ManageBatchBar, ManageFilters, ManageHeader } from './ManagePanelControls'
import ManagePanelModals from './ManagePanelModals'
import {
  emptyPayload,
  useManageNodeView,
  type ManageSortKey,
  type MergedNode,
  type SortDir,
  type StatusFilter,
} from './managePanelModel'

export default function ManagePanel() {
  const [configNodes, setConfigNodes] = useState<ConfigNodeConfig[]>([])
  const [monitorData, setMonitorData] = useState<NodesResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [needReload, setNeedReload] = useState(false)

  // Modal state
  const [modalOpen, setModalOpen] = useState(false)
  const [editingNode, setEditingNode] = useState<string | null>(null)
  const [editingNodeLabel, setEditingNodeLabel] = useState<string | null>(null)
  const [form, setForm] = useState<ConfigNodePayload>(emptyPayload)
  const [formError, setFormError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  // Delete confirm
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null)
  const [deleteTargetLabel, setDeleteTargetLabel] = useState<string | null>(null)
  const [deleting, setDeleting] = useState(false)

  // Toggle state
  const [toggling, setToggling] = useState<string | null>(null)

  // Probe state
  const [probingTag, setProbingTag] = useState<string | null>(null)

  // Batch selection
  const [selectedNodes, setSelectedNodes] = useState<Set<string>>(new Set())
  const [batchProcessing, setBatchProcessing] = useState(false)
  const [batchDeleteConfirm, setBatchDeleteConfirm] = useState(false)
  const [batchProbeProgress, setBatchProbeProgress] = useState<{ current: number; total: number } | null>(null)

  // Filters
  const [filter, setFilter] = useState('')
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('')
  const [regionFilter, setRegionFilter] = useState('')
  const [sourceFilter, setSourceFilter] = useState('')

  // Sort
  const [sortKey, setSortKey] = useState<ManageSortKey>('name')
  const [sortDir, setSortDir] = useState<SortDir>('asc')

  // Import state
  const [importModalOpen, setImportModalOpen] = useState(false)
  const [importContent, setImportContent] = useState('')
  const [importing, setImporting] = useState(false)
  const [importError, setImportError] = useState('')
  const [importResult, setImportResult] = useState<{ message: string; imported: number; errors?: string[] } | null>(null)

  // ---- Data loading ----

  const loadData = useCallback(async () => {
    try {
      setError('')
      const [configRes, monitorRes] = await Promise.all([
        fetchConfigNodes(),
        fetchNodes().catch(() => null), // monitor data is optional
      ])
      setConfigNodes(configRes.nodes || [])
      if (monitorRes) setMonitorData(monitorRes)
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载节点失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    loadData()
  }, [loadData])

  useEffect(() => {
    if (success) {
      const timer = setTimeout(() => setSuccess(''), 5000)
      return () => clearTimeout(timer)
    }
  }, [success])

  const {
    mergedNodes,
    filteredNodes,
    sortedNodes,
    selectedConfigNodes,
    regions,
    sources,
    disabledCount,
    blacklistedCount,
    normalCount,
  } = useManageNodeView({
    configNodes,
    monitorData,
    filter,
    statusFilter,
    regionFilter,
    sourceFilter,
    sortKey,
    sortDir,
    selectedNodes,
  })

  // ---- Handlers ----

  const handleSort = (key: ManageSortKey) => {
    if (sortKey === key) {
      setSortDir(d => d === 'asc' ? 'desc' : 'asc')
    } else {
      setSortKey(key)
      setSortDir('asc')
    }
  }

  const openCreateModal = () => {
    setEditingNode(null)
    setEditingNodeLabel(null)
    setForm(emptyPayload)
    setFormError('')
    setModalOpen(true)
  }

  const openEditModal = (node: MergedNode) => {
    setEditingNode(node.uri)
    setEditingNodeLabel(node.name)
    setForm({
      name: node.name,
      uri: node.uri,
      port: node.port,
      username: node.username || '',
      password: node.password || '',
    })
    setFormError('')
    setModalOpen(true)
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!form.name.trim()) { setFormError('节点名称不能为空'); return }
    if (!form.uri.trim()) { setFormError('URI 不能为空'); return }

    setSubmitting(true)
    setFormError('')
    try {
      if (editingNode) {
        const res = await updateConfigNode(editingNode, form)
        setSuccess(res.message || '节点已更新')
      } else {
        const res = await createConfigNode(form)
        setSuccess(res.message || '节点已添加')
      }
      setNeedReload(true)
      setModalOpen(false)
      await loadData()
    } catch (err) {
      setFormError(err instanceof Error ? err.message : '操作失败')
    } finally {
      setSubmitting(false)
    }
  }

  const handleDelete = async () => {
    if (!deleteTarget) return
    setDeleting(true)
    try {
      const res = await deleteConfigNode(deleteTarget)
      setSuccess(res.message || '节点已删除')
      setNeedReload(true)
      setDeleteTarget(null)
      setDeleteTargetLabel(null)
      await loadData()
    } catch (err) {
      setError(err instanceof Error ? err.message : '删除失败')
    } finally {
      setDeleting(false)
    }
  }

  const handleToggle = async (node: MergedNode) => {
    const newEnabled = !!node.disabled
    setToggling(node.uri)
    try {
      const res = await toggleConfigNode(node.uri, newEnabled)
      setSuccess(res.message || (newEnabled ? '节点已启用' : '节点已禁用'))
      await loadData()
    } catch (err) {
      setError(err instanceof Error ? err.message : '操作失败')
    } finally {
      setToggling(null)
    }
  }

  const handleProbe = async (tag: string) => {
    setProbingTag(tag)
    try {
      await probeNode(tag)
      await loadData()
    } catch (err) {
      setError(err instanceof Error ? err.message : '探测失败')
    } finally {
      setProbingTag(null)
    }
  }

  const handleRelease = async (tag: string) => {
    try {
      await releaseNode(tag)
      setSuccess('已解除黑名单')
      await loadData()
    } catch (err) {
      setError(err instanceof Error ? err.message : '解除失败')
    }
  }

  // ---- Batch ----

  const toggleSelectNode = (ref: string) => {
    setSelectedNodes(prev => {
      const next = new Set(prev)
      if (next.has(ref)) next.delete(ref)
      else next.add(ref)
      return next
    })
  }

  const toggleSelectAll = () => {
    if (selectedNodes.size === sortedNodes.length) {
      setSelectedNodes(new Set())
    } else {
      setSelectedNodes(new Set(sortedNodes.map(n => n.uri)))
    }
  }

  const handleBatchToggle = async (enabled: boolean) => {
    if (selectedConfigNodes.length === 0) return
    setBatchProcessing(true)
    try {
      const res = await batchToggleConfigNodes(selectedConfigNodes.map(node => node.uri), enabled)
      setSuccess(res.message || '批量操作完成')
      setSelectedNodes(new Set())
      await loadData()
    } catch (err) {
      setError(err instanceof Error ? err.message : '批量操作失败')
    } finally {
      setBatchProcessing(false)
    }
  }

  const handleBatchProbe = async () => {
      const nodesToProbe = sortedNodes.filter(n => selectedNodes.has(n.uri) && !n.disabled && n.tag)
    if (nodesToProbe.length === 0) {
      setError('所选节点中没有可探测的节点（已禁用或无运行时标识的节点将被跳过）')
      return
    }

    setBatchProcessing(true)
    setBatchProbeProgress({ current: 0, total: nodesToProbe.length })
    let successCount = 0
    let failCount = 0
    let completed = 0

    const probeOne = async (tag: string) => {
      try {
        await probeNode(tag)
        successCount++
      } catch {
        failCount++
      } finally {
        completed++
        setBatchProbeProgress({ current: completed, total: nodesToProbe.length })
      }
    }

    // Probe concurrently in batches of 10 (matching backend concurrency)
    const concurrency = 10
    for (let i = 0; i < nodesToProbe.length; i += concurrency) {
      const batch = nodesToProbe.slice(i, i + concurrency)
      await Promise.allSettled(batch.map(n => probeOne(n.tag!)))
    }

    setBatchProbeProgress(null)
    setBatchProcessing(false)
    setSuccess(`批量探测完成：${successCount} 成功，${failCount} 失败`)
    await loadData()
  }

  const handleBatchDelete = async () => {
    if (selectedConfigNodes.length === 0) return
    setBatchProcessing(true)
    setBatchDeleteConfirm(false)
    try {
      const res = await batchDeleteConfigNodes(selectedConfigNodes.map(node => node.uri))
      setSuccess(res.message || '批量删除完成')
      setSelectedNodes(new Set())
      await loadData()
    } catch (err) {
      setError(err instanceof Error ? err.message : '批量删除失败')
    } finally {
      setBatchProcessing(false)
    }
  }

  // ---- Import / Export ----

  const openImportModal = () => {
    setImportContent('')
    setImportError('')
    setImportResult(null)
    setImportModalOpen(true)
  }

  const handleFileImport = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    if (!file) return
    const reader = new FileReader()
    reader.onload = (ev) => {
      const text = ev.target?.result
      if (typeof text === 'string') setImportContent(text)
    }
    reader.readAsText(file)
    e.target.value = ''
  }

  const handleImport = async () => {
    if (!importContent.trim()) { setImportError('请输入节点 URI'); return }
    setImporting(true)
    setImportError('')
    setImportResult(null)
    try {
      const res = await importNodes(importContent)
      setImportResult(res)
      if (res.imported > 0) {
        setNeedReload(true)
        setSuccess(res.message)
        await loadData()
      }
    } catch (err) {
      setImportError(err instanceof Error ? err.message : '导入失败')
    } finally {
      setImporting(false)
    }
  }

  const handleExport = async () => {
    try {
      const text = await exportProxies()
      if (!text.trim()) { setError('没有可导出的节点'); return }
      const blob = new Blob([text], { type: 'text/plain' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = 'nodes_export.txt'
      a.click()
      URL.revokeObjectURL(url)
      setSuccess('节点已导出')
    } catch (err) {
      setError(err instanceof Error ? err.message : '导出失败')
    }
  }

  const handleReload = async () => {
    try {
      setError('')
      const res = await triggerReload()
      setSuccess(res.message || '重载成功')
      setNeedReload(false)
      await loadData()
    } catch (err) {
      setError(err instanceof Error ? err.message : '重载失败')
    }
  }

  // ---- Render ----

  if (loading) {
    return (
      <div className="flex items-center justify-center h-64">
        <span className="loading loading-spinner loading-lg text-primary"></span>
      </div>
    )
  }

  return (
    <div className="flex flex-col min-h-full animate-in fade-in duration-500">
      <ManageHeader
        total={mergedNodes.length}
        normalCount={normalCount}
        blacklistedCount={blacklistedCount}
        disabledCount={disabledCount}
        needReload={needReload}
        onCreate={openCreateModal}
        onImport={openImportModal}
        onExport={handleExport}
        onReload={handleReload}
      />
      <div className="p-4 lg:p-8 space-y-6 flex-1 max-w-[1600px] mx-auto w-full">
        <ManageAlerts error={error} success={success} needReload={needReload} onClearError={() => setError('')} />
        <ManageFilters
          filter={filter}
          statusFilter={statusFilter}
          regionFilter={regionFilter}
          sourceFilter={sourceFilter}
          regions={regions}
          sources={sources}
          onFilterChange={setFilter}
          onStatusChange={setStatusFilter}
          onRegionChange={setRegionFilter}
          onSourceChange={setSourceFilter}
        />
        <ManageBatchBar
          selectedCount={selectedNodes.size}
          selectedConfigCount={selectedConfigNodes.length}
          processing={batchProcessing}
          progress={batchProbeProgress}
          onProbe={handleBatchProbe}
          onToggle={handleBatchToggle}
          onDelete={() => setBatchDeleteConfirm(true)}
          onClear={() => setSelectedNodes(new Set())}
        />
        <ManageNodeTable
          nodes={sortedNodes}
          selectedNodes={selectedNodes}
          sortKey={sortKey}
          sortDir={sortDir}
          hasFilters={Boolean(filter || statusFilter || regionFilter || sourceFilter)}
          probingTag={probingTag}
          toggling={toggling}
          onSort={handleSort}
          onToggleSelectAll={toggleSelectAll}
          onToggleSelectNode={toggleSelectNode}
          onProbe={handleProbe}
          onRelease={handleRelease}
          onToggle={handleToggle}
          onEdit={openEditModal}
          onDelete={node => {
            setDeleteTarget(node.uri)
            setDeleteTargetLabel(node.name)
          }}
        />
        {filteredNodes.length !== mergedNodes.length && (
          <div className="text-center text-xs text-base-content/30">
            筛选显示 {filteredNodes.length} / {mergedNodes.length} 个节点
          </div>
        )}
        <ManagePanelModals
          modalOpen={modalOpen}
          editingNode={editingNode}
          editingNodeLabel={editingNodeLabel}
          form={form}
          formError={formError}
          submitting={submitting}
          onFormChange={setForm}
          onSubmit={handleSubmit}
          onCloseEdit={() => setModalOpen(false)}
          importModalOpen={importModalOpen}
          importContent={importContent}
          importing={importing}
          importError={importError}
          importResult={importResult}
          onFileImport={handleFileImport}
          onImportContentChange={setImportContent}
          onImport={handleImport}
          onCloseImport={() => setImportModalOpen(false)}
          deleteTarget={deleteTarget}
          deleteTargetLabel={deleteTargetLabel}
          deleting={deleting}
          onDelete={handleDelete}
          onCloseDelete={() => {
            setDeleteTarget(null)
            setDeleteTargetLabel(null)
          }}
          batchDeleteConfirm={batchDeleteConfirm}
          selectedConfigCount={selectedConfigNodes.length}
          batchProcessing={batchProcessing}
          onBatchDelete={handleBatchDelete}
          onCloseBatchDelete={() => setBatchDeleteConfirm(false)}
        />
      </div>
    </div>
  )
}
