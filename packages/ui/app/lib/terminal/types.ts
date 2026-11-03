// TerminalsApi: shells in a PTY on the client's machine for the terminal pane (harness.agent.v1 terminal RPCs,
// proto/harness/agent/v1/terminal.proto). The RPCs manage terminals; the interactive I/O goes over a WebSocket
// (TerminalConnection, ./connection.ts). Two implementations: connect.ts (a real client) and mock.ts (a tiny fake
// shell, for the mock transport).
//
// A terminal belongs to a session of the client's machine (sessions of other machines are refused) or is
// standalone. It keeps running when the pane disconnects; connecting again replays its recent output (~256 KB).

import type { TerminalConnection, TerminalConnectionOptions } from './connection'

export interface TerminalInfo {
  id: string
  // Empty for a standalone terminal.
  sessionId: string
  title: string
  cwd: string
  // The shell's path, e.g. "/bin/zsh".
  shell: string
  pid: number
  running: boolean
  // Set once the shell has exited (128 + signal number when it was killed by a signal).
  exitCode?: number
  // RFC 3339.
  createdAt: string
  cols: number
  rows: number
}

export interface CreateTerminalOptions {
  // A live session of the client's machine; omitted for a standalone terminal.
  sessionId?: string
  // Absolute or "~/..."; inside the session's working directories. Omitted: the session's cwd, or home.
  cwd?: string
  cols?: number
  rows?: number
  // Extra environment variables (TERM=xterm-256color and COLORTERM=truecolor are always set).
  env?: Record<string, string>
  // Omitted: the cwd's base name.
  title?: string
}

// A terminal with a one-time ticket for its socket (valid for ~30 s).
export interface TerminalTicket {
  terminal: TerminalInfo
  ticket: string
}

export interface TerminalsApi {
  // Starts a shell; connect to it with connect(terminal.id, ticket).
  create(opts: CreateTerminalOptions): Promise<TerminalTicket>
  // A fresh ticket to (re)connect to a terminal. TerminalConnection calls it on its own.
  attach(terminalId: string): Promise<TerminalTicket>
  // The terminals of a session, or all terminals of the client when sessionId is omitted; oldest first.
  list(sessionId?: string): Promise<TerminalInfo[]>
  // Ends the shell (if still running) and forgets the terminal.
  kill(terminalId: string): Promise<void>
  // Resizes without a connection (a connected pane uses TerminalConnection.resize).
  resize(terminalId: string, cols: number, rows: number): Promise<void>
  // Opens the interactive connection; with a ticket from create/attach it is used for the first connect.
  connect(terminalId: string, ticket?: string, opts?: TerminalConnectionOptions): TerminalConnection
}
