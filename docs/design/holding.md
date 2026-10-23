# A holding's own page

_Read before changing `holding.go` or `HoldingView.vue`._

## A holding's own page

`GET /api/v1/positions/:instrument_id` (`internal/db/holding.go`) answers "how has *this* one done?", which nothing else here could ask: the list on `/positions` answers what is owned and the reports on `/reports` answer how a period went, and a single position falls between them.

**It is deliberately lifetime, with no period bounds**, which is the one place it differs from every report beside it. A question about one holding is not improved by hiding half its history — the average cost every figure on the page is built on was formed by trades a window would have excluded, so a period-filtered version would report a cost basis the period did not produce.

The decomposition is the realized report's, applied to one instrument: `TradingPL` against `Dividends`, both read from the stamps of invariant 6 rather than recomputed, with `UnstampedEntries` saying whether it is complete. Beside them sit the cash figures — `Invested`, `Proceeds`, `Dividends` as the money actually moved, fees included — which answer a **different** question and are not an alternative spelling of the same one: trading profit measures the sales against the average cost they released, and the two differ by whatever is still held.

`XIRRBps` is `models.XIRR` over this holding's own flows, closed by the market value of what is still held at `asOf`, and it inherits the book-wide report's rule exactly: an open holding with no quote takes the whole figure out rather than being valued at zero, and `Unavailable` says so in words. A **closed** holding needs no quote, its flows being complete and its ending value genuinely zero.

**Below a year of history there is no rate**, here as everywhere — see *Annual rates need a year* in `reports.md`. The span runs from the first entry to `asOf` while shares are held (the closing flow is dated then), and to the **last entry** once they are gone, so a quick round trip stays short however long ago it was.

`YieldOnCostBps` is the last 365 days of dividends against what the shares still held cost. **Against cost, not market value**, because that is the question a long-term holder is actually asking — what the money they committed now pays them, which rises as a position matures — while the market-value yield is on every quote page already and describes the price rather than this book. It is a **trailing** figure rather than a forecast because this system does not predict distributions; that understates a holding bought part-way through the year, which is honest in the direction that matters and is why the window is named on screen. It is **nil, never 0, once the shares are gone**: there is no cost left to divide by, and 0% would read as an income holding that stopped paying. A trailing total of zero against a real cost basis is a genuine zero and reported as one.

The chart is the equity curve narrowed by `?instrument_id` (`db.InstrumentCurve`), which narrows the **ledger and nothing else**: contributions are still divided out of the index, and the benchmark still runs the same money through the chosen index — which at this scale asks the sharper question of whether this one holding was worth owning instead of it. Sharing `currencyCurve` rather than writing a second fold is what keeps the two scales from disagreeing about what a session or a flow is.
