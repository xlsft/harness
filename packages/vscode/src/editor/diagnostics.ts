// The problems panel in the bridge's terms.
import * as vscode from 'vscode'
import type { Diagnostic, DiagnosticSeverity, GetDiagnosticsRequest, GetDiagnosticsResponse } from '../protocol.ts'

const SEVERITY: Record<vscode.DiagnosticSeverity, DiagnosticSeverity> = {
  [vscode.DiagnosticSeverity.Error]: 'DIAGNOSTIC_SEVERITY_ERROR',
  [vscode.DiagnosticSeverity.Warning]: 'DIAGNOSTIC_SEVERITY_WARNING',
  [vscode.DiagnosticSeverity.Information]: 'DIAGNOSTIC_SEVERITY_INFORMATION',
  [vscode.DiagnosticSeverity.Hint]: 'DIAGNOSTIC_SEVERITY_HINT'
}

function codeOf(code: vscode.Diagnostic['code']): string {
  if (code === undefined) return ''
  return typeof code === 'object' ? String(code.value) : String(code)
}

function toBridge(uri: vscode.Uri, d: vscode.Diagnostic): Diagnostic {
  return {
    path: uri.fsPath,
    line: d.range.start.line + 1,
    column: d.range.start.character + 1,
    endLine: d.range.end.line + 1,
    endColumn: d.range.end.character + 1,
    severity: SEVERITY[d.severity],
    message: d.message,
    source: d.source ?? '',
    code: codeOf(d.code)
  }
}

export function getDiagnostics(req: GetDiagnosticsRequest): GetDiagnosticsResponse {
  const wanted = req.paths?.length ? new Set(req.paths) : undefined
  const diagnostics: Diagnostic[] = []
  for (const [uri, found] of vscode.languages.getDiagnostics()) {
    if (uri.scheme !== 'file' || (wanted && !wanted.has(uri.fsPath))) continue
    for (const d of found) diagnostics.push(toBridge(uri, d))
  }
  return { diagnostics }
}
