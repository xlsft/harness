// Formatting helpers shared by the transcript, panes and composer.

// 41s · 1m 04s · 17m 23s · 1h 4m
export function formatDuration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, '0')}s`
  return `${Math.floor(m / 60)}h ${m % 60}m`
}

// 0m 48s (the transcript's running line always shows minutes)
export function formatClock(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000))
  return `${Math.floor(s / 60)}m ${s % 60}s`
}

// Tool timings: 0.4s · 3.8s · 640ms
export function formatSeconds(ms: number): string {
  if (ms < 1000) return ms % 100 === 0 && ms > 0 ? `${ms / 1000}s` : `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  return formatDuration(ms)
}

// 8.4k · 62k · 1.2M
export function formatTokens(n: number): string {
  if (n < 1000) return String(n)
  if (n < 10_000) return `${(n / 1000).toFixed(1).replace(/\.0$/, '')}k`
  if (n < 1_000_000) return `${Math.round(n / 1000)}k`
  return `${(n / 1_000_000).toFixed(1).replace(/\.0$/, '')}M`
}

export function formatCost(usd: number): string {
  return `$${usd.toFixed(2)}`
}

export function formatCount(n: number): string {
  return n.toLocaleString('en-US')
}

// Short relative age for lists: 5m, 2h, Oct 29.
export function formatAge(ms: number): string {
  const m = Math.round(ms / 60_000)
  if (m < 1) return 'now'
  if (m < 60) return `${m}m`
  const h = Math.round(m / 60)
  if (h < 24) return `${h}h`
  return new Date(Date.now() - ms).toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
}

// Relative time in words for message rows: just now, 1 minute ago, 3 hours ago, yesterday, 4 days ago, Oct 29.
export function formatAgo(ms: number): string {
  const m = Math.floor(Math.max(0, ms) / 60_000)
  if (m < 1) return 'just now'
  if (m < 60) return m === 1 ? '1 minute ago' : `${m} minutes ago`
  const h = Math.floor(m / 60)
  if (h < 24) return h === 1 ? '1 hour ago' : `${h} hours ago`
  const d = Math.floor(h / 24)
  if (d === 1) return 'yesterday'
  if (d < 7) return `${d} days ago`
  return new Date(Date.now() - ms).toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
}

export function basename(path: string): string {
  const p = path.replace(/\/+$/, '')
  return p.slice(p.lastIndexOf('/') + 1) || path
}
