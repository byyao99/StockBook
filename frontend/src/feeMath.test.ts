import { describe, expect, it } from 'vitest'
import {
  chargeMode,
  defaultProfileKey,
  effectiveRatePpm,
  estimateFee,
  profileCurrency,
} from './feeMath'
import type { FeeProfile, FeeProfileKey, Instrument } from './types'

// The defaults the server ships, which is what these cases are measured against.
const PROFILES: Record<FeeProfileKey, FeeProfile> = {
  tw_stock: { key: 'tw_stock', rate_ppm: 1425, min_fee: 2000, sell_tax_ppm: 3000, discount_bps: 0 },
  tw_etf: { key: 'tw_etf', rate_ppm: 1425, min_fee: 2000, sell_tax_ppm: 1000, discount_bps: 0 },
  tw_recurring: { key: 'tw_recurring', rate_ppm: 1000, min_fee: 100, sell_tax_ppm: 3000, discount_bps: 0 },
  us_stock: { key: 'us_stock', rate_ppm: 1500, min_fee: 1500, sell_tax_ppm: 0, discount_bps: 0 },
  us_etf: { key: 'us_etf', rate_ppm: 1500, min_fee: 1500, sell_tax_ppm: 0, discount_bps: 0 },
  us_recurring: { key: 'us_recurring', rate_ppm: 1500, min_fee: 100, sell_tax_ppm: 0, discount_bps: 0 },
}

function instrument(market: string, assetType = ''): Instrument {
  return {
    id: 'i1',
    symbol: 'X',
    name: 'X',
    market,
    currency: market.startsWith('T') ? 'TWD' : 'USD',
    asset_type: assetType,
    last_price: null,
    price_updated_at: null,
    quote_checked_at: null,
    created_at: '',
    updated_at: '',
  }
}

describe('estimateFee', () => {
  // 1000 shares at NT$585 is NT$585,000; 0.1425% of it is NT$833.625, held to
  // the hundredth like every other amount here — comfortably above the NT$20
  // floor. A real broker truncates this to a whole NT$833; the difference is
  // under a dollar on an editable estimate, so the arithmetic stays uniform
  // rather than modelling each broker's rounding.
  it('charges commission alone on a buy', () => {
    expect(estimateFee(PROFILES.tw_stock, 'buy', 1000, 58500)).toBe(83363)
  })

  // The same sale also pays 0.3% transaction tax: NT$833.63 + NT$1,755 = NT$2,588.63.
  // Leaving the tax out would understate every Taiwanese sale by roughly twice
  // the commission.
  it('adds the transaction tax on a sell', () => {
    expect(estimateFee(PROFILES.tw_stock, 'sell', 1000, 58500)).toBe(258863)
  })

  // An ETF pays 0.1% rather than 0.3%: NT$833.63 + NT$585 = NT$1,418.63. This is the
  // only thing distinguishing the two Taiwanese profiles, and it is why the
  // system bothers to record what an instrument is.
  it('taxes an ETF sale at the lower rate', () => {
    expect(estimateFee(PROFILES.tw_etf, 'sell', 1000, 58500)).toBe(141863)
  })

  // 1 share at NT$50 earns NT$0.07 of commission, so the NT$20 floor decides.
  // Without it a small trade would be recorded as almost free.
  it('applies the minimum fee when the rate falls short', () => {
    expect(estimateFee(PROFILES.tw_stock, 'buy', 1, 5000)).toBe(2000)
  })

  // 10 shares at $500 is $5,000; 0.15% is $7.50, so the $15 floor decides. No
  // transaction tax applies to a US sale.
  it('floors a US sale and charges no tax', () => {
    expect(estimateFee(PROFILES.us_stock, 'sell', 10, 50000)).toBe(1500)
  })

  it('charges a savings plan at its own lower floor', () => {
    // 100 shares at NT$50 is NT$5,000; 0.1% is NT$5, above the NT$1 floor.
    expect(estimateFee(PROFILES.tw_recurring, 'buy', 100, 5000)).toBe(500)
  })

  // A dividend's fee field holds tax withheld at source, which has nothing to
  // do with a brokerage commission. Returning 0 would be a claim that nothing
  // was withheld; null leaves the field for the user.
  it('refuses to estimate a dividend', () => {
    expect(estimateFee(PROFILES.tw_stock, 'dividend', 1000, 200)).toBeNull()
  })

  // An unknown answer is null, never 0 — a zero fee asserts the trade was free.
  it('returns null rather than zero when there is nothing to go on', () => {
    expect(estimateFee(undefined, 'buy', 1000, 58500)).toBeNull()
    expect(estimateFee(PROFILES.tw_stock, 'buy', 0, 58500)).toBeNull()
    expect(estimateFee(PROFILES.tw_stock, 'buy', 1000, 0)).toBeNull()
    expect(estimateFee(PROFILES.tw_stock, 'buy', Number.NaN, 58500)).toBeNull()
  })

  // A user whose broker charges nothing saves zeros, and the estimate must
  // honour them rather than fall back to something else.
  it('honours a genuinely free profile', () => {
    const free: FeeProfile = { key: 'us_stock', rate_ppm: 0, min_fee: 0, sell_tax_ppm: 0, discount_bps: 0 }
    expect(estimateFee(free, 'buy', 10, 50000)).toBe(0)
  })
})

describe('defaultProfileKey', () => {
  it('reads the region from the market', () => {
    expect(defaultProfileKey(instrument('TWSE', 'EQUITY'), false)).toBe('tw_stock')
    expect(defaultProfileKey(instrument('TPEX', 'EQUITY'), false)).toBe('tw_stock')
    expect(defaultProfileKey(instrument('NYSE', 'EQUITY'), false)).toBe('us_stock')
    expect(defaultProfileKey(instrument('NASDAQ', 'ETF'), false)).toBe('us_etf')
  })

  it('is case-insensitive about the provider\'s wording', () => {
    expect(defaultProfileKey(instrument('TWSE', 'etf'), false)).toBe('tw_etf')
  })

  // An instrument added before asset_type was recorded has none. Falling back to
  // the ordinary-share profile keeps the form useful; the two differ only in the
  // sell-side tax, which the user can correct.
  it('falls back to the stock profile when unclassified', () => {
    expect(defaultProfileKey(instrument('TWSE', ''), false)).toBe('tw_stock')
  })

  // A savings plan is how the trade was made, not what was traded, so it
  // overrides the instrument's own classification.
  it('lets a savings plan override the asset type', () => {
    expect(defaultProfileKey(instrument('TWSE', 'ETF'), true)).toBe('tw_recurring')
    expect(defaultProfileKey(instrument('NASDAQ', 'EQUITY'), true)).toBe('us_recurring')
  })

  // OTHER is whatever the system could not place, so it has no terms to assume.
  it('suggests nothing for a market it cannot place', () => {
    expect(defaultProfileKey(instrument('OTHER', 'EQUITY'), false)).toBeNull()
    expect(defaultProfileKey(undefined, false)).toBeNull()
  })
})

describe('profileCurrency', () => {
  it('implies the currency from the key', () => {
    expect(profileCurrency('tw_etf')).toBe('TWD')
    expect(profileCurrency('us_recurring')).toBe('USD')
  })
})

describe('broker discount', () => {
  // A discounted profile keeps the list rate on file and multiplies at use. The
  // A 28%-of-list discount on the standard 0.1425% is 0.0399%.
  it('multiplies the commission and nothing else', () => {
    const discounted: FeeProfile = {
      key: 'tw_stock', rate_ppm: 1425, min_fee: 100, sell_tax_ppm: 3000, discount_bps: 2800,
    }
    // 1000 × NT$585 = NT$585,000; 0.0399% of it is NT$233.415.
    expect(estimateFee(discounted, 'buy', 1000, 58500)).toBe(23342)
    // The sell tax is statutory and is not discounted: NT$233.42 + NT$1,755.
    expect(estimateFee(discounted, 'sell', 1000, 58500)).toBe(23342 + 175500)
  })

  // 0.1425% at 33% of list is 470.25 parts per million — not a whole one. Rounding
  // the rate before it met the trade would lose precision the money can keep,
  // so the multiplication runs at full precision and only the fee is rounded.
  it('does not round the rate, only the money', () => {
    const odd: FeeProfile = {
      key: 'tw_stock', rate_ppm: 1425, min_fee: 0, sell_tax_ppm: 0, discount_bps: 3300,
    }
    expect(effectiveRatePpm(odd)).toBeCloseTo(470.25, 10)
    // 1000 × NT$585 × 0.047025% = NT$275.09625 -> 27510 minor units.
    expect(estimateFee(odd, 'buy', 1000, 58500)).toBe(27510)
    // Rounding the rate to 470 ppm first would have given 27495 — off by NT$0.15
    // on one trade, and compounding across a book.
    expect(estimateFee(odd, 'buy', 1000, 58500)).not.toBe(27495)
  })

  // The minimum is a floor on what is actually paid, so it applies after the
  // discount rather than before it.
  it('applies the minimum after the discount', () => {
    const discounted: FeeProfile = {
      key: 'tw_stock', rate_ppm: 1425, min_fee: 2000, sell_tax_ppm: 0, discount_bps: 2800,
    }
    // 100 × NT$50 = NT$5,000; 0.0399% is NT$1.995, so the NT$20 floor decides.
    expect(estimateFee(discounted, 'buy', 100, 5000)).toBe(2000)
  })

  // A migration leaves every existing row at zero. Reading that literally would
  // make every profile predating the column commission-free.
  it('reads a stored zero as no discount, never as free', () => {
    const legacy: FeeProfile = {
      key: 'tw_stock', rate_ppm: 1425, min_fee: 0, sell_tax_ppm: 0, discount_bps: 0,
    }
    expect(effectiveRatePpm(legacy)).toBe(1425)
    expect(estimateFee(legacy, 'buy', 1000, 58500)).toBe(83363)
  })

  it('treats a hundred percent of list as full price', () => {
    const full: FeeProfile = {
      key: 'tw_stock', rate_ppm: 1425, min_fee: 0, sell_tax_ppm: 0, discount_bps: 10000,
    }
    expect(effectiveRatePpm(full)).toBe(1425)
  })
})

// Not every broker quotes a rate. Cathay charges US$3 an ETF trade and US$0.10
// a scheduled purchase whatever the size, and 0.08% flat on a US share with no
// discount behind it. All three have to be expressible, and the first two are the
// degenerate case of the shared formula: a zero rate leaves the minimum as the
// whole charge.
describe('a fixed charge per trade', () => {
  const flatETF: FeeProfile = {
    key: 'us_etf', rate_ppm: 0, min_fee: 300, sell_tax_ppm: 0, discount_bps: 0,
  }
  const flatRecurring: FeeProfile = {
    key: 'us_recurring', rate_ppm: 0, min_fee: 10, sell_tax_ppm: 0, discount_bps: 0,
  }

  it('charges the same amount at every trade size', () => {
    expect(estimateFee(flatETF, 'buy', 1, 50000)).toBe(300)
    expect(estimateFee(flatETF, 'buy', 500, 50000)).toBe(300)
    expect(estimateFee(flatETF, 'sell', 500, 50000)).toBe(300)
    expect(estimateFee(flatRecurring, 'buy', 3, 45000)).toBe(10)
  })

  it('is recognised as a fixed charge rather than a 0% rate', () => {
    expect(chargeMode(flatETF)).toBe('flat')
    expect(chargeMode(flatRecurring)).toBe('flat')
  })

  // A plain rate with no discount is still a rate, and so is one whose broker
  // genuinely charges nothing at all — the mode turns on the minimum, since
  // that is the only thing a zero rate could be charging.
  it('leaves a percentage row alone', () => {
    const plain: FeeProfile = {
      key: 'us_stock', rate_ppm: 800, min_fee: 0, sell_tax_ppm: 0, discount_bps: 0,
    }
    expect(chargeMode(plain)).toBe('rate')
    // 10 shares at US$500 is US$5,000; 0.08% of it is US$4.
    expect(estimateFee(plain, 'buy', 10, 50000)).toBe(400)

    const free: FeeProfile = {
      key: 'us_stock', rate_ppm: 0, min_fee: 0, sell_tax_ppm: 0, discount_bps: 0,
    }
    expect(chargeMode(free)).toBe('rate')
    expect(estimateFee(free, 'buy', 10, 50000)).toBe(0)
  })

  // A Taiwanese broker charging a flat commission still owes the government its
  // transaction tax, which no arrangement between the two of them can waive.
  it('still adds the sell tax', () => {
    const flatTW: FeeProfile = {
      key: 'tw_stock', rate_ppm: 0, min_fee: 2000, sell_tax_ppm: 3000, discount_bps: 0,
    }
    // 1000 × NT$585 = NT$585,000; 0.3% of it is NT$1,755.
    expect(estimateFee(flatTW, 'sell', 1000, 58500)).toBe(2000 + 175500)
  })
})
