// BrowserApi: the session's real browser (a headless Chromium on the client's machine, shared by the Browser pane
// and the agent's Browser* tools; proto/harness/agent/v1/browser.proto). The RPCs manage it; the live view goes
// over a WebSocket (BrowserConnection, ./connection.ts): JPEG screencast frames and state updates come in, and
// navigation, tab commands, viewport changes and mouse / keyboard input go out. Two implementations: connect.ts
// (a real client) and mock.ts (canvas-rendered fake pages, for the mock transport).

import type { BrowserAction, BrowserAvailability, BrowserState, BrowserTab, BrowserViewport } from '~/gen/harness/agent/v1/browser_pb'
import { BrowserAction_Phase, BrowserAvailability_Source, BrowserViewport_Preset } from '~/gen/harness/agent/v1/browser_pb'
import type { PreviewServer } from '~/gen/harness/agent/v1/preview_pb'
import { PreviewServer_Status } from '~/gen/harness/agent/v1/preview_pb'
import type { BrowserConnection, BrowserConnectionOptions } from './connection'

export type ViewportPreset = 'desktop' | 'tablet' | 'mobile' | 'custom'

export interface BrowserViewportVM {
  preset: ViewportPreset
  // CSS px.
  width: number
  height: number
  deviceScaleFactor: number
  mobile: boolean
  touch: boolean
}

export interface BrowserTabVM {
  // "t1", "t2", ...
  id: string
  url: string
  title: string
  loading: boolean
  // Absolute URL of the page's icon; empty when unknown.
  favicon: string
  // Console errors and uncaught exceptions since the page loaded.
  consoleErrors: number
  canGoBack: boolean
  canGoForward: boolean
  active: boolean
}

export interface BrowserStateVM {
  sessionId: string
  // Whether the browser process runs (it starts again on open or on the agent's next tool call).
  running: boolean
  tabs: BrowserTabVM[]
  activeTabId: string
  viewport: BrowserViewportVM
  // Hosts the agent may use in this session without asking again (approved this session).
  allowedSites: string[]
  // The user took the browser over: the agent's action tools fail meanwhile.
  takenOver: boolean
}

// What the agent does in the browser: one 'started' and one 'finished' per Browser* tool call (the pane's overlay,
// "step 3 · click · Add provider"). Coordinates and the box are page CSS px.
export interface BrowserActionVM {
  toolUseId: string
  phase: 'started' | 'finished'
  // navigate, back, forward, reload, click, double_click, type, key, scroll, snapshot, screenshot, eval, tabs,
  // console, network, wait.
  kind: string
  tabId: string
  ref: string
  x?: number
  y?: number
  rect?: { x: number, y: number, width: number, height: number }
  label: string
  // 'finished' only: empty on success.
  error: string
  url: string
}

// One screencast frame of the active tab. width/height are the page viewport in CSS px; the image is `scale`
// image px per CSS px (draw it at width × height).
export interface BrowserFrame {
  tabId: string
  seq: number
  width: number
  height: number
  scale: number
  scrollX: number
  scrollY: number
  // Seconds (the browser's clock).
  timestamp: number
  mime: string
  data: Uint8Array<ArrayBuffer>
}

export interface BrowserAvailabilityVM {
  available: boolean
  executable: string
  // "Google Chrome", "Chromium", "Microsoft Edge", "Brave", "Chrome for Testing".
  product: string
  source: 'none' | 'env' | 'system' | 'downloaded'
  // Whether install() can download Chrome for Testing for this platform, which version, and whether it is there.
  downloadable: boolean
  downloadVersion: string
  downloaded: boolean
  downloading: boolean
}

export interface OpenBrowserOptions {
  // Opens this URL in a new tab.
  url?: string
  viewport?: { preset: ViewportPreset, width?: number, height?: number }
  connection?: BrowserConnectionOptions
}

export type BrowserMouseButton = 'left' | 'middle' | 'right' | 'none'

// Mouse input in page CSS px of the active tab (convert from the frame's drawn rectangle).
export interface BrowserMouseInput {
  event: 'move' | 'down' | 'up' | 'wheel'
  x: number
  y: number
  button?: BrowserMouseButton
  clickCount?: number
  deltaX?: number
  deltaY?: number
  // modifierMask(event).
  modifiers?: number
}

export interface BrowserKeyInput {
  event: 'down' | 'up' | 'char'
  // KeyboardEvent.key / .code; text is what a printable key types.
  key: string
  code?: string
  text?: string
  keyCode?: number
  modifiers?: number
}

// A Chrome for Testing download being watched: done resolves with the availability afterwards; cancel stops watching
// (the client keeps downloading).
export interface BrowserInstallHandle {
  done: Promise<BrowserAvailabilityVM | undefined>
  cancel: () => void
}

// A dev server the client runs for the session (the agent's Preview* tools; preview.proto).
export interface PreviewServerVM {
  id: string
  name: string
  commandLine: string
  port: number
  url: string
  status: 'starting' | 'running' | 'exited' | 'stopped'
  exitCode: number
}

export interface BrowserApi {
  // Which browser would run, or that none is found (then offer install()).
  availability(): Promise<BrowserAvailabilityVM>
  // Downloads the pinned Chrome for Testing build into the client's HARNESS_HOME, with progress.
  install(onProgress?: (downloaded: number, total: number) => void): BrowserInstallHandle
  // Starts the session's browser if needed and connects the live view (a harness session of the client's machine).
  open(sessionId: string, opts?: OpenBrowserOptions): BrowserConnection
  // The state without connecting (running false when the browser is stopped).
  listTabs(sessionId: string): Promise<BrowserStateVM>
  // Stops the browser (its profile stays; the tabs come back on the next open).
  close(sessionId: string): Promise<void>
  // Deletes cookies, cache and site storage of the session's browser profile.
  clearData(sessionId: string): Promise<void>
  // The session's dev servers, oldest first, including ended ones.
  previewServers(sessionId: string): Promise<PreviewServerVM[]>
  stopPreview(sessionId: string, serverId: string): Promise<void>
  // A server's recent output, line by line.
  previewLogs(sessionId: string, serverId: string, opts?: { errorsOnly?: boolean }): Promise<string[]>
}

// ── conversions ──────────────────────────────────────────────────────────────

const PREVIEW_STATUS: Record<PreviewServer_Status, PreviewServerVM['status']> = {
  [PreviewServer_Status.UNSPECIFIED]: 'stopped',
  [PreviewServer_Status.STARTING]: 'starting',
  [PreviewServer_Status.RUNNING]: 'running',
  [PreviewServer_Status.EXITED]: 'exited',
  [PreviewServer_Status.STOPPED]: 'stopped'
}

export function previewServerView(s: PreviewServer): PreviewServerVM {
  return { id: s.serverId, name: s.name, commandLine: s.commandLine, port: s.port, url: s.url, status: PREVIEW_STATUS[s.status], exitCode: s.exitCode }
}

const PRESET: Record<BrowserViewport_Preset, ViewportPreset> = {
  [BrowserViewport_Preset.UNSPECIFIED]: 'desktop',
  [BrowserViewport_Preset.DESKTOP]: 'desktop',
  [BrowserViewport_Preset.TABLET]: 'tablet',
  [BrowserViewport_Preset.MOBILE]: 'mobile',
  [BrowserViewport_Preset.CUSTOM]: 'custom'
}

export function presetPb(p: ViewportPreset): BrowserViewport_Preset {
  switch (p) {
    case 'tablet': return BrowserViewport_Preset.TABLET
    case 'mobile': return BrowserViewport_Preset.MOBILE
    case 'custom': return BrowserViewport_Preset.CUSTOM
    default: return BrowserViewport_Preset.DESKTOP
  }
}

export function viewportView(v: BrowserViewport | undefined): BrowserViewportVM {
  return {
    preset: v ? PRESET[v.preset] ?? 'desktop' : 'desktop',
    width: v?.width ?? 1280,
    height: v?.height ?? 800,
    deviceScaleFactor: v?.deviceScaleFactor || 1,
    mobile: v?.mobile ?? false,
    touch: v?.touch ?? false
  }
}

export function tabView(t: BrowserTab): BrowserTabVM {
  return {
    id: t.tabId,
    url: t.url,
    title: t.title,
    loading: t.loading,
    favicon: t.faviconUrl,
    consoleErrors: t.consoleErrors,
    canGoBack: t.canGoBack,
    canGoForward: t.canGoForward,
    active: t.active
  }
}

export function stateView(s: BrowserState | undefined): BrowserStateVM {
  return {
    sessionId: s?.sessionId ?? '',
    running: s?.running ?? false,
    tabs: (s?.tabs ?? []).map(tabView),
    activeTabId: s?.activeTabId ?? '',
    viewport: viewportView(s?.viewport),
    allowedSites: [...(s?.allowedSites ?? [])],
    takenOver: s?.takenOver ?? false
  }
}

export function actionView(a: BrowserAction): BrowserActionVM {
  const r = a.rect
  return {
    toolUseId: a.toolUseId,
    phase: a.phase === BrowserAction_Phase.FINISHED ? 'finished' : 'started',
    kind: a.kind,
    tabId: a.tabId,
    ref: a.ref,
    x: a.x,
    y: a.y,
    rect: r.length === 4 ? { x: r[0]!, y: r[1]!, width: r[2]!, height: r[3]! } : undefined,
    label: a.label,
    error: a.error,
    url: a.url
  }
}

const SOURCE: Record<BrowserAvailability_Source, BrowserAvailabilityVM['source']> = {
  [BrowserAvailability_Source.UNSPECIFIED]: 'none',
  [BrowserAvailability_Source.ENV]: 'env',
  [BrowserAvailability_Source.SYSTEM]: 'system',
  [BrowserAvailability_Source.DOWNLOADED]: 'downloaded'
}

export function availabilityView(a: BrowserAvailability | undefined): BrowserAvailabilityVM {
  return {
    available: a?.available ?? false,
    executable: a?.executable ?? '',
    product: a?.product ?? '',
    source: a ? SOURCE[a.source] ?? 'none' : 'none',
    downloadable: a?.downloadable ?? false,
    downloadVersion: a?.downloadVersion ?? '',
    downloaded: a?.downloaded ?? false,
    downloading: a?.downloading ?? false
  }
}

// Modifier bit mask of Input.dispatch* (1 Alt, 2 Ctrl, 4 Meta, 8 Shift) from a DOM event.
export function modifierMask(e: { altKey?: boolean, ctrlKey?: boolean, metaKey?: boolean, shiftKey?: boolean }) {
  return (e.altKey ? 1 : 0) | (e.ctrlKey ? 2 : 0) | (e.metaKey ? 4 : 0) | (e.shiftKey ? 8 : 0)
}

// The agent action label of the overlay: "click · Add provider".
export function actionLabel(a: Pick<BrowserActionVM, 'kind' | 'label'>) {
  const kind = a.kind.replace(/_/g, ' ')
  return a.label ? `${kind} · ${a.label}` : kind
}
