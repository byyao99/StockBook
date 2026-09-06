import { beforeEach, describe, expect, it } from 'vitest'
import { claimAutoRun, resetAutoRuns } from './autoRefresh'

beforeEach(resetAutoRuns)

describe('claimAutoRun', () => {
  // Nothing has run yet, so the first arrival always goes.
  it('lets the first run through', () => {
    expect(claimAutoRun('quotes', 60_000, 1_000)).toBe(true)
  })

  // Tabbing away and back is the case this exists for.
  it('refuses a second run inside the interval', () => {
    expect(claimAutoRun('quotes', 60_000, 1_000)).toBe(true)
    expect(claimAutoRun('quotes', 60_000, 30_000)).toBe(false)
    expect(claimAutoRun('quotes', 60_000, 60_999)).toBe(false)
  })

  it('lets a run through once the interval has passed', () => {
    expect(claimAutoRun('quotes', 60_000, 1_000)).toBe(true)
    expect(claimAutoRun('quotes', 60_000, 61_000)).toBe(true)
  })

  // The two pages call different endpoints with different rate limits, so one
  // arriving must not stop the other.
  it('tracks each key separately', () => {
    expect(claimAutoRun('quotes', 60_000, 1_000)).toBe(true)
    expect(claimAutoRun('research', 60_000, 1_000)).toBe(true)
    expect(claimAutoRun('quotes', 60_000, 2_000)).toBe(false)
  })

  // Claiming on read is what stops two mounts in one tick from both going.
  it('claims the slot as it reports, not afterwards', () => {
    expect(claimAutoRun('quotes', 60_000, 1_000)).toBe(true)
    expect(claimAutoRun('quotes', 60_000, 1_000)).toBe(false)
  })
})
