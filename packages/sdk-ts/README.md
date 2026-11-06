# @xlsft/harness-sdk

A small TypeScript SDK for xlsft\`s harness: it starts `harness-client` headless and speaks the [headless protocol](../../docs/protocol/headless.md) over its stdin and stdout. No dependencies; Node 24 runs the TypeScript directly.

The binary has to be on `PATH`, or named with `binary`, and signed in (`harness-client login`, or `HARNESS_DEVICE_TOKEN` and `HARNESS_SERVER_URL`).

## One prompt

```ts
import { run } from '@xlsft/harness-sdk'

const result = await run('Summarize the failing test', {
  allowedTools: ['Read', 'Bash(go test *)'],
  maxBudgetUsd: 1,
})
console.log(result.result)
```

`run` resolves with the result message, successful or not: check `result.is_error`. It rejects only when the process itself fails, for instance when the binary is missing or the flags are wrong.

## Structured output

```ts
const { structured_output: out } = await run('How many TODOs are in src/?', {
  jsonSchema: { type: 'object', properties: { count: { type: 'integer' } }, required: ['count'] },
})
```

## Events and more messages

```ts
import { startSession } from '@xlsft/harness-sdk'

const session = startSession({ cwd: '/work/project', permissionMode: 'acceptEdits' })

session.send('Rename the config struct')
for await (const message of session) {
  if (message.type === 'assistant') console.log(message.message.content)
  if (message.type === 'result') break
}

session.send('Now update the docs')
const second = await session.result()

session.interrupt() // stops a turn in progress
session.end()       // no more input: the process ends after the last message
await session.closed
```

`session.result()` skips to the next result message. Options map to the flags in the protocol: `model`, `effort`, `permissionMode`, `allowedTools`, `disallowedTools`, `addDirs`, `maxTurns`, `maxBudgetUsd`, `systemPrompt`, `appendSystemPrompt`, `jsonSchema`, `includePartialMessages`, `resume`, `continue`, `sessionId`, `noSessionPersistence`, `server`, plus `cwd`, `env`, `binary` and `extraArgs`.
