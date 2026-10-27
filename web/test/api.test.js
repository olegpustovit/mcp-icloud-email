import assert from 'node:assert/strict'
import test from 'node:test'
import { request } from '../src/api.js'

test('request retains HTTP status when the API returns an error string', async () => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = async () => ({ ok: false, status: 401, json: async () => ({ error: 'login_required' }) })
  try {
    await assert.rejects(request('/api/me'), error => error.message === 'login_required' && error.status === 401)
  } finally { globalThis.fetch = originalFetch }
})

test('request treats empty successful responses as null', async () => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = async () => ({ ok: true, status: 204 })
  try { assert.equal(await request('/api/account', { method: 'DELETE' }), null) }
  finally { globalThis.fetch = originalFetch }
})
