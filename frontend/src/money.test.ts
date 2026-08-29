import { describe, expect, it } from 'vitest'
import {
  UNKNOWN,
  formatBpsMagnitudeOrUnknown,
  formatBpsOrUnknown,
  formatCents,
  formatCentsOrUnknown,
  formatCompactCents,
  formatPercent,
  formatPercentOrUnknown,
  bpsToListPercent,
  formatPpmPercent,
  formatQty,
  formatSignedCents,
  formatSignedOrUnknown,
  fromCents,
  percentToPpm,
  ppmToPercent,
  listPercentToBps,
  toCents,
} from './money'

describe('formatCents', () => {
  it('formats whole dollars with two decimals', () => {
    expect(formatCents(18000)).toBe('$180.00')
  })

  it('formats sub-dollar amounts', () => {
    expect(formatCents(5)).toBe('$0.05')
    expect(formatCents(50)).toBe('$0.50')
  })

  it('formats zero', () => {
    expect(formatCents(0)).toBe('$0.00')
  })
})

describe('toCents', () => {
  it('converts dollars to integer cents', () => {
    expect(toCents(180)).toBe(18000)
    expect(toCents(9.99)).toBe(999)
  })

  it('rounds instead of truncating float artifacts', () => {
    // 19.99 * 100 is 1998.9999999999998 in IEEE-754; Math.round must fix it.
    expect(toCents(19.99)).toBe(1999)
    expect(toCents(0.1 + 0.2)).toBe(30)
  })
})

describe('fromCents', () => {
  it('converts cents back to a dollar number', () => {
    expect(fromCents(18000)).toBe(180)
    expect(fromCents(999)).toBe(9.99)
  })

  it('round-trips with toCents', () => {
    for (const cents of [0, 1, 99, 100, 1999, 123456]) {
      expect(toCents(fromCents(cents))).toBe(cents)
    }
  })
})

describe('formatSignedCents', () => {
  // A gain and a loss must never look alike at a glance, and the sign carries
  // that even where color is unavailable.
  it('always carries an explicit sign', () => {
    expect(formatSignedCents(2500)).toBe('+$25.00')
    expect(formatSignedCents(-2500)).toBe('-$25.00')
  })

  it('treats break-even as a non-loss', () => {
    expect(formatSignedCents(0)).toBe('+$0.00')
  })
})

describe('formatPercent', () => {
  it('formats a fraction as a signed percentage', () => {
    expect(formatPercent(0.1234)).toBe('+12.34%')
    expect(formatPercent(-0.5)).toBe('-50.00%')
    expect(formatPercent(0)).toBe('+0.00%')
  })
})

describe('formatQty', () => {
  it('groups thousands so large share counts stay readable', () => {
    expect(formatQty(1000)).toBe('1,000')
    expect(formatQty(12345678)).toBe('12,345,678')
    expect(formatQty(7)).toBe('7')
  })
})

describe('the unknown-value formatters', () => {
  // Showing an unvalued holding as $0.00 would read as a total loss, so every
  // nullable number gets an explicit "not known" rendering instead.
  it('render null as the unknown marker rather than zero', () => {
    expect(formatCentsOrUnknown(null)).toBe(UNKNOWN)
    expect(formatSignedOrUnknown(null)).toBe(UNKNOWN)
    expect(formatPercentOrUnknown(null)).toBe(UNKNOWN)
  })

  it('pass real values through, including zero', () => {
    expect(formatCentsOrUnknown(0)).toBe('$0.00')
    expect(formatCentsOrUnknown(18000)).toBe('$180.00')
    expect(formatSignedOrUnknown(-1)).toBe('-$0.01')
    expect(formatPercentOrUnknown(0.05)).toBe('+5.00%')
  })
})

describe('formatBpsOrUnknown', () => {
  it('reads basis points as a signed percentage', () => {
    expect(formatBpsOrUnknown(1234)).toBe('+12.34%')
    expect(formatBpsOrUnknown(-5000)).toBe('-50.00%')
    expect(formatBpsOrUnknown(10000)).toBe('+100.00%')
  })

  it('shows breaking even as zero, not as unknown', () => {
    expect(formatBpsOrUnknown(0)).toBe('+0.00%')
  })

  it('marks a rate that could not be computed as unknown', () => {
    expect(formatBpsOrUnknown(null)).toBe(UNKNOWN)
  })
})

describe('formatBpsMagnitudeOrUnknown', () => {
  it('reports a depth without a sign', () => {
    // The signed formatter would print the worst fall a book ever took as
    // "+15.99%", which reads as a gain — this is the whole reason it exists.
    expect(formatBpsMagnitudeOrUnknown(1599)).toBe('15.99%')
  })

  it('reads a drawdown that arrived negative as the same depth', () => {
    expect(formatBpsMagnitudeOrUnknown(-1599)).toBe('15.99%')
  })

  it('shows a book that never fell as zero, not as unknown', () => {
    expect(formatBpsMagnitudeOrUnknown(0)).toBe('0.00%')
  })

  it('marks an unmeasurable drawdown as unknown', () => {
    expect(formatBpsMagnitudeOrUnknown(null)).toBe(UNKNOWN)
  })
})

describe('formatCompactCents', () => {
  it('shortens amounts to fit an axis margin', () => {
    expect(formatCompactCents(123_456_789, 'TWD')).toBe('NT$1.23M')
    expect(formatCompactCents(500_000, 'USD')).toBe('$5.0K')
    expect(formatCompactCents(1_234_567_890_00)).toBe('$1.23B')
  })

  it('leaves small amounts unscaled', () => {
    expect(formatCompactCents(45_600)).toBe('$456')
  })

  it('keeps the sign on a negative amount', () => {
    // Money in can go negative once more has been taken out than put in.
    expect(formatCompactCents(-500_000)).toBe('-$5.0K')
  })
})

describe('parts per million', () => {
  // 0.1425% is 14.25 basis points, which is why fee rates are stored in ppm at
  // all — a basis point cannot hold the most common rate in this book.
  it('formats a rate without a sign, trimming trailing zeros', () => {
    expect(formatPpmPercent(1425)).toBe('0.1425%')
    expect(formatPpmPercent(3000)).toBe('0.3%')
    expect(formatPpmPercent(0)).toBe('0%')
    expect(formatPpmPercent(10000)).toBe('1%')
  })

  it('round-trips through the editing scale', () => {
    for (const ppm of [0, 1, 399, 1000, 1425, 1500, 3000, 100000]) {
      expect(percentToPpm(ppmToPercent(ppm))).toBe(ppm)
    }
  })
})

describe('broker discount as a percentage of list', () => {
  it('converts between the stored basis points and the percentage shown', () => {
    expect(bpsToListPercent(2800)).toBe(28)
    expect(bpsToListPercent(10000)).toBe(100)
    expect(listPercentToBps(28)).toBe(2800)
    expect(listPercentToBps(60)).toBe(6000)
  })

  it('round-trips every discount a broker is likely to quote', () => {
    for (let bps = 100; bps <= 10000; bps += 100) {
      expect(listPercentToBps(bpsToListPercent(bps))).toBe(bps)
    }
  })

  // An effective rate is a list rate times a discount and need not land on a
  // whole part per million.
  it('formats a fractional effective rate', () => {
    expect(formatPpmPercent(470.25)).toBe('0.047025%')
    expect(formatPpmPercent(399)).toBe('0.0399%')
  })
})
