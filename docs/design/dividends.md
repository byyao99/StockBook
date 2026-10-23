# Cash dividends

_Read before changing dividend entries, `DividendEvent`, or the pending-dividend prompt._

## Cash dividends

`models.SideDividend` is a third kind of ledger entry beside buy and sell, not a separate resource: it goes in through `POST /transactions`, folds through `Apply` like everything else, and carries a `RealizedPL` stamp like a sell.

**A dividend is income, not a refund of what the shares cost.** It adds its payout to `RealizedPL` and leaves `Quantity` and `CostBasis` alone. The alternative convention — deducting the payout from the cost basis — is how a US return of capital is treated, and adopting it here would make an ordinary Taiwanese cash dividend quietly inflate the paper gain still showing on shares the user holds, instead of appearing as the income it is. `TestApplyDividendIsIncomeNotCostReduction` pins this down.

Its three numbers reuse the trade fields as `Quantity` = shares the payout was made on, `Price` = amount per share, `Fee` = tax withheld, which is how a dividend is announced; `NetAmount` is `q*price - fee` like a sell's (`models.NetAmount` keys off `SideBuy`, not `SideSell`, so a new realizing side gets the right sign by default). The trade form relabels those three fields rather than growing a second near-identical form.

**A dividend is deliberately not checked against the shares on hand.** It is earned on the ex-dividend date but lands weeks later, by which time the shares may have been sold; refusing that entry would decline to record money that genuinely arrived. Nothing about a dividend can run out or go negative, so there is no invariant needing the guard a sell requires — `TestApplyDividendAfterFullExit` keeps that path open.

`TransactionSide.Realizes()` is what decides whether an entry gets a stamp; use it rather than comparing against `SideSell`, so a future realizing side is picked up everywhere at once.

**`models.DividendEvent` is what the market did; a ledger entry is what the user banked.** The two are kept strictly apart and nothing crosses between them automatically. A distribution the provider reports is stored as master data beside the daily closes — `db.SaveDividendEvents`, same composite key, same idempotent refetch — and **never posted**. Writing it into the ledger would invent an entry, which is the same thing the hindsight report refuses when it declines to model the dividends unsold shares would have received. What closes the gap is a prompt: `db.PendingDividends` reports distributions the book was entitled to with nothing recorded against them, and the Ledger page fills the trade form in from one and stops there. The announced amount is before whatever was withheld, which this system deliberately does not guess.

**It costs no extra request.** `quotes.Client.History` asks for `events=div` on the call the price sync already makes, so the distributions arrive in the same payload as the closes — which is why "automatic dividend fetching" moved from a natural next step to something that happens whenever prices are synced.

Entitlement is decided by **replaying the ledger to the ex-date**, not by shares on hand: a holding sold last month was still owed the payout from the month before, and refusing to prompt for it would lose real money. This is the same reasoning that keeps a dividend entry from being checked against the current position. An entry counts as the record of a distribution when it falls within `dividendClaimWindow` (60 days) after the ex-date, which covers the payment lag in both markets while staying inside a quarterly cycle — TSMC's ex-dates run about 86 days apart, so a wider window would let one recorded entry answer for two distributions. **Each entry can be claimed only once**, or a quarterly payer recorded a single time would look fully recorded all year, which is exactly the failure this exists to catch.
