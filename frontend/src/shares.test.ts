import { describe, expect, it } from 'vitest'
import { SHARES_SCALE, formatShares, fromShares, toShares } from './shares'

describe('toShares / fromShares', () => {
  it('round-trips a whole holding', () => {
    expect(toShares(100 * SHARES_SCALE)).toBe(100)
    expect(fromShares(100)).toBe(100 * SHARES_SCALE)
  })

  // The case the scale exists for: a broker filling a fixed-amount purchase.
  it('round-trips a fractional holding', () => {
    expect(fromShares(2.79)).toBe(2_790_000)
    expect(toShares(2_790_000)).toBe(2.79)
  })

  // 2.79 is not exactly representable, so the product lands a hair under the
  // integer. Truncating would lose the last unit — silently, and differently
  // for different inputs.
  it('rounds rather than truncates on the way in', () => {
    expect(fromShares(0.1)).toBe(100_000)
    expect(fromShares(0.3)).toBe(300_000)
    expect(fromShares(1.0000005)).toBe(1_000_001)
  })
})

describe('formatShares', () => {
  // The common case must not be made noisy by the uncommon one.
  it('writes a whole holding without a fraction', () => {
    expect(formatShares(100 * SHARES_SCALE)).toBe('100')
    expect(formatShares(1_000 * SHARES_SCALE)).toBe('1,000')
  })

  it('writes only the decimals that are there', () => {
    expect(formatShares(2_790_000)).toBe('2.79')
    expect(formatShares(1_500_000)).toBe('1.5')
  })

  it('groups thousands', () => {
    expect(formatShares(12_345 * SHARES_SCALE)).toBe('12,345')
  })

  it('renders zero as zero', () => {
    expect(formatShares(0)).toBe('0')
  })
})
