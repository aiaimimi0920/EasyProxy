import { useState } from 'react'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import type { SelectionTier } from '../../types'
import SelectionTiersForm from './SelectionTiersForm'

function Harness({ initial }: { initial?: SelectionTier[] }) {
  const [value, setValue] = useState(initial)
  return <><SelectionTiersForm value={value} onChange={setValue} /><output data-testid="tiers">{JSON.stringify(value)}</output></>
}

it('shows legacy profiles without silently creating tiers', () => {
  const onChange = vi.fn()
  render(<SelectionTiersForm onChange={onChange} />)
  expect(screen.getByLabelText('第一级策略')).toHaveValue('disabled')
  expect(screen.getByLabelText('第二级策略')).toBeDisabled()
  expect(screen.getByText('第三级（固定）：回退到通用池')).toBeVisible()
  expect(onChange).not.toHaveBeenCalled()
})

it('configures, reorders, switches and disables two tiers without stale country fields', async () => {
  render(<Harness />)
  await userEvent.selectOptions(screen.getByLabelText('第一级策略'), 'country')
  await userEvent.type(screen.getByLabelText('第一级策略优先国家（两位代码）'), 'jp')
  await userEvent.selectOptions(screen.getByLabelText('第二级策略'), 'latency')
  expect(screen.getByTestId('tiers')).toHaveTextContent('[{"kind":"country","country":"JP"},{"kind":"latency"}]')
  await userEvent.click(screen.getByRole('button', { name: '交换一二级顺序' }))
  expect(screen.getByLabelText('第一级策略')).toHaveValue('latency')
  expect(screen.getByLabelText('第二级策略优先国家（两位代码）')).toHaveValue('JP')
  await userEvent.selectOptions(screen.getByLabelText('第二级策略'), 'stability')
  expect(screen.getByTestId('tiers')).toHaveTextContent('[{"kind":"latency"},{"kind":"stability"}]')
  await userEvent.selectOptions(screen.getByLabelText('第一级策略'), 'disabled')
  expect(screen.getByLabelText('第一级策略')).toHaveValue('stability')
  expect(screen.getByLabelText('第二级策略')).toHaveValue('disabled')
  await userEvent.selectOptions(screen.getByLabelText('第一级策略'), 'disabled')
  expect(screen.getByTestId('tiers')).toHaveTextContent('[]')
})

it('locks every editing control while saving', () => {
  render(<SelectionTiersForm value={[{ kind: 'country', country: 'US' }, { kind: 'stability' }]} onChange={vi.fn()} disabled />)
  for (const control of [...screen.getAllByRole('combobox'), ...screen.getAllByRole('textbox'), ...screen.getAllByRole('button')]) {
    expect(control).toBeDisabled()
  }
})


it('explains metric ranking, warmup and fallback without implying sticky strategy overrides metrics', () => {
  render(<SelectionTiersForm onChange={vi.fn()} />)
  expect(screen.getByText(/国家级和通用池使用 stable/)).toHaveTextContent('延时级、稳定级严格按测量指标排序，不使用会话绑定或手动置顶')
  expect(screen.getByText(/稳定性采用最近最多 8 次/)).toHaveTextContent('至少需要 3 次')
  expect(screen.getByText(/稳定性采用最近最多 8 次/)).toHaveTextContent('默认每 2 小时探测时需要等待积累')
  expect(screen.getByText(/停用某级后/)).toHaveTextContent('测量不足、过期时跳过该级')
})

it('gives each control a distinct accessible label even with two country tiers', () => {
  render(<SelectionTiersForm value={[{ kind: 'country', country: 'JP' }, { kind: 'country', country: 'US' }]} onChange={vi.fn()} />)
  expect(screen.getByRole('combobox', { name: '第一级策略' })).toBeEnabled()
  expect(screen.getByRole('combobox', { name: '第二级策略' })).toBeEnabled()
  expect(screen.getByRole('textbox', { name: '第一级策略优先国家（两位代码）' })).toHaveValue('JP')
  expect(screen.getByRole('textbox', { name: '第二级策略优先国家（两位代码）' })).toHaveValue('US')
  expect(screen.getByRole('region', { name: '三级节点选择' })).toBeVisible()
})
