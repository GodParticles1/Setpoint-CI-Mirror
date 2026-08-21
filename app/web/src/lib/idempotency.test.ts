import { describe, expect, it } from 'vitest'
import { IdempotentOperation } from './idempotency'

describe('IdempotentOperation', () => {
  it('reuses a key for retries and changes it only for a new operation', () => {
    const operation = new IdempotentOperation()
    const first = operation.keyFor('same-input')
    expect(operation.keyFor('same-input')).toBe(first)
    expect(operation.keyFor('changed-input')).not.toBe(first)

    const changed = operation.keyFor('changed-input')
    operation.complete()
    expect(operation.keyFor('changed-input')).not.toBe(changed)
  })
})
