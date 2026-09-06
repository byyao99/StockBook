/**
 * Whether an automatic fetch is worth making again yet.
 *
 * The pages that refresh quotes and sync research do it on arrival, so tabbing
 * between views would otherwise re-ask every time — spending a round trip to be
 * told "all current", and on the research endpoint running into its own 2/min
 * limit, which would then suppress a sync that had real work to do.
 *
 * This is **not** a second copy of the server's freshness rules. Those are per
 * instrument and decide what to actually fetch; this only decides whether to
 * open the conversation at all, and is deliberately far shorter than any server
 * window so it can never suppress a fetch that would have done something.
 *
 * The state lives here, in a module, on purpose. A `let` inside `<script setup>`
 * looks like module scope and is not: that block compiles into the component's
 * setup function, so the counter would reset on every mount and the guard would
 * never hold — which is exactly how it was first written, and it did nothing.
 */
const lastRun: Record<string, number> = {}

/**
 * Report whether `key` may run again, claiming the slot if so.
 *
 * Reading and writing together is what keeps two mounts in the same tick from
 * both deciding to go. `now` is injectable so the rule can be tested against
 * fixed times rather than the wall clock.
 */
export function claimAutoRun(key: string, intervalMs: number, now = Date.now()): boolean {
  // Never having run is its own case, not a run at time zero. Reading a missing
  // entry as 0 happens to work against a wall clock, whose epoch is far past
  // any interval, and refuses the very first run against any other — which is
  // both a real rule ("nothing has happened yet, so go") and the one a test can
  // state without pinning the year.
  const previous = lastRun[key]
  if (previous !== undefined && now - previous < intervalMs) return false
  lastRun[key] = now
  return true
}

/** Forget every claim. For tests, which must not leak state into each other. */
export function resetAutoRuns(): void {
  for (const key of Object.keys(lastRun)) delete lastRun[key]
}
