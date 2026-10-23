import { describe, expect, it } from 'vitest'
import { STALE_AFTER_DAYS, historyLag, isStale } from './historyLag'

describe('historyLag', () => {
  // The case that prompted it: closes stopped on the 16th, read on the 23rd.
  it('counts the days from the last session to today for an open period', () => {
    expect(historyLag('2026-10-16', '', '2026-10-23')).toBe(7)
    expect(historyLag('2026-10-16', '2026-12-31', '2026-10-23')).toBe(7)
  })

  // A period that ended in the past is complete when it reaches its own end.
  it('measures a closed period against its end, not against today', () => {
    expect(historyLag('2024-12-31', '2024-12-31', '2026-10-23')).toBe(0)
    expect(historyLag('2024-12-27', '2024-12-31', '2026-10-23')).toBe(4)
  })

  it('has nothing to measure without a session', () => {
    expect(historyLag(undefined, '', '2026-10-23')).toBeNull()
  })

  it('never reports a negative lag', () => {
    expect(historyLag('2026-10-23', '', '2026-10-22')).toBe(0)
  })
})

describe('isStale', () => {
  // Friday's close read on Monday is three days old and perfectly current.
  it('tolerates a weekend', () => {
    expect(isStale(STALE_AFTER_DAYS)).toBe(false)
    expect(isStale(STALE_AFTER_DAYS + 1)).toBe(true)
  })

  it('is not stale when there is nothing to measure', () => {
    expect(isStale(null)).toBe(false)
  })
})
