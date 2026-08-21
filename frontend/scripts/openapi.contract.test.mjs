import test from 'node:test'
import { readFile } from 'node:fs/promises'
import assert from 'node:assert/strict'

const openapi = await readFile(new URL('../../backend/openapi.yaml', import.meta.url), 'utf8')
const schema = await readFile(new URL('../src/api/schema.d.ts', import.meta.url), 'utf8')

test('generated API schema tracks the OpenAPI contract', () => {
  for (const path of [
    '/api/v1/ranks/latest',
    '/api/v1/boards/{type}/{code}/quotes',
    '/api/v1/research/export',
    '/api/v1/focus/scan',
  ]) {
    assert.ok(openapi.includes(`  ${path}:`), `OpenAPI is missing ${path}`)
    assert.ok(schema.includes(`"${path}"`), `generated schema is missing ${path}`)
  }
  assert.match(openapi, /BearerAuth:/)
  assert.match(schema, /quote_status\?: "ready" \| "warming" \| "stale" \| "unavailable"/)
})
