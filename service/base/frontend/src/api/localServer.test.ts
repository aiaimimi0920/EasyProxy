import { expect, it } from 'vitest'

import {
  copySharedProfile,
  createIPMapping,
  deleteDeviceProfile,
  deleteIPMapping,
  fetchDevice,
  fetchDevices,
  fetchIPMappings,
  fetchLocalServerConfig,
  fetchLocalServerStatus,
  fetchSharedProfile,
  putDevice,
  putDeviceProfile,
  putSharedProfile,
  setDeviceProfileEnabled,
  updateIPMapping,
  updateLocalServerConfig,
} from './localServer'
import { jsonResponse, mockFetch } from '../test/http'
import {
  deviceFixtures,
  mappingFixture,
  mutationFixture,
  profileFixture,
  profileResourceFixture,
} from '../test/localServerFixtures'

it('uses the canonical Local Server resource URLs', async () => {
  const fetchSpy = mockFetch(
    jsonResponse({ enabled: true }),
    jsonResponse({ enabled: true }),
    jsonResponse(profileResourceFixture()),
    jsonResponse({ devices: deviceFixtures() }),
    jsonResponse({ ...deviceFixtures()[0], mappings: [] }),
    jsonResponse({ mappings: [mappingFixture()] }),
  )

  await fetchLocalServerStatus()
  await fetchLocalServerConfig()
  await fetchSharedProfile()
  await fetchDevices()
  await fetchDevice('Laptop.Work')
  await fetchIPMappings()

  expect(fetchSpy.mock.calls.map(([path]) => path)).toEqual([
    '/api/local-server/status',
    '/api/local-server/config',
    '/api/local-server/profiles/shared',
    '/api/local-server/devices',
    '/api/local-server/devices/laptop.work',
    '/api/local-server/ip-mappings',
  ])
})

it('creates a device profile with If-None-Match', async () => {
  const fetchSpy = mockFetch(jsonResponse(mutationFixture(profileResourceFixture())))

  await putDeviceProfile('Laptop', profileFixture(), 0)

  expect(fetchSpy).toHaveBeenCalledWith(
    '/api/local-server/devices/laptop/profile',
    expect.objectContaining({
      method: 'PUT',
      headers: expect.objectContaining({ 'If-None-Match': '*' }),
      body: JSON.stringify({ expected_revision: 0, profile: profileFixture() }),
    }),
  )
})

it('sends matching conditional revisions for profile and device mutations', async () => {
  const fetchSpy = mockFetch(
    jsonResponse(mutationFixture(profileResourceFixture())),
    jsonResponse(mutationFixture({ ...deviceFixtures()[0], mappings: [] })),
    jsonResponse(mutationFixture(profileResourceFixture())),
    jsonResponse(mutationFixture(profileResourceFixture())),
    jsonResponse(mutationFixture(profileResourceFixture())),
    jsonResponse(mutationFixture(profileResourceFixture())),
  )

  await putSharedProfile(profileFixture(), 3)
  await putDevice('Laptop', 'Laptop Work', 0)
  await setDeviceProfileEnabled('Laptop', false, 4)
  await copySharedProfile('Laptop')
  await deleteDeviceProfile('Laptop', 5)
  await updateLocalServerConfig({ auth_username: 'easyproxy' })

  expect(fetchSpy).toHaveBeenNthCalledWith(1, '/api/local-server/profiles/shared', expect.objectContaining({
    method: 'PUT',
    headers: expect.objectContaining({ 'If-Match': '"3"' }),
  }))
  expect(fetchSpy).toHaveBeenNthCalledWith(2, '/api/local-server/devices/laptop', expect.objectContaining({
    method: 'PUT',
    headers: expect.objectContaining({ 'If-None-Match': '*' }),
  }))
  expect(fetchSpy).toHaveBeenNthCalledWith(3, '/api/local-server/devices/laptop/profile/enabled', expect.objectContaining({
    method: 'PATCH',
    headers: expect.objectContaining({ 'If-Match': '"4"' }),
  }))
  expect(fetchSpy).toHaveBeenNthCalledWith(4, '/api/local-server/devices/laptop/profile/copy-shared', expect.objectContaining({
    method: 'POST',
  }))
  expect(fetchSpy).toHaveBeenNthCalledWith(5, '/api/local-server/devices/laptop/profile', expect.objectContaining({
    method: 'DELETE',
    headers: expect.objectContaining({ 'If-Match': '"5"' }),
  }))
  expect(fetchSpy).toHaveBeenNthCalledWith(6, '/api/local-server/config', expect.objectContaining({
    method: 'PUT',
    body: JSON.stringify({ auth_username: 'easyproxy' }),
  }))
})

it('uses mapping IDs and conditional headers for mapping mutations', async () => {
  const mapping = mappingFixture()
  const fetchSpy = mockFetch(
    jsonResponse(mutationFixture(mapping)),
    jsonResponse(mutationFixture(mapping)),
    jsonResponse(mutationFixture(mapping)),
  )

  const input = { cidr: mapping.cidr, device_id: mapping.device_id, priority: mapping.priority, enabled: mapping.enabled }
  await createIPMapping(input)
  await updateIPMapping('Map / 1', input, 2)
  await deleteIPMapping('Map / 1', 3)

  expect(fetchSpy).toHaveBeenNthCalledWith(1, '/api/local-server/ip-mappings', expect.objectContaining({
    method: 'POST',
    headers: expect.objectContaining({ 'If-None-Match': '*' }),
  }))
  expect(fetchSpy).toHaveBeenNthCalledWith(2, '/api/local-server/ip-mappings/Map%20%2F%201', expect.objectContaining({
    method: 'PUT',
    headers: expect.objectContaining({ 'If-Match': '"2"' }),
  }))
  expect(fetchSpy).toHaveBeenNthCalledWith(3, '/api/local-server/ip-mappings/Map%20%2F%201', expect.objectContaining({
    method: 'DELETE',
    headers: expect.objectContaining({ 'If-Match': '"3"' }),
  }))
})
