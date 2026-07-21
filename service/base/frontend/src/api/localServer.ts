import { apiRequest } from './client'
import type {
  DeviceResource,
  DeviceSummary,
  ForwardingProfile,
  IPMapping,
  LocalServerConfig,
  LocalServerStatus,
  MutationResponse,
  ProfileResource,
} from '../types/localServer'

type LocalServerConfigUpdate = {
  enabled?: boolean
  listen?: string
  auth_username?: string
  auth_password?: string
}

type IPMappingInput = Omit<IPMapping, 'mapping_id' | 'revision'>

function devicePath(deviceId: string): string {
  return encodeURIComponent(deviceId.trim().toLowerCase())
}

function mappingPath(mappingId: string): string {
  return encodeURIComponent(mappingId.trim())
}

function createHeaders(): HeadersInit {
  return { 'If-None-Match': '*' }
}

function updateHeaders(revision: number): HeadersInit {
  return { 'If-Match': `"${revision}"` }
}

function createOrUpdateHeaders(revision: number): HeadersInit {
  return revision === 0 ? createHeaders() : updateHeaders(revision)
}

export function fetchLocalServerStatus(): Promise<LocalServerStatus> {
  return apiRequest('/api/local-server/status')
}

export function fetchLocalServerConfig(): Promise<LocalServerConfig> {
  return apiRequest('/api/local-server/config')
}

export function updateLocalServerConfig(
  update: LocalServerConfigUpdate,
): Promise<MutationResponse<LocalServerConfig>> {
  return apiRequest('/api/local-server/config', {
    method: 'PUT',
    body: JSON.stringify(update),
  })
}

export function fetchSharedProfile(): Promise<ProfileResource> {
  return apiRequest('/api/local-server/profiles/shared')
}

export function putSharedProfile(
  profile: ForwardingProfile,
  expectedRevision: number,
): Promise<MutationResponse<ProfileResource>> {
  return apiRequest('/api/local-server/profiles/shared', {
    method: 'PUT',
    headers: updateHeaders(expectedRevision),
    body: JSON.stringify({ expected_revision: expectedRevision, profile }),
  })
}

export function fetchDevices(): Promise<{ devices: DeviceSummary[] }> {
  return apiRequest('/api/local-server/devices')
}

export function fetchDevice(deviceId: string): Promise<DeviceResource> {
  return apiRequest(`/api/local-server/devices/${devicePath(deviceId)}`)
}

export function putDevice(
  deviceId: string,
  displayName: string,
  expectedRevision: number,
): Promise<MutationResponse<DeviceResource>> {
  return apiRequest(`/api/local-server/devices/${devicePath(deviceId)}`, {
    method: 'PUT',
    headers: createOrUpdateHeaders(expectedRevision),
    body: JSON.stringify({ expected_revision: expectedRevision, display_name: displayName }),
  })
}

export function putDeviceProfile(
  deviceId: string,
  profile: ForwardingProfile,
  expectedRevision: number,
): Promise<MutationResponse<ProfileResource>> {
  return apiRequest(`/api/local-server/devices/${devicePath(deviceId)}/profile`, {
    method: 'PUT',
    headers: createOrUpdateHeaders(expectedRevision),
    body: JSON.stringify({ expected_revision: expectedRevision, profile }),
  })
}

export function setDeviceProfileEnabled(
  deviceId: string,
  enabled: boolean,
  expectedRevision: number,
): Promise<MutationResponse<ProfileResource>> {
  return apiRequest(`/api/local-server/devices/${devicePath(deviceId)}/profile/enabled`, {
    method: 'PATCH',
    headers: updateHeaders(expectedRevision),
    body: JSON.stringify({ expected_revision: expectedRevision, enabled }),
  })
}

export function copySharedProfile(deviceId: string): Promise<MutationResponse<ProfileResource>> {
  return apiRequest(`/api/local-server/devices/${devicePath(deviceId)}/profile/copy-shared`, {
    method: 'POST',
  })
}

export function deleteDeviceProfile(
  deviceId: string,
  expectedRevision: number,
): Promise<MutationResponse<ProfileResource>> {
  return apiRequest(`/api/local-server/devices/${devicePath(deviceId)}/profile`, {
    method: 'DELETE',
    headers: updateHeaders(expectedRevision),
    body: JSON.stringify({ expected_revision: expectedRevision }),
  })
}

export function fetchIPMappings(): Promise<{ mappings: IPMapping[] }> {
  return apiRequest('/api/local-server/ip-mappings')
}

export function createIPMapping(input: IPMappingInput): Promise<MutationResponse<IPMapping>> {
  return apiRequest('/api/local-server/ip-mappings', {
    method: 'POST',
    headers: createHeaders(),
    body: JSON.stringify({ expected_revision: 0, ...input }),
  })
}

export function updateIPMapping(
  mappingId: string,
  input: IPMappingInput,
  expectedRevision: number,
): Promise<MutationResponse<IPMapping>> {
  return apiRequest(`/api/local-server/ip-mappings/${mappingPath(mappingId)}`, {
    method: 'PUT',
    headers: updateHeaders(expectedRevision),
    body: JSON.stringify({ expected_revision: expectedRevision, ...input }),
  })
}

export function deleteIPMapping(
  mappingId: string,
  expectedRevision: number,
): Promise<MutationResponse<IPMapping>> {
  return apiRequest(`/api/local-server/ip-mappings/${mappingPath(mappingId)}`, {
    method: 'DELETE',
    headers: updateHeaders(expectedRevision),
    body: JSON.stringify({ expected_revision: expectedRevision }),
  })
}
