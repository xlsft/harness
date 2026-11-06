// TypeScript SDK over headless harness-client: docs/protocol/headless.md is the contract.

import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { createInterface } from 'node:readline'

export interface Options {
  /** The binary; default `harness-client` on PATH. */
  binary?: string
  cwd?: string
  /** Added to the process's environment. */
  env?: Record<string, string>
  server?: string
  model?: string
  effort?: 'low' | 'medium' | 'high' | 'xhigh' | 'max'
  permissionMode?: 'default' | 'acceptEdits' | 'plan' | 'auto' | 'dontAsk' | 'bypassPermissions'
  allowedTools?: string[]
  disallowedTools?: string[]
  addDirs?: string[]
  maxTurns?: number
  maxBudgetUsd?: number
  systemPrompt?: string
  appendSystemPrompt?: string
  /** A JSON schema the final message must match; the value comes back as `structured_output`. */
  jsonSchema?: object
  includePartialMessages?: boolean
  resume?: string
  continue?: boolean
  sessionId?: string
  noSessionPersistence?: boolean
  /** Flags this SDK does not know yet. */
  extraArgs?: string[]
}

export interface ResultMessage {
  type: 'result'
  subtype: string
  is_error: boolean
  session_id: string
  uuid: string
  result?: string
  structured_output?: unknown
  errors?: string[]
  interrupted?: boolean
  num_turns: number
  duration_ms: number
  duration_api_ms: number
  total_cost_usd: number
  usage: Record<string, unknown>
  modelUsage?: Record<string, unknown>
  stop_reason?: string | null
  permission_denials: unknown[]
}

export interface SystemMessage {
  type: 'system'
  subtype: string
  session_id: string
  uuid: string
  [key: string]: unknown
}

export interface AssistantMessage {
  type: 'assistant'
  session_id: string
  uuid: string
  message: { role: 'assistant', content: ContentBlock[], model?: string, usage?: Record<string, unknown>, stop_reason?: string | null }
}

export interface UserMessage {
  type: 'user'
  session_id: string
  uuid: string
  message: { role: 'user', content: ContentBlock[] }
  client_uuids?: string[]
  tool_use_result?: unknown
}

export interface StreamEventMessage {
  type: 'stream_event'
  session_id: string
  uuid: string
  event: unknown
}

export type Message = SystemMessage | AssistantMessage | UserMessage | StreamEventMessage | ResultMessage

export interface ContentBlock {
  type: string
  text?: string
  [key: string]: unknown
}

export interface SendOptions {
  /** Comes back in `client_uuids` of the user message it became. */
  uuid?: string
}

/** The flags of Options, for the caller who starts the process itself. */
export function buildArgs(o: Options): string[] {
  const args = ['-p', '--output-format', 'stream-json', '--input-format', 'stream-json']
  const value = (flag: string, v: string | number | undefined) => {
    if (v !== undefined && v !== '') args.push(flag, String(v))
  }
  value('--server', o.server)
  value('--model', o.model)
  value('--effort', o.effort)
  value('--permission-mode', o.permissionMode)
  for (const t of o.allowedTools ?? []) args.push('--allowed-tools', t)
  for (const t of o.disallowedTools ?? []) args.push('--disallowed-tools', t)
  for (const d of o.addDirs ?? []) args.push('--add-dir', d)
  value('--max-turns', o.maxTurns)
  value('--max-budget-usd', o.maxBudgetUsd)
  value('--system-prompt', o.systemPrompt)
  value('--append-system-prompt', o.appendSystemPrompt)
  if (o.jsonSchema) args.push('--json-schema', JSON.stringify(o.jsonSchema))
  if (o.includePartialMessages) args.push('--include-partial-messages')
  value('--resume', o.resume)
  if (o.continue) args.push('--continue')
  value('--session-id', o.sessionId)
  if (o.noSessionPersistence) args.push('--no-session-persistence')
  args.push(...(o.extraArgs ?? []))
  return args
}

/** A running harness-client: send messages, read what comes back. Reading ends when the process ends. */
export class Session implements AsyncIterable<Message> {
  private proc: ChildProcessWithoutNullStreams
  private queue: Message[] = []
  private waiters: (() => void)[] = []
  private done = false
  private failure: Error | undefined
  private stderr = ''
  /** The process's exit code, once it ended; rejects when it could not start or ended with no result at all. */
  readonly closed: Promise<number>

  constructor(o: Options = {}) {
    this.proc = spawn(o.binary ?? 'harness-client', buildArgs(o), {
      cwd: o.cwd,
      env: { ...process.env, ...o.env },
      stdio: ['pipe', 'pipe', 'pipe']
    })
    this.proc.stderr.setEncoding('utf8')
    this.proc.stderr.on('data', (s: string) => {
      this.stderr = (this.stderr + s).slice(-8192)
    })
    // A process that died cannot take more input; the reason comes through closed.
    this.proc.stdin.on('error', () => {})
    let sawResult = false
    const lines = createInterface({ input: this.proc.stdout })
    lines.on('line', (line) => {
      if (!line.trim()) return
      let m: Message
      try {
        m = JSON.parse(line)
      } catch {
        return
      }
      if (m.type === 'result') sawResult = true
      this.queue.push(m)
      this.wake()
    })
    const linesClosed = new Promise<void>(resolve => lines.once('close', resolve))
    this.closed = new Promise<number>((resolve, reject) => {
      this.proc.once('error', (err) => {
        this.finish(err)
        reject(err)
      })
      this.proc.once('close', (code) => {
        void linesClosed.then(() => {
          const c = code ?? 1
          if (c !== 0 && !sawResult) {
            const err = new Error(`harness-client exited with ${c}: ${this.stderr.trim()}`)
            this.finish(err)
            reject(err)
            return
          }
          this.finish()
          resolve(c)
        })
      })
    })
    // Nobody has to look at closed; a rejection also reaches whoever reads the messages.
    this.closed.catch(() => {})
  }

  private finish(err?: Error) {
    this.done = true
    this.failure ??= err
    this.wake()
  }

  private wake() {
    const w = this.waiters
    this.waiters = []
    for (const f of w) f()
  }

  /** Sends a user message: text, or text and image blocks. */
  send(content: string | ContentBlock[], o: SendOptions = {}): void {
    this.write({ type: 'user', ...(o.uuid && { uuid: o.uuid }), message: { role: 'user', content } })
  }

  /** Stops the turn in progress. It ends with a result that says `interrupted`. */
  interrupt(): void {
    this.write({ type: 'control', subtype: 'interrupt' })
  }

  /** No more input: the process ends after the last message. */
  end(): void {
    this.proc.stdin.end()
  }

  /** Ends the process now. */
  kill(): void {
    this.proc.kill()
  }

  private write(v: unknown) {
    if (this.done || this.proc.stdin.writableEnded) throw new Error('the session has ended')
    this.proc.stdin.write(JSON.stringify(v) + '\n')
  }

  /** The next message, or undefined when the process has ended. */
  async next(): Promise<Message | undefined> {
    for (;;) {
      const m = this.queue.shift()
      if (m) return m
      if (this.failure) throw this.failure
      if (this.done) return undefined
      await new Promise<void>(resolve => this.waiters.push(resolve))
    }
  }

  /** Skips to the next result message. */
  async result(): Promise<ResultMessage> {
    for (;;) {
      const m = await this.next()
      if (!m) throw new Error(`harness-client ended without a result: ${this.stderr.trim()}`)
      if (m.type === 'result') return m
    }
  }

  async* [Symbol.asyncIterator](): AsyncGenerator<Message> {
    for (;;) {
      const m = await this.next()
      if (!m) return
      yield m
    }
  }
}

/** Starts harness-client with stream input and output. */
export function startSession(o: Options = {}): Session {
  return new Session(o)
}

/** Runs one prompt and resolves with its result message, successful or not. */
export async function run(prompt: string, o: Options = {}): Promise<ResultMessage> {
  const s = new Session(o)
  try {
    s.send(prompt)
    s.end()
    return await s.result()
  } finally {
    await s.closed.catch(() => {})
  }
}
