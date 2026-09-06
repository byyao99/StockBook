import { describe, expect, it } from 'vitest'
import { formatNewsTime, utcDate } from './newsTime'
import { UNKNOWN } from './money'

// A fixed "now" so these cases describe a rule rather than the wall clock.
const now = new Date('2026-08-29T12:00:00Z')

describe('formatNewsTime', () => {
  // Under a minute there is no useful number to show.
  it('reads as just now inside the first minute', () => {
    expect(formatNewsTime('2026-08-29T11:59:30Z', now)).toBe('just now')
  })

  it('counts minutes, then hours, inside the first day', () => {
    expect(formatNewsTime('2026-08-29T11:48:00Z', now)).toBe('12m ago')
    expect(formatNewsTime('2026-08-29T07:00:00Z', now)).toBe('5h ago')
    // 23 hours is still an hour count; the next one is not.
    expect(formatNewsTime('2026-08-28T13:00:00Z', now)).toBe('23h ago')
  })

  // Past a day a relative age stops being useful: "37h ago" is arithmetic the
  // reader has to do.
  it('falls back to the UTC date past a day', () => {
    expect(formatNewsTime('2026-08-27T23:00:00Z', now)).toBe('2026-08-27')
  })

  // The date is the same for every reader. Rendering in the reader's locale
  // would put a Taipei evening and a New York morning on different days.
  it('does not shift the date with the reader zone', () => {
    // 23:30 UTC is already the 28th in Taipei and still the 27th in New York;
    // both must read the stored day.
    expect(formatNewsTime('2026-08-27T23:30:00Z', now)).toBe('2026-08-27')
  })

  // An unparseable stamp is unknown, not now. A headline stamped with the moment
  // it was read would sort to the top of the feed forever.
  it('returns the unknown marker rather than substituting now', () => {
    expect(formatNewsTime('whenever', now)).toBe(UNKNOWN)
    expect(formatNewsTime('', now)).toBe(UNKNOWN)
  })

  // A provider's clock running slightly ahead is not worth showing a reader.
  it('treats a future stamp as just now rather than a negative age', () => {
    expect(formatNewsTime('2026-08-29T12:05:00Z', now)).toBe('just now')
  })
})

describe('utcDate', () => {
  it('slices the day out of the stamp rather than parsing it', () => {
    expect(utcDate('2026-08-29T23:59:59Z')).toBe('2026-08-29')
  })

  it('returns the unknown marker for anything that is not a date', () => {
    expect(utcDate('nope')).toBe(UNKNOWN)
  })
})
