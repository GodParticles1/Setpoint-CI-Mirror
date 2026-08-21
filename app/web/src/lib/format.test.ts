import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanTags, dateTime, isTerminal, itemLabels, newIdempotencyKey, shortID } from './format'

afterEach(() => vi.unstubAllGlobals())

describe('format helpers', () => {
  it('keeps the five check conclusions distinct', () => {
    expect(Object.keys(itemLabels).sort()).toEqual(['error', 'manual_review', 'not_applicable', 'safe', 'unsafe'])
  })

  it('recognizes only terminal run phases', () => {
    expect(isTerminal('completed')).toBe(true)
    expect(isTerminal('partial_failed')).toBe(true)
    expect(isTerminal('canceled')).toBe(true)
    expect(isTerminal('running')).toBe(false)
    expect(isTerminal('pending')).toBe(false)
  })

  it('normalizes tags and short identifiers without changing facts', () => {
    expect(cleanTags('prod, ssh, prod,  ')).toEqual(['prod', 'ssh'])
    expect(shortID('12345678-1234-1234-1234')).toBe('12345678…1234')
  })

  it('hides Go zero time and creates valid idempotency keys', () => {
    expect(dateTime('0001-01-01T00:00:00Z')).toBe('暂无')
    expect(newIdempotencyKey()).toMatch(/^[A-Za-z0-9._:-]+$/)
  })

  it('uses Web Crypto random bytes when randomUUID is unavailable', () => {
    const getRandomValues = vi.fn((bytes: Uint8Array) => {
      bytes.forEach((_value, index) => { bytes[index] = index })
      return bytes
    })
    vi.stubGlobal('crypto', { getRandomValues })

    expect(newIdempotencyKey()).toBe('ui-000102030405060708090a0b0c0d0e0f')
    expect(getRandomValues).toHaveBeenCalledOnce()
  })
})
