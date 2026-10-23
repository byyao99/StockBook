# Realized, hindsight, and money-weighted (XIRR) reports

_Read before changing anything under `/reports` or `models.XIRR`._

## Reporting realized results

`GET /api/v1/reports/realized?from=&to=` (`internal/db/reports.go`) answers what the caller banked over a period, by summing the stamps from invariant 6. It returns a **slice, one entry per currency** with a per-instrument breakdown inside it — the same rule `PortfolioSummary` follows, for the same reason. The report is not paginated: it is a summary, so it returns `{"data": [...]}` with no `pagination` block.

**Trading and dividends are reported apart as well as together.** `TradingPL` is what the sales banked, `Dividends` is what was paid out for holding, and `RealizedPL` is their sum. They are taxed differently in most places, and a book that lost on price while making it back on income must not read as a flat year. `Proceeds` and `Cost` describe the sales alone, so the identity to check is `TradingPL == Proceeds - Cost`, **never** against the combined total. `Cost` is reported rather than left to be inferred because it is what a tax form asks for next to the proceeds, and it is not recoverable from the ledger alone since it comes from the average held at the moment of each sale. For the same reason the UI's return percentage is measured against `TradingPL`: dividends are earned by holding shares, not by selling them.

**A bare `to=YYYY-MM-DD` covers the whole of that day.** `handlers.parseDateRange` stretches a date-only upper bound to the last instant of it, because `traded_at <= 2026-12-31` means midnight and would drop every trade made on the closing day of the range — a yearly report omitting its own last entry. `GET /transactions` shares the helper, so its `?to` behaves the same way. Bare dates are read as UTC, which is the calendar the rest of the system speaks: the trade form stores a chosen date at **noon UTC** precisely so the day a trade belongs to does not depend on the reader's zone, and the ledger displays it by slicing the first ten characters off the UTC timestamp.

## Hindsight on sell decisions

`GET /api/v1/reports/hindsight?from=&to=` (`internal/db/hindsight.go`) answers what the sales in a period would be worth had the shares never been sold. Every broker shows what a sale banked; almost none shows what it gave up. Shape is a **slice, one entry per currency**, with per-instrument rows inside — the same as `RealizedReport`, and it shares `parseDateRange`, so `?to=YYYY-MM-DD` covers the whole of that day here too. Unlike `/reports/returns` it takes a period happily: the comparison is always against *today's* quote, so no historical price is needed to ask it about a past year.

**The comparison is exact and needs no replay.** Refolding the ledger with the sells removed looks like the obvious implementation, but the difference between the two worlds collapses algebraically:

```
held on to everything = market value of every share ever bought − what they cost
what actually happened = market value of the shares still held + sale proceeds − what they cost
difference             = sale proceeds − (shares sold × today's price)
```

The cost paid cancels, and **so does every dividend** — which is the load-bearing part. Dividends were recorded against the shares actually held, so a replay would have had to either under-count them or invent the payouts the unsold shares would have received. Neither is necessary, and inventing them would have put a fabricated number inside a report whose entire value is that it is factual. Only the sold shares and the current quote are needed, which is also why the answer decomposes per sale.

**What the report does not model is the cash.** Money from a sale usually buys something else, and there is no cash balance here to follow it into (deliberately out of scope). So the claim is the narrow, factual one — *these shares fetched this much, and they would be worth this much now* — and never "you would be richer today". The case where those two readings diverge most is a sale followed by re-entry into the same instrument, so `SharesHeld` is reported on every row and the UI renders it as the reader's signal that a round trip, not a loss, is what they are looking at. Netting it out would require exactly the cash model that is out of scope.

`SellingGain == Proceeds - ValueIfHeld` is the identity to check, and it holds on both the rows and the total. Rows come back **worst decision first** (ascending `SellingGain`) rather than largest-first like the realized report: this one is read to learn something, and what there is to learn sits at the regret end. Sales of an instrument with no quote have nothing to be compared against and land in `UnpricedSales` rather than the totals — valuing those shares at zero would report every one of those sales as a masterstroke.

## Money-weighted return (XIRR)

`GET /api/v1/reports/returns` (`internal/db/returns.go`) answers *how fast* the book grew, which no other figure here can. A gain-over-cost percentage is blind to time and to size: doubling 10,000 over five years and doubling 1,000,000 over one both read as `+100%`, and a well-timed second purchase counts for no more than the first. XIRR weights every dollar by how long it was actually at work, which is what makes a book comparable with another book, or with a savings rate. It returns a **slice, one entry per currency**, for the same reason every other total does.

The cash flows *are* the ledger, seen from the user's pocket — a buy is money out, a sale or dividend is money in — plus **one final payment in at `asOf`: the market value of what is still held.** Without that closing flow every unsold share would measure as money that never came back. `ReturnsReport` takes `asOf` as a parameter (the handler passes `time.Now()`) purely so the arithmetic is testable against fixed dates.

**It answers one of two questions and says which.** With no bounds it measures **since inception**, closing on the live quote for whatever is still held. With `from`/`to` it measures **that window**, which is a different question rather than a filtered version of the same one: a period rate has to open with the position the period was entered holding, so the book's value on the from-date enters the flows as money out and its value on the to-date as money in. Without that opening flow a book held untouched through a rising year has no outflow at all and no rate.

The window was refused for a long time, on the grounds that the book's value on a past date was not recoverable from current quotes alone. `models.DailyClose` lifted that and nobody noticed for a while — a constraint that has expired is worth correcting rather than deleting, or the next reader re-derives the old limit. A windowed report is valued by `db.priceLedger`, the same machinery the equity curve runs on, and therefore inherits the curve's exclusion rather than the live-quote one: an instrument whose stored history does not reach the day it was first traded is dropped whole and counted in `WithoutHistory`.

**The closing figure is dated at the last session it could actually be struck on**, not at the bound asked for. Asking for "this year" in September would otherwise price the book in December, spreading the same gain over a longer assumed period and reporting a lower annual rate with nothing on screen to say why. A trade dated exactly on the opening bound is inside the opening value and never also a flow — counting both charges the period twice for one purchase.

`internal/db/valuation.go` holds what both reports need: which instruments can be valued at all, and what price stands on a day the market did not open (`priceSeries.on` carries the last close forward, never backwards). Two copies of that would drift, which is the same reason `curveMath.ts` mirrors the valuation rules rather than inventing its own.

**An open holding with no quote takes its entire history out of the report** — its purchases along with its unknown value. This is the "unknown is not zero" rule applied to a figure that spans history rather than a moment, and the two alternatives are both worse: valuing it at zero shows the holding as a wipeout, and keeping its buys while dropping only its ending value reports *exactly the same wipeout* with nothing to say anything was missing. Omitting the instrument answers a smaller question truthfully, and `PricedPositions` against `OpenPositions` is what tells the caller how much of the book that is — the same pair `CurrencySummary` reports. A **closed** holding needs no quote and is always counted: its flows are complete and its ending value is genuinely zero.

`models.XIRR` (`internal/models/xirr.go`) is pure, has no database dependency, and is **actual/365**, the convention every published XIRR uses — matching it beats being astronomically right, because a number the user can reconcile against a broker statement is worth more than one that agrees with nothing. The root is found by **bisection, not Newton-Raphson**: a ledger with flows in both directions gives the present value more than one turning point, and a derivative step from a bad guess leaves the domain — Newton's divergence is the classic source of a spreadsheet's `#NUM!`. Bisection cannot diverge, needs no starting guess, and the whole search is a few hundred passes over a list built once per page load.

**The rate crosses the wire as integer basis points** (`XIRRBps`, 1234 = 12.34%), for the reason every amount crosses it as integer minor units: the rounding is decided once, on the server. It is a `*int64` and is **nil, never 0**, when no rate could be found — `Unavailable` then carries the reason in the user's own words, because a book that cannot be measured has not broken even. The three refusals (`ErrOneDirection`, `ErrSameInstant`, `ErrNoRate`) are separate errors so each can say what actually went wrong.

The UI puts this on `/positions` rather than under Realized, because the closing figure of the calculation is the market value of current holdings — exactly what the cards there already show.
