# The equity curve and the benchmark

_Read before changing `curve.go`, `benchmarks.go`, or `EquityCurveChart`._

## The equity curve

`GET /api/v1/reports/curve?from=&to=` (`internal/db/curve.go`) returns the daily history, one entry per currency. Its bounds select **sessions, not trades** — the whole ledger is always folded and the window only decides which days are reported, so narrowing to last month still shows holdings bought years ago.

**`MarketValue` is not a return, and this is the whole difficulty.** A book that doubles its contributions doubles its market value without having earned anything, so drawdown or volatility measured on that curve would report saving as performance and a withdrawal as a crash. `Index` is the figure that answers performance: a notional 100 chained from daily returns with the contributions divided out.

```
r     = (value at close − money paid in today) / value at the previous close − 1
index = index yesterday × (1 + r)
```

**Flows come out of the numerator, not into the denominator**, and that choice is load-bearing. The only price stored for a session is its close, so a purchase is most nearly a purchase *at* the close — money that has not been at work yet. Counting it into the denominator as if it had been there since the open divides the day's real gain by a base that money never earned on, and a large deposit silently drags the day's return toward zero. `TestCurveIndexIgnoresContributions` is the test that catches this: two books holding the same stock over the same days must report the same return even when one keeps adding to the position. It failed on the first implementation, which used the denominator form.

The first session with anything in it anchors the index at 100 and reports no return, there being no previous close to measure against. A trade dated on a day the market did not open folds into the next session rather than being dropped. One artifact remains, and it is the standard one: a trade filled away from the close credits that difference to the day it happened, since the flow subtracted is the cash that actually moved while the shares are valued at the close. Pricing the flow at the close instead would hide a genuinely good fill, which the money-weighted return reports more directly anyway.

**An instrument whose stored history does not reach the day it was first traded is excluded whole, trades and all**, and counted in `WithoutHistory` — the same rule the returns report follows. Counting it short would understate the book for every day before its prices begin, which looks exactly like a real drawdown. Interior gaps are different and are covered by carrying the last close forward (`priceSeries.on`), which never carries *backwards*: that is the case the exclusion exists for.

`TWRBps` (time-weighted) and the money-weighted `XIRRBps` are deliberately both reported and are not the same question — time-weighted measures the picking, money-weighted measures the picking *and* the timing of what was put in. Both annualize actual/365 so the two figures on one book are measured the same way.

## The benchmark

`models.Benchmark` is the instrument a book is measured against, **one per currency**, because measuring a TWD book against a USD index would report the exchange rate as performance. `db.SaveBenchmark` refuses a mismatch (`ErrBenchmarkCurrency` → 409), which is the quote package's currency rule applied to the one place a user could otherwise break it by hand.

**It is chosen on `/reports`, beside the chart it changes**, for the reason the Sync prices button is there too: an absent comparison is noticed where the comparison would be. Putting it on `/reports` also removes a control the settings-page version needed — that page had to ask which currency, while here the currency is whichever block the picker sits in, so a reader is never asked about one they do not hold. It saves on pick rather than behind a Save button, because the confirmation is the line appearing on the chart a moment later. Two traps come with that: `PUT /settings/benchmarks` takes the **whole set** and clears what is left out, so the view holds every currency's choice and sends them all — setting USD must not silently wipe TWD; and `InstrumentPicker`'s own "change" link clears its value to reopen the search, which means "pick a different one" and not "stop comparing", so an empty id is ignored and removing is what the button is for. Escape closes the picker, which is what makes "change" reversible at all.

**There is deliberately no default**, and this is the one settings endpoint that does not merge one in. A default would have to name an instrument, and an instrument exists here only once the provider has priced it (`POST /instruments`) — a read that invented master data would break that. So a book is unmeasured until its owner picks something and the curve says so in words.

**The comparison runs inside the curve's own loop, not beside it.** A second account starts with the book's value on the first plotted session, grows by the benchmark's return each day, and takes the same flows on the same days. That recursion is the book's own, rearranged: the index is chained from `(value - flow) / previous value`, so `value = previous × (1 + r) + flow` is an identity the book already satisfies. Running the benchmark by the same identity is what makes the two ends comparable rather than merely adjacent, and it needs **no cash balance** — a flow is added at the close, exactly the convention the index already assumes. Sharing the loop also means the two can never disagree about which sessions exist or what the flows were.

**It is total return, never price.** The book's index already includes the dividends it received (a payout is a negative flow, so the index rises by it), and comparing that against a benchmark's bare price would dock the benchmark every distribution it ever made while crediting the book with all of its own. That is not small — 0050 yields around 3% and VOO around 1.3% — and it would flatter the reader by a compounding margin in the one direction nobody thinks to question. `models.DividendEvent` is already stored, since the price sync asks for distributions in the same request, so there is no excuse for the price series. `TestBenchmarkCountsItsDistributions` pins it.

Two figures come out: `BenchmarkTWRBps`, measured the same way over the same sessions as the book's own (a comparison between numbers measured differently is not a comparison), and `BenchmarkValue` — what the book would be worth now had the same money arrived on the same days into the benchmark instead. The claim is deliberately the narrow one: money taken *out* of this book went somewhere the system does not model, so it is never "you would be richer today", exactly as the hindsight report refuses that step.

`BenchmarkExhausted` covers the case where the book's withdrawals would have emptied the synthetic account. It is floored at zero because shares nobody owns cannot be sold — a clamp, not a fudge — and from that point the two are no longer running the same money, so the value comparison is flagged while the index comparison stands.

**Both benchmark figures on a `CurvePoint` are zero when no comparison ran**, and the gate is `CurrencyCurve.Benchmark` being nil. Leaving the index at its base instead would hand every reader a flat line at 100 that nothing had measured — a comparison they never made, rendered as if it had come out even. The frontend passes the symbol into `seriesOf` rather than reading the points, for the same reason.

`db.EarliestHistoryNeeded` counts a benchmark as a third reason to fetch history, back to the **book's own first trade** rather than the day it was chosen. An instrument nobody trades is the normal case for a benchmark — the whole point is to hold something else — which is exactly what the old first-trade rule declined to fetch.
