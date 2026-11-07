// What the mock client reports for the slash menu and Extensions: the built-ins the real one has, and one skill and one
// command for the switch.
import { create } from '@bufbuild/protobuf'
import { SlashCommandKind, SlashCommandSchema, type SlashCommand } from '~/gen/harness/agent/v1/slash_commands_pb'
import type { SlashCatalog } from '~/lib/session/slashCommands'

const builtin = (name: string, description: string, argumentHint = '', needsSession = false): SlashCommand => create(SlashCommandSchema, {
  name, description, argumentHint, needsSession, kind: SlashCommandKind.BUILTIN, source: 'builtin', enabled: true, userInvocable: true
})

export function mockSlashCatalog(disabled: ReadonlySet<string>): SlashCatalog {
  const own = (name: string, description: string, kind: SlashCommandKind, path: string): SlashCommand => create(SlashCommandSchema, {
    name, description, kind, path, source: 'project', enabled: !disabled.has(name), userInvocable: true, modelInvocable: true, switchable: true, listed: !disabled.has(name)
  })
  const commands = [
    builtin('compact', 'Summarize the conversation to free up context', '[what to focus on]', true),
    builtin('clear', 'Start a new session here with an empty context', '', true),
    builtin('model', 'Switch the session\'s model', '[model]'),
    builtin('effort', 'Set how hard the model thinks', '[low|medium|high|xhigh|max|auto]'),
    builtin('init', 'Write an instructions file for this repository', '[what to emphasize]'),
    builtin('code-review', 'Review the current diff for bugs and list the findings', '[range, files or focus]'),
    builtin('goal', 'Keep working until a condition holds, with a cap on the turns', '[--max N] <condition> | stop | resume', true),
    builtin('loop', 'Repeat a prompt or command here, on an interval or at the model\'s pace', '[interval] <prompt or /command> | stop | list', true),
    builtin('tools', 'List the tools offered to the model now, and why others are not', '', true),
    builtin('schedule', 'Open Scheduled: tasks that run by themselves'),
    builtin('skills', 'Open Extensions: skills and commands, with their switches'),
    own('release-notes', 'Draft release notes from the commits since the last tag', SlashCommandKind.SKILL, '.harness/skills/release-notes/SKILL.md'),
    own('fe:test', 'Run the frontend tests', SlashCommandKind.COMMAND, '.harness/commands/fe/test.md')
  ]
  return { commands, projectSkippedUntrusted: false, listingBudgetChars: 8000, listingUsedChars: 120 }
}
