# The six invariants, moving-average cost, and unknown-is-not-zero

_Read before touching the ledger write path, position math, or anything that reports a valuation._

## The six invariants

These are the things most easily broken by a well-meaning change. Each has tests pinning it down.

**1. `Position.CostBasis` is the total remaining cost, never a per-share average.** Storing an average would accumulate rounding error on every single trade: 3 shares at $10.00 plus 1 at $10.01 average 1000.25 cents, which is not an integer. Storing the total keeps `CostBasis` as exact as the inputs allow, because it only ever gets added to and subtracted from.

Fractional shares put **one** rounding step in front of it, and only one: 2.79 shares at $10.01 is $27.9279, which is not a whole cent and never will be. `models.Gross` is where that rounding happens, once, and what lands in `CostBasis` afterwards is the exact integer result. Rounding anywhere else, or twice, is what makes a basis drift. A full exit still leaves exactly 0 — `cost × q / q` is `cost` at any scale — so `TestApplyFullExitLeavesNoResidue` holds unchanged. The average is derived for display at the UI edge (`averageCost` in `positionMath.ts`) and never persisted or returned by the API. Do not add an `average_cost` column or response field.

**2. Replaying the ledger is the definition of a position; the `positions` table is a cache of it.** `db.ReplayPosition` folds a user's transactions through `models.PositionState.Apply` in `ledgerOrder` and is the authoritative answer. `syncPosition` may take an incremental shortcut — applying just the new entry to the current row — but **only** when that entry sorts last in the ledger. An edit, a delete, or a back-dated entry landing mid-history always triggers a full replay, because moving-average cost is order-dependent and a single incremental step would compute the basis against the wrong prefix. `TestMaterializedPositionMatchesReplay` checks both paths agree across a table of sequences; any new write path must be added to it.

**3. The ledger's sort key is the triple `(traded_at, created_at, id)`.** Chronology alone is not a total order — several trades on the same day would fold in an arbitrary order and the average cost would drift between replays. `created_at` breaks same-day ties and `id` breaks same-tick ties. `isLastInLedger` compares the full triple, which is why an entry created in the same clock tick as another correctly falls back to a replay.

**4. Position writes are compare-and-swap, never read-then-write.** `writePosition` conditions its UPDATE on the `(quantity, cost_basis, realized_pl)` triple read a moment earlier — that triple is the row's version. Zero rows matched means somebody else moved it, which surfaces as `db.ErrConflict` → 409 rather than a silent overwrite. A plain conditional UPDATE (the `SET stock = stock - ? WHERE stock >= ?` trick an inventory system can use) is **not** available here: a sell releases cost in proportion to the shares leaving, so the new value depends on the old one in a way SQL arithmetic cannot express. For a holding that does not exist yet, the unique index on `(user_id, instrument_id)` turns a concurrent double-insert into the same conflict. `internal/db/cas_test.go` drives the guard directly, because the handler-level concurrency tests would otherwise pass on SQLite's single-writer serialization alone and prove nothing.

**5. Server-authoritative amounts, snapshotted symbols.** `Transaction.NetAmount` is always computed by `models.NetAmount` from quantity, price and side; a client-supplied value is ignored. `Transaction.Symbol` is snapshotted from the instrument at entry time, so renaming an instrument never rewrites history. `db.CreateTransaction` resolves the instrument inside its own transaction so it cannot be deleted between validation and the write.

**6. A realizing entry's `RealizedPL` stamp is a decomposition of the position's total, and must always add back up to it.** `Position.RealizedPL` is a running total, which cannot answer "how did last year go?" — so each sell and each dividend also stores the step it contributed. The stamp is never recomputed from quantity and price: `foldLedger` derives it by subtraction across a single `Apply` (`realizedStep`), which keeps `models.PositionState.Apply` the only implementation of the moving-average algebra. Both write paths stamp — the incremental one writes the appended entry, the replay path restamps **the whole ledger**, because changing one entry changes the average every later sell was measured against. `TestStampsSumToPositionRealized` checks the sum against the materialized position across the same table of sequences as invariant 2, and any new write path must keep that true. A buy's stamp is nil (it realizes nothing, which is not zero); a sell's or dividend's is nil only when `db.backfillRealizedPL` could not replay its holding, which `RealizedReport` counts as `UnstampedEntries` and excludes from the totals rather than folding in as zero.

## Moving-average cost

The whole algebra lives in `models.PositionState.Apply` (`internal/models/position_state.go`), which is pure and has no database dependency:

```
BUY(q, price, fee):   qty += q;  cost += q*price + fee
SELL(q, price, fee):  requires qty >= q
                      costRemoved = round(cost * q / qty)   // half-up, integer only
                      realized   += (q*price - fee) - costRemoved
                      cost       -= costRemoved
                      qty        -= q
```

Buy fees join the cost basis; sell fees come off the proceeds and so reduce the realized gain. A full exit needs no rounding at all (`cost*q/q == cost`), so `CostBasis` lands on exactly 0 with no residue — `TestApplyFullExitLeavesNoResidue` is the most sensitive check that the proportional math is right, and it is worth keeping green above almost anything else here.

## Unknown is not zero

An instrument's `LastPrice` is a `*int64` maintained by hand; nil means no quote has been set. Everything downstream reports that honestly rather than substituting zero: `PositionView.MarketValue` and `UnrealizedPL` are nil, `positionMath.ts` returns `null`, and the UI renders an em dash. `PortfolioSummary` goes further — its valuation totals cover only the priced holdings, so it reports `PricedCostBasis` alongside them (making `TotalUnrealizedPL == TotalMarketValue - PricedCostBasis` an identity that always holds) and `PricedPositions` vs `OpenPositions` so the UI can say how much of the book the totals actually account for. Showing an unvalued holding as $0.00 would read as a total loss; do not "simplify" these to zero.
