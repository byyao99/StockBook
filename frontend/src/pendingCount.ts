// How many prompts are waiting on the Ledger page: dividends the book was paid
// with no entry against them, and savings-plan instalments with nothing
// recorded. Both are only ever shown on that page, so without a count in the
// nav a reader who lands on Holdings — which is where logging in goes — has no
// way to know something is owed until they happen to open the Ledger.
//
// Both endpoints read the database only; neither calls a provider, so asking on
// every navigation is cheap and is not the kind of background fetch the system
// avoids.
import { ref } from 'vue'
import { planApi, reportApi } from './api/client'

/** Prompts waiting, or null when not known (signed out, or the read failed). */
export const pendingCount = ref<number | null>(null)

// Each read takes a ticket, and only the newest one may write. Two navigations
// in quick succession, or the Ledger reporting its own count while the nav's
// read is still in flight, must not let an older answer land last.
let ticket = 0

/** Records a count the caller already has — the Ledger page loads both lists anyway. */
export function setPendingCount(count: number) {
  ticket++
  pendingCount.value = count
}

export function clearPendingCount() {
  ticket++
  pendingCount.value = null
}

export async function refreshPendingCount(): Promise<void> {
  const mine = ++ticket
  try {
    const [dividends, purchases] = await Promise.all([
      reportApi.pendingDividends(),
      planApi.pending(),
    ])
    if (mine === ticket) pendingCount.value = dividends.length + purchases.length
  } catch {
    // A badge that failed to load says nothing rather than "0": nothing owed
    // and not knowing are different answers. The Ledger page reports the
    // error itself when the reader goes there.
    if (mine === ticket) pendingCount.value = null
  }
}
