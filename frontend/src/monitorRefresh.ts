import type { QueryClient } from '@tanstack/react-query'
import type { BoardQuoteMeta } from './api/types'

// One refresh cycle drives the whole monitor. Invalidate cached rankings for
// both board types, not just the currently observed one; React Query publishes
// new data without remounting the table or resetting its selection/filter/page.
export function refreshMonitorQueries(client: QueryClient, manual = false) {
  const options = { cancelRefetch: false }
  return Promise.all([
    client.invalidateQueries({ queryKey: ['latest'], refetchType: 'all' }, options),
    client.invalidateQueries({
      predicate: (query) => {
        const family = query.queryKey[0]
        if (family === 'system-status' || family === 'intraday' || family === 'board-quotes') return true
        // A complete previous close is immutable. Only retry a missing/failed
        // archive on a timer; an explicit refresh can reload the baseline too.
        return family === 'monitor-previous-close' && (manual || query.state.status === 'error'
          || !Array.isArray(query.state.data) || query.state.data.length === 0)
      },
    }, options),
  ])
}

// A quote request starts asynchronous work on the server. Finish that request
// even with auto-refresh off, rather than leaving the first `warming` response
// on screen for another 30 seconds–5 minutes. Failures stop the short polling.
export function quoteRefreshInterval(meta?: BoardQuoteMeta, requestFailed = false): number | false {
  // A transport/HTTP failure retains the old `warming` data in React Query.
  // Do not turn that stale flag into an endless one-second retry loop.
  if (requestFailed) return false
  if (meta?.quote_refreshing !== undefined) return meta.quote_refreshing ? 1000 : false
  // Allow the web bundle to work while an older API instance is being replaced.
  return !meta?.quote_error && (meta?.quote_status === 'warming' || meta?.quote_status === 'stale') ? 1000 : false
}
