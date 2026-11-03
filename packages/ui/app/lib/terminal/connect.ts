// TerminalsApi over AgentService's terminal RPCs, on the same connect transport (and agent token) as the rest of
// AgentService; the I/O goes over the client's terminal WebSocket (./connection.ts).

import { Code, ConnectError, createClient, type Client, type Transport } from '@connectrpc/connect'
import { AgentService } from '~/gen/harness/agent/v1/agent_pb'
import type { TerminalInfo as TerminalInfoPb } from '~/gen/harness/agent/v1/terminal_pb'
import { TerminalConnection, TerminalGoneError, webSocketOpener, type TerminalConnectionOptions, type TerminalLink } from './connection'
import type { CreateTerminalOptions, TerminalInfo, TerminalTicket, TerminalsApi } from './types'

export function terminalView(t: TerminalInfoPb | undefined): TerminalInfo {
  if (!t) throw new Error('the client returned no terminal')
  return {
    id: t.terminalId,
    sessionId: t.sessionId,
    title: t.title,
    cwd: t.cwd,
    shell: t.shell,
    pid: t.pid,
    running: t.running,
    exitCode: t.exitCode,
    createdAt: t.createdAt,
    cols: t.cols,
    rows: t.rows
  }
}

export class ConnectTerminalsApi implements TerminalsApi {
  private client: Client<typeof AgentService>
  private link: TerminalLink

  // agentUrl is the client's base URL (the socket is served next to AgentService); WS is for tests.
  constructor(transport: Transport, agentUrl: string, WS?: typeof WebSocket) {
    this.client = createClient(AgentService, transport)
    this.link = { attach: id => this.attach(id), open: webSocketOpener(agentUrl, WS) }
  }

  async create(opts: CreateTerminalOptions): Promise<TerminalTicket> {
    const res = await this.client.createTerminal({
      sessionId: opts.sessionId ?? '',
      cwd: opts.cwd ?? '',
      cols: opts.cols ?? 0,
      rows: opts.rows ?? 0,
      env: opts.env ?? {},
      title: opts.title ?? ''
    })
    return { terminal: terminalView(res.terminal), ticket: res.ticket }
  }

  async attach(terminalId: string): Promise<TerminalTicket> {
    try {
      const res = await this.client.attachTerminal({ terminalId })
      return { terminal: terminalView(res.terminal), ticket: res.ticket }
    } catch (err) {
      if (ConnectError.from(err).code === Code.NotFound) throw new TerminalGoneError(terminalId)
      throw err
    }
  }

  async list(sessionId = '') {
    return (await this.client.listTerminals({ sessionId })).terminals.map(terminalView)
  }

  async kill(terminalId: string) {
    await this.client.killTerminal({ terminalId })
  }

  async resize(terminalId: string, cols: number, rows: number) {
    await this.client.resizeTerminal({ terminalId, cols, rows })
  }

  connect(terminalId: string, ticket?: string, opts?: TerminalConnectionOptions) {
    const conn = new TerminalConnection(this.link, terminalId, ticket, opts)
    void conn.connect()
    return conn
  }
}
