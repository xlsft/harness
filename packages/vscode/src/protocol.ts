// harness.ide.v1.EditorBridgeService in its JSON form (proto/harness/ide/v1/bridge.proto). Written by hand: the package
// has no runtime dependencies, and test/unit/bridge.test.ts checks these shapes against the golden files the Go client
// checks too (packages/client/ide/testdata/bridge). Absent fields are proto3 defaults.

export interface PingResponse {
  name: string
  version: string
  workspaceFolders: string[]
}

export interface ShowDiffRequest {
  diffId?: string
  sessionId?: string
  toolUseId?: string
  path?: string
  exists?: boolean
  before?: string
  after?: string
  toolName?: string
}

export type DiffOutcome = 'DIFF_OUTCOME_ACCEPTED' | 'DIFF_OUTCOME_REJECTED' | 'DIFF_OUTCOME_DISMISSED'

export interface ShowDiffResponse {
  outcome: DiffOutcome
  comment?: string
}

export type DiagnosticSeverity
  = 'DIAGNOSTIC_SEVERITY_ERROR' | 'DIAGNOSTIC_SEVERITY_WARNING' | 'DIAGNOSTIC_SEVERITY_INFORMATION' | 'DIAGNOSTIC_SEVERITY_HINT'

export interface Diagnostic {
  path: string
  line: number
  column: number
  endLine: number
  endColumn: number
  severity: DiagnosticSeverity
  message: string
  source?: string
  code?: string
}

export interface GetDiagnosticsRequest {
  paths?: string[]
}

export interface GetDiagnosticsResponse {
  diagnostics: Diagnostic[]
}

export interface OpenFileRequest {
  path?: string
  line?: number
  endLine?: number
}

export interface ReadBufferRequest {
  path?: string
}

export interface ReadBufferResponse {
  open: boolean
  content?: string
  dirty?: boolean
}

export interface EditAppliedRequest {
  sessionId?: string
  toolUseId?: string
  path?: string
}

/** What the extension does for each method. The signal aborts when the client hangs up. */
export interface BridgeHandlers {
  ping(): PingResponse | Promise<PingResponse>
  showDiff(req: ShowDiffRequest, signal: AbortSignal): Promise<ShowDiffResponse>
  getDiagnostics(req: GetDiagnosticsRequest): GetDiagnosticsResponse | Promise<GetDiagnosticsResponse>
  openFile(req: OpenFileRequest): void | Promise<void>
  readBuffer(req: ReadBufferRequest): ReadBufferResponse | Promise<ReadBufferResponse>
  editApplied(req: EditAppliedRequest): void | Promise<void>
}

/** An answer with a Connect error code. */
export class BridgeError extends Error {
  readonly code: string
  constructor(code: string, message: string) {
    super(message)
    this.code = code
  }
}
