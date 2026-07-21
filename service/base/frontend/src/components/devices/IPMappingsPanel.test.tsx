import { render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'

import { mappingFixture } from '../../test/localServerFixtures'
import IPMappingsPanel from './IPMappingsPanel'

it('shows the source-IP reliability warning and mapping identifiers', () => {
  render(<IPMappingsPanel mappings={[mappingFixture()]} onCreate={() => undefined} onUpdate={vi.fn()} onDelete={vi.fn()} />)

  expect(screen.getByText(/IP 映射仅作为回退/)).toBeInTheDocument()
  expect(screen.getByText('map-1')).toBeInTheDocument()
  expect(screen.getByText('192.168.1.10/32')).toBeInTheDocument()
})
