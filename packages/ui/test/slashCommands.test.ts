import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { SendUserMessageRequest_Delivery } from '~/gen/harness/agent/v1/agent_pb'
import { SlashCommandKind, SlashCommandResult_Action, SlashCommandSchema } from '~/gen/harness/agent/v1/slash_commands_pb'
import { pendingMessages } from '~/lib/session/delivery'
import { createSessionState, reduceAll } from '~/lib/session/reducer'
import { commandFacts, customizeSections, matchSlash, SLASH_ACTION_FRESH_MS, slashMenuOptions, sourceLabel, takeSlashAction } from '~/lib/session/slashCommands'
import { EventBuilder } from '~/lib/transport/events'
import { MockTransport } from '~/lib/transport/mock'
import { toolIcon, toolTarget } from '~/utils/tools'

const command = (name: string, o: Partial<Parameters<typeof create<typeof SlashCommandSchema>>[1]> = {}) =>
  create(SlashCommandSchema, { name, kind: SlashCommandKind.SKILL, source: 'project', enabled: true, userInvocable: true, modelInvocable: true, ...o })

describe('the slash menu', () => {
  it('shows what the client reports, and only what can be typed', () => {
    const rows = slashMenuOptions([
      command('compact', { kind: SlashCommandKind.BUILTIN, source: 'builtin', needsSession: true, description: 'Shrink' }),
      command('deploy', { argumentHint: '[env]', description: 'Ship it' }),
      command('fe:test', { kind: SlashCommandKind.COMMAND }),
      command('review', { kind: SlashCommandKind.AGENT, source: 'agent', needsSession: true }),
      command('off', { enabled: false }),
      command('modelonly', { userInvocable: false }),
      command('loser', { shadowedBy: 'personal' })
    ], { hasSession: true })
    expect(rows.map(r => [r.label, r.tag])).toEqual([['/compact', 'built-in'], ['/deploy', 'skill'], ['/fe:test', 'command'], ['/review', 'agent']])
    expect(rows[1]!.detail).toBe('[env] · Ship it')
  })

  it('leaves out what needs a conversation before a session exists', () => {
    const rows = slashMenuOptions([command('compact', { needsSession: true }), command('model')], { hasSession: false })
    expect(rows.map(r => r.key)).toEqual(['/model'])
  })

  it('matches the start of a name, then the last part of a namespaced one', () => {
    const rows = slashMenuOptions([command('fe:test'), command('test-all'), command('deploy')], { hasSession: true })
    expect(matchSlash(rows, '/').map(r => r.key)).toEqual(['/fe:test', '/test-all', '/deploy'])
    expect(matchSlash(rows, '/te').map(r => r.key)).toEqual(['/test-all', '/fe:test'])
    expect(matchSlash(rows, '/zz')).toEqual([])
  })

  it('has no demo rows in the mock client', async () => {
    const t = new MockTransport({ tick: 0 })
    const { commands } = await t.listSlashCommands({ cwd: '/w' })
    const names = commands.map(c => c.name)
    expect(names).not.toContain('deploy-core')
    for (const n of ['compact', 'clear', 'model', 'effort', 'init', 'code-review']) expect(names).toContain(n)
  })
})

describe('slash command results', () => {
  it('settle the message, show the text and keep the action', () => {
    const b = new EventBuilder('s1', Date.parse('2026-11-06T12:00:00Z'))
    const s = createSessionState('s1')
    b.slashResult('u1', 'model', { text: 'Model: other' })
    b.slashResult('u2', 'model', { action: SlashCommandResult_Action.PICK_MODEL })
    b.slashResult('', 'effort', { text: 'not a level', isError: true })
    reduceAll(s, b.events)
    const notices = s.items.filter(i => i.kind === 'notice')
    expect(notices.map(n => [n.text, n.level])).toEqual([['Model: other', 'info'], ['not a level', 'warning']])
    expect(pendingMessages(s, [{ id: 'u1', text: '/model other', at: 1, state: 'sent' }])).toEqual([])
    expect(s.slashAction).toMatchObject({ id: 'u2', action: SlashCommandResult_Action.PICK_MODEL })
  })

  it('act once, and only while fresh', () => {
    const a = { id: 'x', action: SlashCommandResult_Action.OPEN_CUSTOMIZE, newSessionId: '', at: 1000 }
    expect(takeSlashAction(a, [], 1000)).toBe(true)
    expect(takeSlashAction(a, ['x'], 1000)).toBe(false)
    expect(takeSlashAction(a, [], 1000 + SLASH_ACTION_FRESH_MS)).toBe(false)
    expect(takeSlashAction(undefined, [], 1000)).toBe(false)
  })
})

describe('skills in the transcript', () => {
  it('notes what the user started; the tool row shows the model\'s', () => {
    const b = new EventBuilder('s1', 0)
    const s = createSessionState('s1')
    b.skillInvoked('deploy', { source: 'project', model: 'fast' })
    b.skillInvoked('guide', { toolUseId: 't1' })
    reduceAll(s, b.events)
    expect(s.items.filter(i => i.kind === 'notice').map(n => n.text)).toEqual(['Started /deploy (project, on fast)'])
  })

  it('labels the Skill tool', () => {
    const t = { kind: 'tool', id: 't', name: 'Skill', input: { skill: 'deploy', args: 'prod' }, summary: '', status: 'ok', output: '', result: null, resultSummary: '', durationMs: 1, startedAt: 0, messageId: 'm', grouped: false } as never
    expect(toolTarget(t)).toBe('/deploy prod')
    expect(toolIcon(t)).toBe('page')
  })
})

describe('Customize', () => {
  it('groups by kind, leaving agents\' commands out', () => {
    const sections = customizeSections([
      command('compact', { kind: SlashCommandKind.BUILTIN }),
      command('a'),
      command('b', { kind: SlashCommandKind.COMMAND }),
      command('c', { kind: SlashCommandKind.AGENT })
    ])
    expect(sections.map(s => [s.title, s.rows.map(r => r.name)])).toEqual([['Skills', ['a']], ['Commands', ['b']], ['Built-in commands', ['compact']]])
    expect(customizeSections([])).toEqual([])
  })

  it('says what a row does', () => {
    expect(commandFacts(command('x', { modelInvocable: false, fork: true, model: 'm', effort: 'high', allowedTools: ['Read', 'Bash(git *)'], paths: ['**/*.go'], shadowedBy: 'personal' })))
      .toEqual(['replaced by personal', 'you only', 'runs in a subagent', 'model m', 'effort high', 'may use Read, Bash(git *)', 'for **/*.go'])
    expect(commandFacts(command('y', { userInvocable: false }))).toEqual(['model only'])
    expect(sourceLabel('imported')).toBe('Imported')
    expect(sourceLabel('odd')).toBe('odd')
  })

  it('keeps a switch in the mock client', async () => {
    const t = new MockTransport({ tick: 0 })
    const on = async () => (await t.listSlashCommands({ cwd: '/w' })).commands.find(c => c.name === 'release-notes')!.enabled
    expect(await on()).toBe(true)
    await t.setSkillEnabled('release-notes', false)
    expect(await on()).toBe(false)
    await t.setSkillEnabled('release-notes', true)
    expect(await on()).toBe(true)
  })
})

describe('built-ins in the mock client', () => {
  it('answer like the client, without a turn', async () => {
    const transport = new MockTransport({ tick: 0 })
    const info = await transport.createSession({ cwd: '/w' })
    const seen: string[] = []
    const stop = transport.subscribe(info.id, 0, { onEvent: (ev) => {
      if (ev.event.case === 'slashCommandResult') seen.push(`${ev.event.value.name}:${ev.event.value.action}`)
      if (ev.event.case === 'turnStarted') seen.push('turn')
    } })
    await new Promise(resolve => queueMicrotask(() => resolve(null)))
    await transport.sendUserMessage(info.id, 'u1', '/model', SendUserMessageRequest_Delivery.QUEUE)
    await transport.sendUserMessage(info.id, 'u2', '/skills', SendUserMessageRequest_Delivery.QUEUE)
    stop()
    expect(seen).toEqual([`model:${SlashCommandResult_Action.PICK_MODEL}`, `skills:${SlashCommandResult_Action.OPEN_CUSTOMIZE}`])
  })
})
