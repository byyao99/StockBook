/**
 * Estimating what a trade costs in fees.
 *
 * This lives on the client on purpose. `Transaction.Fee` records what the trade
 * actually cost — the number printed on a contract note — and that is a fact,
 * not something derivable from quantity and price. It is unlike
 * `Transaction.NetAmount`, which the server always computes precisely because a
 * client must not be able to assert a total that disagrees with its own line
 * items. Computing the fee server-side would write an estimate into a field
 * that means "what happened", so the estimate stays here, at the edge, where it
 * fills a form field the user can overwrite.
 *
 * Every function is pure and has no DOM dependency, so the arithmetic can be
 * tested directly — the same reason `curveMath.ts` and `positionMath.ts` exist.
 */
import type {
  FeeProfile,
  FeeProfileKey,
  Instrument,
  TransactionSide,
} from './types'

/** Parts per million, the scale fee rates are stored in. See FeeProfile. */
const PPM = 1_000_000

/** A discount that takes nothing off, in basis points. See FeeProfile. */
const FULL_PRICE_BPS = 10_000

/**
 * The fraction of the list commission a profile actually pays.
 *
 * A stored zero means "no discount", not "free" — the column is added by a
 * migration that leaves every existing row at zero, and reading that literally
 * would make every profile predating it commission-free. A user who genuinely
 * pays nothing sets the rate to 0, which says so directly.
 */
export function discountFactor(profile: FeeProfile): number {
  return profile.discount_bps > 0 ? profile.discount_bps / FULL_PRICE_BPS : 1
}

/**
 * The rate actually charged, in parts per million — the list rate after the
 * broker's discount.
 *
 * The result need not be a whole ppm, and is deliberately not rounded: 0.1425%
 * at 33% of list is 470.25 ppm, and rounding it to 470 before it ever meets a
 * trade
 * would throw away precision the money can still use. This figure is for
 * display; `estimateFee` multiplies from the stored parts and rounds once.
 */
export function effectiveRatePpm(profile: FeeProfile): number {
  return profile.rate_ppm * discountFactor(profile)
}

/**
 * Picks the profile a trade should default to, or null when nothing sensible
 * applies and the user should enter the fee by hand.
 *
 * This is a suggestion, never a constraint. The form keeps the choice editable
 * because all three inputs can be wrong for a given account: the provider
 * occasionally classifies an ETF as an ordinary share, a broker may charge
 * something no profile describes, and `asset_type` is empty for every
 * instrument added before it was recorded.
 */
export function defaultProfileKey(
  instrument: Instrument | undefined,
  recurring: boolean,
): FeeProfileKey | null {
  if (!instrument) return null

  let region: 'tw' | 'us'
  switch (instrument.market) {
    case 'TWSE':
    case 'TPEX':
      region = 'tw'
      break
    case 'NYSE':
    case 'NASDAQ':
      region = 'us'
      break
    default:
      // OTHER is whatever this system could not place, so it has no fee terms
      // to assume. Guessing one would be worse than leaving the field blank.
      return null
  }

  if (recurring) return `${region}_recurring` as FeeProfileKey
  // An unclassified instrument falls back to the ordinary-share profile rather
  // than to nothing: it is the common case, and the two differ only in the
  // sell-side tax, which the user can correct on the form.
  const kind = instrument.asset_type.toUpperCase() === 'ETF' ? 'etf' : 'stock'
  return `${region}_${kind}` as FeeProfileKey
}

/**
 * Estimates the total cost of a trade in integer minor units, or null when
 * there is nothing to estimate.
 *
 * ```
 * commission = max(round(gross × rate_ppm / 1e6 × discount), min_fee)
 * tax        = sells only: round(gross × sell_tax_ppm / 1e6)
 * ```
 *
 * The discount multiplies the commission and nothing else. The minimum is a
 * floor on what is actually paid, so it applies after the discount; the sell tax
 * is levied by the government and no broker can discount it. That tax lands in
 * the same field because `Transaction.Fee` records what the trade cost in total,
 * and a sale's proceeds are net of both.
 *
 * Everything is multiplied at full precision and rounded once, at the end. The
 * effective rate is frequently not a whole part per million — 0.1425% at 33% of
 * list is 470.25 — so rounding the rate first would lose accuracy the trade could
 * otherwise have kept.
 *
 * Returns null rather than 0 whenever the answer is unknown, following the rule
 * the rest of this system keeps: a zero fee is a claim that the trade was free.
 */
export function estimateFee(
  profile: FeeProfile | undefined,
  side: TransactionSide,
  quantity: number,
  priceCents: number,
): number | null {
  if (!profile) return null
  // A dividend's fee field holds tax withheld at source — Taiwan's supplementary
  // health premium, or the 30% the US withholds from a foreign holder. That is
  // a different charge with different rates from a brokerage commission, and
  // applying one to the other would produce a plausible-looking wrong number.
  if (side === 'dividend') return null
  if (!Number.isFinite(quantity) || !Number.isFinite(priceCents)) return null
  if (quantity <= 0 || priceCents <= 0) return null

  const gross = quantity * priceCents
  const commission = Math.max(
    Math.round((gross * profile.rate_ppm * discountFactor(profile)) / PPM),
    profile.min_fee,
  )
  const tax = side === 'sell' ? Math.round((gross * profile.sell_tax_ppm) / PPM) : 0
  return commission + tax
}

/** Finds a profile by key in a loaded set. */
export function findProfile(
  profiles: FeeProfile[],
  key: FeeProfileKey | '',
): FeeProfile | undefined {
  return key === '' ? undefined : profiles.find((p) => p.key === key)
}

/** The human label for a profile, used by the settings page and the trade form. */
export const FEE_PROFILE_LABELS: Record<FeeProfileKey, string> = {
  tw_stock: 'Taiwan stock',
  tw_etf: 'Taiwan ETF',
  tw_recurring: 'Taiwan savings plan',
  us_stock: 'US stock',
  us_etf: 'US ETF',
  us_recurring: 'US savings plan',
}

/** The currency a profile's minimum fee is denominated in, implied by its key. */
export function profileCurrency(key: FeeProfileKey): 'TWD' | 'USD' {
  return key.startsWith('tw_') ? 'TWD' : 'USD'
}

/** How a profile charges: a share of the trade, or a fixed amount per trade. */
export type FeeChargeMode = 'rate' | 'flat'

/**
 * Whether a profile charges a percentage or a flat amount per trade.
 *
 * Not every broker quotes a rate. A flat "US$3 an ETF trade, US$0.1 a scheduled
 * purchase" is common, and so is a plain 0.08% with no discount behind it — a
 * discount off a list rate is one way terms are expressed, not the only one.
 *
 * The mode is **derived, never stored**, because a flat fee is already the
 * degenerate case of the general formula: with a zero rate,
 * `max(0, min_fee)` is exactly the flat charge for every trade size, so the
 * arithmetic in `estimateFee` needs no branch and cannot disagree with the
 * label. Storing a mode beside the numbers would create a second source of
 * truth that a stale row could contradict. The consequence is that a profile
 * saved as 0% with a minimum reads back as flat — which it is.
 */
export function chargeMode(profile: FeeProfile): FeeChargeMode {
  return profile.rate_ppm === 0 && profile.min_fee > 0 ? 'flat' : 'rate'
}
