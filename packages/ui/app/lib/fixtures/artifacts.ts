// Demo data for the Artifacts page (docs/design/ui/ArtifactsPage.dc.html). Artifacts are not part
// of the agent contract yet.
import type { IconName } from '~/utils/icons'

export type ArtifactType = 'doc' | 'slides' | 'design' | 'dash' | 'page'

export const ARTIFACT_TYPES: Record<ArtifactType, { color: string, tint: string, icon: IconName }> = {
  doc: { color: 'var(--color-accent)', tint: '#2b7fff1f', icon: 'doc' },
  slides: { color: 'var(--color-orange)', tint: '#fe6e001f', icon: 'slides' },
  design: { color: 'var(--color-violet)', tint: '#8e51ff26', icon: 'design' },
  dash: { color: 'var(--color-bad)', tint: '#ff20561f', icon: 'dash' },
  page: { color: 'var(--color-muted)', tint: 'var(--color-raised)', icon: 'page' }
}

export const ARTIFACT_KINDS: { name: string, type: ArtifactType, badge: string, badgeColor: string }[] = [
  { name: 'Docs', type: 'doc', badge: 'New', badgeColor: 'var(--color-accent)' },
  { name: 'Slides', type: 'slides', badge: 'New', badgeColor: 'var(--color-accent)' },
  { name: 'Design', type: 'design', badge: 'New', badgeColor: 'var(--color-accent)' },
  { name: 'Dashboard', type: 'dash', badge: 'Beta', badgeColor: 'var(--color-muted)' }
]

export interface ArtifactEntry {
  title: string
  type: ArtifactType
  when: string
  shared: boolean
}

const I = (title: string, type: ArtifactType, when: string, shared = false): ArtifactEntry => ({ title, type, when, shared })

export const ARTIFACTS: ArtifactEntry[] = [
  I('harness desktop', 'design', 'Opened 3m ago'),
  I('ymcp pipeline', 'page', 'Opened 29m ago', true),
  I('harness CLI identity', 'page', 'Opened 1h ago'),
  I('Registry layout verdicts', 'page', 'Opened Oct 29', true),
  I('ymcp: promo video storyboard', 'doc', 'Opened Oct 28'),
  I('ymcp', 'page', 'Opened Oct 28'),
  I('Binrobi Model 1: a free smartphone, review outline', 'doc', 'Opened Oct 27'),
  I('Companion screens', 'design', 'Opened Oct 25'),
  I('Ore completeness audit vs JavaScript/TypeScript', 'doc', 'Opened Oct 16'),
  I('Open LLMs vs Claude', 'dash', 'Opened Oct 14'),
  I('Crumble auth panel', 'page', 'Opened Oct 12'),
  I('Native TypeScript compiler', 'doc', 'Opened Oct 10'),
  I('Crumble auth variants', 'page', 'Opened Oct 8'),
  I('Crumble action log', 'page', 'Opened Oct 8')
]

export const ARTIFACT_FILTERS = ['All', 'Yours', 'Shared with you'] as const
