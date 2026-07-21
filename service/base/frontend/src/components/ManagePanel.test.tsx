import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'

import ManagePanel from './ManagePanel'

const apiMocks = vi.hoisted(() => ({
  fetchConfigNodes: vi.fn(),
  fetchNodes: vi.fn(),
  createConfigNode: vi.fn(),
  updateConfigNode: vi.fn(),
  deleteConfigNode: vi.fn(),
  toggleConfigNode: vi.fn(),
  batchToggleConfigNodes: vi.fn(),
  batchDeleteConfigNodes: vi.fn(),
  triggerReload: vi.fn(),
  importNodes: vi.fn(),
  exportProxies: vi.fn(),
  probeNode: vi.fn(),
  releaseNode: vi.fn(),
}))

vi.mock('../api/client', () => apiMocks)

const runtimeNode = {
  tag: 'subscribed-us-1',
  name: '订阅运行节点',
  uri: 'vless://runtime-node@example.com:443?security=tls#订阅运行节点',
  mode: 'hybrid',
  listen_address: '0.0.0.0',
  port: 25001,
  region: 'us',
  country: 'United States',
  source_kind: 'subscription',
  source_name: 'subscription-1',
  source_ref: 'local:subscription-1',
  failure_count: 0,
  success_count: 3,
  blacklisted: false,
  blacklisted_until: '',
  active_connections: 0,
  last_latency_ms: 120,
  available: true,
  initial_check_done: true,
  total_upload: 0,
  total_download: 0,
}

const configuredNode = {
  name: '静态配置节点',
  uri: 'trojan://configured-node@example.com:443#静态配置节点',
  port: 25002,
  username: '',
  password: '',
  source: 'manual',
  disabled: false,
}

const configuredSnapshot = {
  ...runtimeNode,
  tag: 'configured-node',
  name: configuredNode.name,
  uri: configuredNode.uri,
  port: configuredNode.port,
  source_kind: 'manual',
}

beforeEach(() => {
  for (const mock of Object.values(apiMocks)) mock.mockReset()
  apiMocks.fetchConfigNodes.mockResolvedValue({ nodes: [] })
  apiMocks.fetchNodes.mockResolvedValue({
    nodes: [runtimeNode],
    total_nodes: 1,
    total_upload: 0,
    total_download: 0,
    region_stats: { us: 1 },
    region_healthy: { us: 1 },
  })
})

it('shows runtime subscription nodes when no static config nodes exist', async () => {
  render(<ManagePanel />)

  expect(await screen.findByText('订阅运行节点')).toBeInTheDocument()
  expect(screen.getByText((_content, element) => element?.tagName === 'SPAN' && element.textContent === '共 1 个节点')).toBeInTheDocument()
  expect(screen.getByText('订阅')).toBeInTheDocument()
  expect(screen.getByText('25001')).toBeInTheDocument()
  expect(screen.getByTitle('探测延迟')).toBeInTheDocument()
  expect(screen.queryByTitle('编辑节点配置')).not.toBeInTheDocument()
  expect(screen.queryByTitle('删除节点')).not.toBeInTheDocument()
  expect(screen.queryByTitle('启用该节点')).not.toBeInTheDocument()
  expect(screen.queryByTitle('禁用该节点')).not.toBeInTheDocument()
})

it('does not send static batch mutations for a runtime-only selection', async () => {
  const user = userEvent.setup()
  render(<ManagePanel />)

  await screen.findByText('订阅运行节点')
  await user.click(screen.getAllByRole('checkbox')[1])

  expect(screen.getByRole('button', { name: '启用' })).toBeDisabled()
  expect(screen.getByRole('button', { name: '禁用' })).toBeDisabled()
  expect(screen.getByRole('button', { name: '删除' })).toBeDisabled()
  expect(screen.getByRole('button', { name: /批量探测/ })).toBeEnabled()
  expect(apiMocks.batchToggleConfigNodes).not.toHaveBeenCalled()
  expect(apiMocks.batchDeleteConfigNodes).not.toHaveBeenCalled()
})

it('keeps configured actions and filters runtime nodes from static batch mutations', async () => {
  const user = userEvent.setup()
  apiMocks.fetchConfigNodes.mockResolvedValue({ nodes: [configuredNode] })
  apiMocks.fetchNodes.mockResolvedValue({
    nodes: [configuredSnapshot, runtimeNode],
    total_nodes: 2,
    total_upload: 0,
    total_download: 0,
    region_stats: { us: 2 },
    region_healthy: { us: 2 },
  })
  apiMocks.probeNode.mockResolvedValue({ message: 'ok', latency_ms: 120 })
  apiMocks.batchToggleConfigNodes.mockResolvedValue({ message: 'done', success: 1, total: 1 })

  render(<ManagePanel />)

  const configuredRow = (await screen.findByText(configuredNode.name)).closest('tr')
  const runtimeRow = screen.getByText(runtimeNode.name).closest('tr')
  if (!configuredRow || !runtimeRow) throw new Error('expected node rows')

  expect(within(configuredRow).getByTitle('编辑节点配置')).toBeInTheDocument()
  expect(within(configuredRow).getByTitle('删除节点')).toBeInTheDocument()
  expect(within(configuredRow).getByTitle('禁用该节点')).toBeInTheDocument()
  expect(within(runtimeRow).queryByTitle('编辑节点配置')).not.toBeInTheDocument()

  await user.click(within(runtimeRow).getByTitle('探测延迟'))
  await waitFor(() => expect(apiMocks.probeNode).toHaveBeenCalledWith(runtimeNode.tag))

  await user.click(within(configuredRow).getByRole('checkbox'))
  await user.click(within(runtimeRow).getByRole('checkbox'))
  await user.click(screen.getByRole('button', { name: '禁用' }))

  await waitFor(() => {
    expect(apiMocks.batchToggleConfigNodes).toHaveBeenCalledWith([configuredNode.uri], false)
  })
})

it('allows a runtime-only blacklisted node to be released by tag', async () => {
  const user = userEvent.setup()
  apiMocks.fetchNodes.mockResolvedValue({
    nodes: [{ ...runtimeNode, available: false, blacklisted: true }],
    total_nodes: 1,
    total_upload: 0,
    total_download: 0,
    region_stats: { us: 1 },
    region_healthy: { us: 0 },
  })
  apiMocks.releaseNode.mockResolvedValue({ message: 'released' })

  render(<ManagePanel />)

  await user.click(await screen.findByTitle('解除黑名单'))

  await waitFor(() => expect(apiMocks.releaseNode).toHaveBeenCalledWith(runtimeNode.tag))
})
