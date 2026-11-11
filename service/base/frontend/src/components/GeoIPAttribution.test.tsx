import { render, screen } from '@testing-library/react'
import { expect, test } from 'vitest'
import GeoIPAttribution from './GeoIPAttribution'

test('shows DB-IP source, license and geography limitation', () => {
  render(<GeoIPAttribution />)
  expect(screen.getByRole('link', { name: 'IP Geolocation by DB-IP' }).getAttribute('href')).toBe('https://db-ip.com')
  expect(screen.getByRole('link', { name: 'CC BY 4.0' }).getAttribute('href')).toBe('https://creativecommons.org/licenses/by/4.0/')
  expect(screen.getByRole('contentinfo').textContent).toContain('不代表实际出口位置')
})
