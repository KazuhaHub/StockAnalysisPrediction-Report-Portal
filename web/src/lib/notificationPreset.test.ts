/// <reference types="node" />
// @vitest-environment node
import { describe, expect, it } from 'vitest'
import * as crypto from 'node:crypto'
import { Buffer } from 'node:buffer'
const { createHmac } = crypto
import workflow from '../../public/notification-presets/n8n-signed-receiver.json'
const code = workflow.nodes.find(node => node.type === 'n8n-nodes-base.code')!.parameters.jsCode!
const run = async (raw: Buffer, signature: string, secret = 'example-signing-secret') => {
  const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor
  const evaluate = new AsyncFunction('require', '$input', '$env', code)
  return evaluate.call({ helpers: { getBinaryDataBuffer: async (index: number, field: string) => { expect(index).toBe(0); expect(field).toBe('data'); return raw } } }, (name: string) => { expect(name).toBe('crypto'); return crypto }, { first: () => ({ json: { headers: { 'x-report-portal-signature': signature } } }) }, { REPORT_PORTAL_WEBHOOK_SECRET: secret })
}
describe('signed receiver preset', () => {
  it('verifies exact bytes and preserves large integers without parsing', async () => {
    const raw = Buffer.from('{"id":9007199254740993,"title":"A < B & C"}')
    const signature = 'sha256=' + createHmac('sha256', 'example-signing-secret').update(raw).digest('hex')
    const result = await run(raw, signature)
    expect(result[0].json.valid).toBe(true)
    expect(result[0].json.body_utf8).toBe(raw.toString())
    expect(result[0].json.body_sha256).toMatch(/^[a-f0-9]{64}$/)
    expect((await run(Buffer.from(raw.toString() + ' '), signature))[0].json.valid).toBe(false)
    expect((await run(raw, 'sha256=invalid'))[0].json.valid).toBe(false)
  })
  it('fails closed before use and ships inactive without secrets or pinned data', async () => {
    await expect(run(Buffer.from('{}'), '', '')).rejects.toThrow('Configure')
    expect(workflow.active).toBe(false)
    expect(workflow.pinData).toEqual({})
    expect(workflow.nodes.some(node => node.parameters.options && 'rawBody' in node.parameters.options && node.parameters.options.rawBody)).toBe(true)
  })
})
