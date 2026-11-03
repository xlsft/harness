// MockBrowserApi: a fake browser for the mock transport. Its sockets speak the real wire format (binary frames with
// a header, JSON state / action / error messages, one-time tickets), so a BrowserConnection behaves exactly as against
// a client. Pages are rendered as images: on a canvas when one is available (OffscreenCanvas in browsers), as SVG
// otherwise (tests under node). Tabs, history, viewport presets and take-over work; clicks on a page draw a marker.
// The mock transport's scenario (or a test) can play the agent with agentAct().

import { create, toJson } from '@bufbuild/protobuf'
import { BrowserActionSchema, BrowserAction_Phase, BrowserStateSchema, BrowserTabSchema, BrowserViewportSchema } from '~/gen/harness/agent/v1/browser_pb'
import { BrowserConnection, encodeFrame, type BrowserLink, type BrowserSocketHandlers } from './connection'
import { presetPb, type BrowserApi, type BrowserAvailabilityVM, type BrowserInstallHandle, type BrowserStateVM, type BrowserViewportVM, type OpenBrowserOptions, type ViewportPreset } from './types'

interface MockTab {
  id: string
  history: string[]
  index: number
  loading: boolean
  consoleErrors: number
  // Where the page was last clicked, to show that input arrived.
  marker?: { x: number, y: number }
}

interface MockSocket {
  handlers: BrowserSocketHandlers
}

const PRESETS: Record<ViewportPreset, Omit<BrowserViewportVM, 'preset'>> = {
  desktop: { width: 1280, height: 800, deviceScaleFactor: 1, mobile: false, touch: false },
  tablet: { width: 768, height: 1024, deviceScaleFactor: 2, mobile: true, touch: true },
  mobile: { width: 375, height: 812, deviceScaleFactor: 3, mobile: true, touch: true },
  custom: { width: 1280, height: 800, deviceScaleFactor: 1, mobile: false, touch: false }
}

function pageTitle(url: string) {
  if (!url || url === 'about:blank') return 'New tab'
  try {
    const u = new URL(url)
    return `${u.host}${u.pathname === '/' ? '' : u.pathname}`
  } catch {
    return url
  }
}

function escapeXml(s: string) {
  return s.replace(/[<>&"']/g, c => ({ '<': '&lt;', '>': '&gt;', '&': '&amp;', '"': '&quot;', '\'': '&apos;' }[c]!))
}

function hue(s: string) {
  let h = 0
  for (const c of s) h = (h * 31 + c.charCodeAt(0)) % 360
  return h
}

async function renderPage(tab: MockTab, vp: BrowserViewportVM, url: string): Promise<{ mime: string, data: Uint8Array }> {
  const { width, height } = vp
  const title = pageTitle(url)
  const accent = `hsl(${hue(url)}, 60%, 45%)`
  const Off = (globalThis as { OffscreenCanvas?: typeof OffscreenCanvas }).OffscreenCanvas
  if (Off) {
    const canvas = new Off(width, height)
    const ctx = canvas.getContext('2d')
    if (ctx) {
      ctx.fillStyle = '#fafafa'
      ctx.fillRect(0, 0, width, height)
      ctx.fillStyle = accent
      ctx.fillRect(0, 0, width, 56)
      ctx.fillStyle = '#fff'
      ctx.font = '600 20px system-ui, sans-serif'
      ctx.fillText(title, 20, 36, width - 40)
      ctx.fillStyle = '#333'
      ctx.font = '14px system-ui, sans-serif'
      ctx.fillText(url || 'Type a URL', 20, 96, width - 40)
      ctx.fillStyle = '#ddd'
      for (let i = 0; i < 6; i++) ctx.fillRect(20, 124 + i * 28, Math.max(80, width - 80 - i * 40), 14)
      if (tab.marker) {
        ctx.strokeStyle = accent
        ctx.lineWidth = 2
        ctx.beginPath()
        ctx.arc(tab.marker.x, tab.marker.y, 14, 0, Math.PI * 2)
        ctx.stroke()
      }
      const blob = await canvas.convertToBlob({ type: 'image/jpeg', quality: 0.8 })
      return { mime: 'image/jpeg', data: new Uint8Array(await blob.arrayBuffer()) }
    }
  }
  const lines = Array.from({ length: 6 }, (_, i) => `<rect x="20" y="${124 + i * 28}" width="${Math.max(80, width - 80 - i * 40)}" height="14" fill="#ddd"/>`).join('')
  const marker = tab.marker ? `<circle cx="${tab.marker.x}" cy="${tab.marker.y}" r="14" fill="none" stroke="${accent}" stroke-width="2"/>` : ''
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}"><rect width="100%" height="100%" fill="#fafafa"/><rect width="100%" height="56" fill="${accent}"/><text x="20" y="36" fill="#fff" font-family="sans-serif" font-size="20" font-weight="600">${escapeXml(title)}</text><text x="20" y="96" fill="#333" font-family="sans-serif" font-size="14">${escapeXml(url || 'Type a URL')}</text>${lines}${marker}</svg>`
  return { mime: 'image/svg+xml', data: new TextEncoder().encode(svg) }
}

class MockBrowser {
  sessionId: string
  running = false
  tabs: MockTab[] = []
  activeId = ''
  nextTab = 1
  seq = 0
  takenOver = false
  viewport: BrowserViewportVM = { preset: 'desktop', ...PRESETS.desktop }
  allowedSites: string[] = []
  sockets = new Set<MockSocket>()
  private latency: number

  constructor(sessionId: string, latency: number) {
    this.sessionId = sessionId
    this.latency = latency
  }

  start() {
    if (this.running) return
    this.running = true
    if (!this.tabs.length) this.addTab('')
  }

  active() {
    return this.tabs.find(t => t.id === this.activeId)
  }

  url(t: MockTab) {
    return t.history[t.index] ?? ''
  }

  addTab(url: string) {
    const tab: MockTab = { id: `t${this.nextTab++}`, history: [url], index: 0, loading: false, consoleErrors: 0 }
    this.tabs.push(tab)
    this.activeId = tab.id
    return tab
  }

  statePb() {
    return create(BrowserStateSchema, {
      sessionId: this.sessionId,
      running: this.running,
      activeTabId: this.activeId,
      allowedSites: this.allowedSites,
      takenOver: this.takenOver,
      viewport: create(BrowserViewportSchema, {
        preset: presetPb(this.viewport.preset),
        width: this.viewport.width,
        height: this.viewport.height,
        deviceScaleFactor: this.viewport.deviceScaleFactor,
        mobile: this.viewport.mobile,
        touch: this.viewport.touch
      }),
      tabs: this.tabs.map(t => create(BrowserTabSchema, {
        tabId: t.id,
        url: this.url(t),
        title: pageTitle(this.url(t)),
        loading: t.loading,
        faviconUrl: '',
        consoleErrors: t.consoleErrors,
        canGoBack: t.index > 0,
        canGoForward: t.index < t.history.length - 1,
        active: t.id === this.activeId
      }))
    })
  }

  state(): BrowserStateVM {
    return {
      sessionId: this.sessionId,
      running: this.running,
      activeTabId: this.activeId,
      allowedSites: [...this.allowedSites],
      takenOver: this.takenOver,
      viewport: { ...this.viewport },
      tabs: this.tabs.map(t => ({
        id: t.id,
        url: this.url(t),
        title: pageTitle(this.url(t)),
        loading: t.loading,
        favicon: '',
        consoleErrors: t.consoleErrors,
        canGoBack: t.index > 0,
        canGoForward: t.index < t.history.length - 1,
        active: t.id === this.activeId
      }))
    }
  }

  broadcast(msg: unknown) {
    const text = JSON.stringify(msg)
    for (const s of this.sockets) s.handlers.onMessage(text)
  }

  pushState() {
    this.broadcast({ type: 'state', state: toJson(BrowserStateSchema, this.statePb()) })
  }

  async pushFrame() {
    const t = this.active()
    if (!t || !this.sockets.size) return
    const { mime, data } = await renderPage(t, this.viewport, this.url(t))
    const frame = encodeFrame({ tabId: t.id, seq: ++this.seq, width: this.viewport.width, height: this.viewport.height, scale: 1, scrollX: 0, scrollY: 0, timestamp: Date.now() / 1000, mime }, data)
    for (const s of this.sockets) s.handlers.onMessage(frame)
  }

  changed() {
    this.pushState()
    void this.pushFrame()
  }

  // A navigation of the active tab: loading briefly, then the new page.
  go(t: MockTab, mutate: () => void) {
    mutate()
    t.loading = true
    t.marker = undefined
    this.changed()
    setTimeout(() => {
      t.loading = false
      if (this.running) this.changed()
    }, this.latency)
  }

  command(msg: Record<string, unknown>, reply: (m: unknown) => void) {
    const t = this.active()
    switch (msg.type) {
      case 'navigate': {
        const url = normalize(String(msg.url ?? ''))
        if (!url) return reply({ type: 'error', message: `invalid url ${String(msg.url)}` })
        if (t) {
          this.go(t, () => {
            t.history = [...t.history.slice(0, t.index + 1), url]
            t.index = t.history.length - 1
          })
        }
        break
      }
      case 'back':
        if (t && t.index > 0) this.go(t, () => void t.index--)
        break
      case 'forward':
        if (t && t.index < t.history.length - 1) this.go(t, () => void t.index++)
        break
      case 'reload':
        if (t) this.go(t, () => {})
        break
      case 'stop':
        if (t) {
          t.loading = false
          this.changed()
        }
        break
      case 'newTab':
        this.addTab(msg.url ? normalize(String(msg.url)) : '')
        this.changed()
        break
      case 'closeTab': {
        const id = String(msg.tab ?? this.activeId)
        const i = this.tabs.findIndex(x => x.id === id)
        if (i < 0) return reply({ type: 'error', message: `no tab ${id}` })
        this.tabs.splice(i, 1)
        if (!this.tabs.length) this.addTab('')
        else if (this.activeId === id) this.activeId = this.tabs[this.tabs.length - 1]!.id
        this.changed()
        break
      }
      case 'switchTab': {
        const id = String(msg.tab)
        if (!this.tabs.some(x => x.id === id)) return reply({ type: 'error', message: `no tab ${id}` })
        this.activeId = id
        this.changed()
        break
      }
      case 'viewport': {
        const preset = String(msg.preset || 'desktop') as ViewportPreset
        if (!(preset in PRESETS)) return reply({ type: 'error', message: `unknown viewport preset ${preset}` })
        const base = PRESETS[preset]
        const custom = preset === 'custom' && Number(msg.width) > 0 && Number(msg.height) > 0
        this.viewport = { preset, ...base, ...(custom ? { width: Number(msg.width), height: Number(msg.height) } : {}) }
        this.changed()
        break
      }
      case 'mouse':
        if (t && msg.event === 'down') {
          t.marker = { x: Number(msg.x) || 0, y: Number(msg.y) || 0 }
          void this.pushFrame()
        }
        break
      case 'takeover':
        this.takenOver = !!msg.on
        this.pushState()
        break
    }
  }
}

function normalize(raw: string) {
  const s = raw.trim()
  if (!s) return ''
  if (s === 'about:blank' || /^https?:\/\//i.test(s) || /^file:\/\//i.test(s)) return s
  if (/^[a-z][a-z0-9+.-]*:(?!\d)/i.test(s)) return ''
  return (/^(localhost|127\.|\[::1\])/i.test(s) ? 'http://' : 'https://') + s
}

export class MockBrowserApi implements BrowserApi {
  private browsers = new Map<string, MockBrowser>()
  private tickets = new Map<string, MockBrowser>()
  private available = true
  private downloaded = false
  private latency: number

  // latencyMs: how long a fake page load shows as loading.
  constructor(opts: { latencyMs?: number, available?: boolean } = {}) {
    this.latency = opts.latencyMs ?? 200
    this.available = opts.available ?? true
  }

  private browser(id: string) {
    let b = this.browsers.get(id)
    if (!b) {
      b = new MockBrowser(id, this.latency)
      this.browsers.set(id, b)
    }
    return b
  }

  private availabilityNow(): BrowserAvailabilityVM {
    return {
      available: this.available,
      executable: this.available ? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' : '',
      product: this.available ? (this.downloaded ? 'Chrome for Testing' : 'Google Chrome') : '',
      source: this.available ? (this.downloaded ? 'downloaded' : 'system') : 'none',
      downloadable: true,
      downloadVersion: '156.0.8078.80',
      downloaded: this.downloaded,
      downloading: false
    }
  }

  async availability() {
    return this.availabilityNow()
  }

  install(onProgress?: (downloaded: number, total: number) => void): BrowserInstallHandle {
    const total = 200 * 1024 * 1024
    let timer: ReturnType<typeof setInterval> | undefined
    let cancelled = false
    const done = new Promise<BrowserAvailabilityVM | undefined>((resolve) => {
      let n = 0
      timer = setInterval(() => {
        n = Math.min(total, n + total / 5)
        onProgress?.(n, total)
        if (n >= total) {
          clearInterval(timer)
          this.available = true
          this.downloaded = true
          resolve(cancelled ? undefined : this.availabilityNow())
        }
      }, 20)
    })
    return {
      done,
      cancel: () => {
        cancelled = true
        clearInterval(timer)
      }
    }
  }

  open(sessionId: string, opts: OpenBrowserOptions = {}) {
    const link: BrowserLink = {
      open: async (id, first) => {
        if (!this.available) throw new Error('no Chromium-based browser was found')
        const b = this.browser(id)
        b.start()
        if (first && opts.viewport) {
          const base = PRESETS[opts.viewport.preset]
          b.viewport = { preset: opts.viewport.preset, ...base, ...(opts.viewport.preset === 'custom' && opts.viewport.width && opts.viewport.height ? { width: opts.viewport.width, height: opts.viewport.height } : {}) }
        }
        if (first && opts.url) b.addTab(normalize(opts.url))
        const ticket = Math.random().toString(36).slice(2)
        this.tickets.set(ticket, b)
        return { state: b.state(), ticket }
      },
      socket: (ticket, handlers) => {
        const b = this.tickets.get(ticket)
        this.tickets.delete(ticket)
        if (!b) {
          queueMicrotask(() => handlers.onClose(1008, 'invalid ticket'))
          return { send: () => {}, close: () => {} }
        }
        const sock: MockSocket = { handlers }
        queueMicrotask(() => {
          b.sockets.add(sock)
          handlers.onOpen()
          b.changed()
        })
        return {
          send: (data) => {
            let msg: Record<string, unknown>
            try {
              msg = JSON.parse(data)
            } catch {
              return
            }
            b.command(msg, m => handlers.onMessage(JSON.stringify(m)))
          },
          close: () => void b.sockets.delete(sock)
        }
      }
    }
    const conn = new BrowserConnection(link, sessionId, opts.connection)
    void conn.connect()
    return conn
  }

  async listTabs(sessionId: string) {
    return this.browser(sessionId).state()
  }

  async close(sessionId: string) {
    const b = this.browsers.get(sessionId)
    if (!b) return
    b.running = false
    b.tabs = []
    b.activeId = ''
    const closed = JSON.stringify({ type: 'closed', reason: 'closed' })
    for (const s of [...b.sockets]) s.handlers.onMessage(closed)
    b.sockets.clear()
  }

  async clearData(_sessionId: string) {}

  /** Plays the agent: reports an action over the socket, as the real client does for its tool calls. */
  agentAct(sessionId: string, action: { toolUseId: string, kind: string, label?: string, ref?: string, x?: number, y?: number, finished?: boolean, error?: string }) {
    const b = this.browser(sessionId)
    const pb = create(BrowserActionSchema, {
      toolUseId: action.toolUseId,
      phase: action.finished ? BrowserAction_Phase.FINISHED : BrowserAction_Phase.STARTED,
      kind: action.kind,
      tabId: b.activeId,
      ref: action.ref ?? '',
      x: action.x,
      y: action.y,
      label: action.label ?? '',
      error: action.error ?? '',
      url: b.active() ? b.url(b.active()!) : ''
    })
    b.broadcast({ type: 'action', action: toJson(BrowserActionSchema, pb) })
  }
}
