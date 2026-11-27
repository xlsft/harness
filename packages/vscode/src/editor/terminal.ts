// Terminal integration, where it is cheap: terminals started in this window reach this window's bridge, the terminal
// interface opens in one, and the last command of a terminal can go to the agent. Shell integration reports the commands;
// a shell without it has nothing to report.
import * as vscode from 'vscode'

// How much of a command's output is kept. Terminals can produce without end.
const MAX_OUTPUT = 32 << 10

export interface FinishedCommand {
  command: string
  output: string
  exitCode: number | undefined
}

/** Strips what is for the terminal's eyes: colors, cursor moves and window titles. */
export function plainOutput(text: string): string {
  return text
    // eslint-disable-next-line no-control-regex
    .replace(/\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g, '')
    // eslint-disable-next-line no-control-regex
    .replace(/\x1b\[[0-9;?]*[ -/]*[@-~]/g, '')
    .replace(/\r\n/g, '\n')
}

export function formatCommand(c: FinishedCommand): string {
  const status = c.exitCode === undefined ? '' : ` (exit code ${c.exitCode})`
  return `Terminal command: \`${c.command}\`${status}\n\`\`\`\n${c.output.trimEnd()}\n\`\`\`\n`
}

export class TerminalWatcher implements vscode.Disposable {
  private readonly disposables: vscode.Disposable[] = []
  private last: FinishedCommand | undefined

  constructor() {
    this.disposables.push(vscode.window.onDidStartTerminalShellExecution(e => void this.capture(e)))
  }

  private async capture(e: vscode.TerminalShellExecutionStartEvent) {
    let output = ''
    const command = e.execution.commandLine.value
    try {
      for await (const chunk of e.execution.read()) {
        output = (output + chunk).slice(-MAX_OUTPUT)
      }
    } catch {
      // The terminal went away: what was read still counts.
    }
    const ended = await new Promise<number | undefined>((resolve) => {
      const sub = vscode.window.onDidEndTerminalShellExecution((end) => {
        if (end.execution !== e.execution) return
        sub.dispose()
        resolve(end.exitCode)
      })
      setTimeout(() => {
        sub.dispose()
        resolve(undefined)
      }, 1000)
    })
    this.last = { command, output: plainOutput(output), exitCode: ended }
  }

  lastCommand(): FinishedCommand | undefined {
    return this.last
  }

  dispose() {
    for (const d of this.disposables) d.dispose()
  }
}
