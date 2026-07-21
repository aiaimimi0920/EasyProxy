import { vi, type Mock } from 'vitest'

export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

export function mockFetch(...responses: Response[]): Mock<typeof fetch> {
  const spy = vi.fn<typeof fetch>()
  for (const response of responses) spy.mockResolvedValueOnce(response)
  if (responses.length > 0) spy.mockResolvedValue(responses[responses.length - 1])
  vi.stubGlobal('fetch', spy)
  return spy
}
