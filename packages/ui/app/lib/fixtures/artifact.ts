// Demo data for the Artifact pane (docs/design/ui/ArtifactPane.dc.html): the open artifact's page,
// its title menu and a comment thread. Artifacts are not part of the agent contract yet.
import type { IconName } from '~/utils/icons'

export const ARTIFACT = {
  title: 'ymcp pipeline',
  author: 'By you',
  updated: 'Updated just now',
  version: { from: 'v9', to: 'v10' },
  eyebrow: 'Design document',
  heading: { mark: 'ymcp', rest: ' pipeline' },
  lead: 'A pipeline that takes issues from YouTrack, hands them to an AI agent, opens a PR or MR, moves the issue across the board and answers feedback without the developer.',
  status: 'Draft',
  date: '27 Oct 2026',
  repo: 'github.com/xlsft/ymcp',
  toc: [
    { n: '01', t: 'Goal and scope' }, { n: '02', t: 'Principles' }, { n: '03', t: 'Architecture' }, { n: '04', t: 'Run lifecycle' },
    { n: '05', t: 'Configuration' }, { n: '06', t: 'State in SQLite' }, { n: '07', t: 'Agent messages' }, { n: '08', t: 'Feedback' }
  ],
  section: {
    n: '01',
    title: 'Goal and scope',
    before: 'The user describes a pipeline once: where issues come from, how they move between stages, which agent solves them with which prompts, where the code goes. After that ',
    code: 'ymcp pipeline <name> run -d',
    after: ' runs on its own, and a person is needed only when the agent asks for input.'
  }
}

export interface ArtifactComment {
  author: string
  agent?: boolean
  when: string
  text: string
  note?: string
}

export const ARTIFACT_THREAD = {
  marker: 'X',
  comments: [
    { author: 'xlsft', when: '2 min ago', text: 'Shorten this section, the scope lists can be a table.', note: 'sent to the agent' },
    { author: 'agent', agent: true, when: 'via you · just now', text: 'Done: two sentences of intro, in and out of scope moved into one table.' }
  ] as ArtifactComment[]
}

export interface ArtifactMenuItem {
  label: string
  icon: IconName
  danger?: boolean
  disabled?: boolean
}

export const ARTIFACT_MENU: (ArtifactMenuItem | 'sep')[] = [
  { label: 'Export', icon: 'download' }, { label: 'Copy as Markdown', icon: 'copy' }, 'sep',
  { label: 'Share', icon: 'upload' }, { label: 'Duplicate', icon: 'copy' }, 'sep',
  { label: 'Rename', icon: 'edit' }, 'sep',
  { label: 'Pin', icon: 'pin' }, { label: 'Permissions', icon: 'lock', disabled: true }, 'sep',
  { label: 'Refresh', icon: 'refresh' }, { label: 'Pop out', icon: 'copy' }, { label: 'Open in web', icon: 'external' }, 'sep',
  { label: 'Give feedback', icon: 'feedback' }, { label: 'Report artifact', icon: 'flag' }, 'sep',
  { label: 'Delete artifact', icon: 'trash', danger: true }
]
