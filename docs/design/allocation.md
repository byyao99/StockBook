# Allocation

_Read before changing `allocation.ts` or the allocation block on `/positions`._

## Allocation

Every other figure in this system is about profit. `frontend/src/allocation.ts` is about **risk**, and it answers the one question a ledger can answer exactly while a broker statement usually will not: how much of this is in one stock.

It is pure frontend arithmetic over the positions already on the page, unit-tested like the other math modules, because nothing about it needs the database — the weights are market values over a total, and both are already on `PositionView`. `Instrument.AssetType` is carried through onto that view so a book can be sliced by **what it holds** as well as by which venue it sits on; an ETF-heavy book and a stock-picking one are different books on the same market, and an instrument the provider never classified gets its own bucket rather than being folded into either.

Two rules, and they are the ones the rest of the system already follows. The denominator is **one currency's priced market value**, so there is no cross-currency weight — that number does not exist here. And an **unpriced holding is excluded rather than drawn as 0%**, because its value is unknown and it may be the largest position in the book; the count of what was left out comes back on the `Allocation` so the block can say what share of itself the picture accounts for. A concentration understated is the wrong way for this figure to be wrong.

The view fetches **every** open holding rather than allocating over the current page (`loadAllOpen` pages to the end), since a concentrated position sitting on page two would otherwise simply not appear — which is the one case the whole feature exists for. It renders as a single stacked bar rather than a pie: a length is read accurately and an angle is not, and comparing one wedge against another is the entire point.
