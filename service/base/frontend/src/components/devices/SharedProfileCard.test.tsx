import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'

import { profileFixture, profileResourceFixture } from '../../test/localServerFixtures'
import SharedProfileCard from './SharedProfileCard'

it('shows shared Profile state and revision', () => {
  render(<SharedProfileCard resource={profileResourceFixture({
    profile_scope: 'shared',
    device_id: undefined,
    revision: 4,
    profile: profileFixture({ enabled: false, final_policy: 'DIRECT', rules: ['GEOIP,CN,DIRECT'] }),
  })} onEdit={() => undefined} />)

  expect(screen.getByText('共享 Profile')).toBeInTheDocument()
  expect(screen.getByRole('group', { name: '状态 DIRECT' })).toBeInTheDocument()
  expect(screen.getByRole('group', { name: '最终策略 DIRECT' })).toBeInTheDocument()
  expect(screen.getByText(/修订 4/)).toBeInTheDocument()
  expect(screen.getByText(/1 条规则/)).toBeInTheDocument()
})
