import { describe, expect, it } from 'vitest'
import { formatSharesAfterSplit, formatSplitRatio, sharesAfterSplit } from './splits'
import { fromShares } from './shares'

describe('formatSplitRatio', () => {
  it('prints an ordinary split the way it is announced', () => {
    expect(formatSplitRatio(2_000_000)).toBe('2:1')
    expect(formatSplitRatio(4_000_000)).toBe('4:1')
  })

  // The Taiwanese case, and the reason the ratio is stored in millionths at
  // all: an 8% stock dividend turns 25 shares into 27.
  it('prints a stock dividend as the fraction it is', () => {
    expect(formatSplitRatio(1_080_000)).toBe('1.08:1')
  })

  // A 1-for-5 consolidation printed as "0.2:1" is a true statement nobody says
  // out loud, and reads as a rounding error rather than as the fivefold change
  // it is.
  it('turns a reverse split around rather than printing a fraction', () => {
    expect(formatSplitRatio(200_000)).toBe('1:5')
  })

  it('does not pad a whole ratio with decimals', () => {
    expect(formatSplitRatio(3_000_000)).toBe('3:1')
  })

  it('has nothing to say about a ratio that is not one', () => {
    expect(formatSplitRatio(0)).toBe('—')
  })
})

describe('sharesAfterSplit', () => {
  // The number the warning exists to give: what the recorded entries actually
  // describe in today's shares, which is the correction a reader must make.
  it('states what a recorded count became', () => {
    expect(sharesAfterSplit(fromShares(1000), 2_000_000)).toBe(fromShares(2000))
    expect(formatSharesAfterSplit(fromShares(1000), 2_000_000)).toBe('2,000')
  })

  it('handles a fractional ratio without drifting', () => {
    expect(sharesAfterSplit(fromShares(25), 1_080_000)).toBe(fromShares(27))
  })

  it('shrinks a holding on a reverse split', () => {
    expect(sharesAfterSplit(fromShares(500), 200_000)).toBe(fromShares(100))
  })
})
