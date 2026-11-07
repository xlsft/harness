<script setup lang="ts">
// The detail of an item: what it holds, what it may do, where it comes from, its version and approval, with what can be done about it.
import { isDesktop, revealInFolder } from '~/lib/desktop'
import type { ConnectorOption, DiscoverItem, ExtensionConnector, ExtensionMarketplace, ExtensionPluginVM, ExtensionSkill, GetExternalSkillResponse, GetSkillDetailResponse, InstalledExternalSkill } from '~/lib/extensions/types'
import { connectorStatus, installsLabel, providedBy, shortDate, TONE_CLASS } from '~/lib/extensions/view'
import type { InspectMcpServerResponse } from '~/lib/mcp/types'
import { runsPrograms, statusLabel, type PluginContentsVM } from '~/lib/plugins/types'
import { commandFacts, sourceLabel } from '~/lib/session/slashCommands'

export type DetailTarget
  = | { kind: 'skill', row: ExtensionSkill }
    | { kind: 'plugin', row: ExtensionPluginVM }
    | { kind: 'connector', row: ExtensionConnector }
    | { kind: 'discover', item: DiscoverItem }
    | { kind: 'marketplace', market: ExtensionMarketplace }

const props = defineProps<{ target: DetailTarget, cwd: string, canManageOrg: boolean }>()
const emit = defineEmits<{ 'close': [], 'changed': [], 'open-plugin': [string], 'add-connector': [ConnectorOption], 'target': [DetailTarget] }>()
const store = useSessionStore()
const extensions = () => store.transport!.extensions()
const plugins = () => store.transport!.plugins()
const mcp = () => store.transport!.mcp()

const error = ref('')
const note = ref('')
const busy = ref(false)
const skillDetail = ref<GetSkillDetailResponse | null>(null)
const external = ref<GetExternalSkillResponse | null>(null)
const inspection = ref<InspectMcpServerResponse | undefined>()
const inspecting = ref(false)
const editing = ref(false)

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function run(f: () => Promise<unknown>, done = '') {
  busy.value = true
  note.value = ''
  try {
    await f()
    error.value = ''
    note.value = done
    emit('changed')
  } catch (e) {
    error.value = message(e)
  } finally {
    busy.value = false
  }
}

const title = computed(() => {
  const t = props.target
  switch (t.kind) {
    case 'skill': return t.row.command?.name ?? t.row.external?.name ?? ''
    case 'plugin': return t.row.plugin.displayName || t.row.plugin.name
    case 'connector': return t.row.name
    case 'discover': return t.item.name
    case 'marketplace': return t.market.name
  }
  return ''
})

const stored = computed<InstalledExternalSkill | undefined>(() => (props.target.kind === 'skill' ? props.target.row.external : undefined))
const entry = computed(() => (props.target.kind === 'connector' ? props.target.row.entry : undefined))

async function loadExtras() {
  error.value = ''
  skillDetail.value = null
  external.value = null
  inspection.value = undefined
  editing.value = false
  const t = props.target
  try {
    if (t.kind === 'skill' && t.row.command) {
      skillDetail.value = await extensions().skillDetail(t.row.command.name, t.row.command.path, props.cwd)
    } else if (t.kind === 'discover' && t.item.externalId) {
      external.value = await extensions().externalSkill(t.item.externalId)
    } else if (t.kind === 'connector' && t.row.entry?.server?.id && t.row.entry.state === 1) {
      inspecting.value = true
      inspection.value = await mcp().inspect(t.row.entry.server.id, props.cwd)
    }
  } catch (e) {
    error.value = message(e)
  } finally {
    inspecting.value = false
  }
}
watch(() => props.target, loadExtras, { immediate: true })

function discoverContents(it: DiscoverItem): PluginContentsVM | null {
  const c = it.contents
  if (!c) return null
  return { skills: c.skills, commands: c.commands, agents: c.agents, outputStyles: c.outputStyles, allowedTools: c.allowedTools, skipped: c.skipped,
    hooks: c.hooks.map(h => ({ event: h.event, matcher: h.matcher, type: h.type, command: h.command, fromSkill: h.fromSkill })),
    mcpServers: c.mcpServers.map(m => ({ name: m.name, transport: m.transport, command: m.command })) }
}

// ---- plugins ----
function installListing(it: DiscoverItem) {
  return run(async () => {
    const p = await plugins().install(it.pluginId, props.cwd)
    emit('target', { kind: 'plugin', row: { plugin: p, category: it.category, tags: it.tags, publisher: it.publisher, homepage: it.homepage, license: it.license, folder: '' } })
  })
}
async function refreshPlugin(row: ExtensionPluginVM) {
  const st = await plugins().list(props.cwd)
  const p = st.plugins.find(x => x.id === row.plugin.id)
  if (p) emit('target', { kind: 'plugin', row: { ...row, plugin: p } })
}
function pluginChange(row: ExtensionPluginVM, f: () => Promise<unknown>, done = '') {
  return run(async () => {
    await f()
    await refreshPlugin(row)
  }, done)
}
function updatePlugin(row: ExtensionPluginVM) {
  return pluginChange(row, async () => {
    const res = await plugins().update(row.plugin.id, props.cwd)
    note.value = res.note
  })
}
function removePlugin(row: ExtensionPluginVM) {
  if (!window.confirm(tt('Remove {0} and its data folder?', [row.plugin.id]))) return
  return run(async () => {
    await plugins().remove(row.plugin.id, false)
    emit('close')
  })
}

// ---- skills ----
function toggleSkill(row: ExtensionSkill) {
  const c = row.command
  if (!c) return
  return run(async () => {
    await store.transport!.setSkillEnabled(c.name, !c.enabled)
    c.enabled = !c.enabled
  })
}
function removeSkill(row: ExtensionSkill) {
  const c = row.command
  if (!c || !window.confirm(tt('Delete {0} from this device? Its files go too.', [c.name]))) return
  return run(async () => {
    await extensions().removeSkill(c.name, c.path, props.cwd)
    emit('close')
  })
}

// ---- external skills ----
function installExternal(it: DiscoverItem) {
  const shown = external.value
  if (!shown) return
  return run(async () => {
    const skill = await extensions().installExternal(it.externalId, shown.hash)
    if (skill) emit('target', { kind: 'skill', row: { $typeName: 'harness.agent.v1.ExtensionSkill', pluginId: '', updatedAt: '', canRemove: false, folder: skill.folder, blockedByPolicy: false, external: skill } })
  }, 'Stored. It doesn\'t load until you approve it below.')
}
function approveExternal(e: InstalledExternalSkill, approved: boolean) {
  return run(async () => {
    await extensions().setExternalApproval(e.name, approved, approved ? e.contentHash : '')
    e.approved = approved
  }, approved ? 'Approved on this device.' : 'It no longer loads here.')
}
function removeExternal(e: InstalledExternalSkill) {
  if (!window.confirm(tt('Delete {0} from this device? Its files go too.', [e.name]))) return
  return run(async () => {
    await extensions().removeExternal(e.name)
    emit('close')
  })
}

// ---- marketplaces ----
function removeMarket(m: ExtensionMarketplace) {
  if (!window.confirm(tt('Remove the marketplace {0} and the plugins installed from it?', [m.name]))) return
  return run(async () => {
    await plugins().removeMarketplace(m.name)
    emit('close')
  })
}

function reveal(p: string) {
  return revealInFolder(p)
}
const desktop = isDesktop()
// A connector acts from its own block, and a built-in marketplace has nothing to do: no empty band under them.
const hasActions = computed(() => {
  const t = props.target
  if (t.kind === 'skill' || t.kind === 'plugin') return true
  if (t.kind === 'discover') return !!(t.item.externalId || t.item.pluginId)
  return t.kind === 'marketplace' && !t.market.builtin
})
</script>

<template>
  <ExtensionsSheet
    :label="title"
    :footer="hasActions"
    @close="emit('close')"
  >
    <template #header>
      <h2 class="m-0 break-words text-[15px] font-semibold text-strong">
        {{ title }}
      </h2>
      <p
        v-if="target.kind === 'plugin'"
        class="m-0 text-[12px]"
        :class="target.row.plugin.status === 'active' ? 'text-ok' : target.row.plugin.status === 'disabled' ? 'text-muted' : 'text-warn'"
      >
        {{ statusLabel(target.row.plugin.status) }}
      </p>
      <p
        v-else-if="target.kind === 'connector'"
        class="m-0 text-[12px]"
        :class="TONE_CLASS[connectorStatus(target.row.status).tone]"
      >
        {{ connectorStatus(target.row.status).label }}
      </p>
      <p
        v-else-if="target.kind === 'discover'"
        class="m-0 text-[12px] text-muted"
      >
        {{ target.item.sourceLabel }}
      </p>
    </template>

    <div class="flex flex-col gap-4 text-[12.5px]">
      <p
        v-if="error"
        role="alert"
        class="m-0 border border-l-2 border-default border-l-bad px-2.5 py-1.5 text-toned"
      >
        {{ error }}
      </p>
      <p
        v-if="note"
        role="status"
        class="m-0 border border-l-2 border-default border-l-accent px-2.5 py-1.5 text-toned"
      >
        {{ note }}
      </p>

      <!-- A skill -->
      <template v-if="target.kind === 'skill'">
        <p class="m-0 text-toned">
          {{ target.row.command?.description ?? stored?.description }}
        </p>
        <p
          v-if="target.row.blockedByPolicy"
          class="m-0 border border-l-2 border-default border-l-warn px-2.5 py-1.5 text-toned"
        >
          {{ tt('Set by your administrator: this skill can\'t run.') }}
        </p>

        <template v-if="stored">
          <section
            class="flex flex-col gap-2 border border-default p-3"
            data-approval
          >
            <h3 class="m-0 text-[12.5px] text-strong">
              {{ tt('Approval on this device') }}
            </h3>
            <p
              class="m-0 text-[12px]"
              :class="stored.approved ? 'text-ok' : 'text-warn'"
            >
              {{ stored.approved ? tt('Approved as it is now.') : stored.changedOnDisk ? tt('Its files no longer match what was stored. Install it again.') : tt('It doesn\'t load until you approve exactly these files.') }}
            </p>
            <ExtensionsAudits :audits="stored.audits" />
          </section>
          <dl class="m-0 grid grid-cols-[110px_1fr] gap-x-3 gap-y-1.5 text-[12px] max-sm:grid-cols-1">
            <dt class="text-muted">
              {{ tt('Source') }}
            </dt><dd class="m-0 break-all text-toned">
              {{ tt('skills.sh · {0}', [stored.id]) }}
            </dd>
            <template v-if="installsLabel(stored.installs)">
              <dt class="text-muted">
                {{ tt('Installs') }}
              </dt><dd class="m-0 text-toned">
                {{ tt('{0}, as the source counts', [installsLabel(stored.installs)]) }}
              </dd>
            </template>
            <template v-if="stored.license">
              <dt class="text-muted">
                {{ tt('License') }}
              </dt><dd class="m-0 text-toned">
                {{ stored.license }}
              </dd>
            </template>
            <dt class="text-muted">
              {{ tt('Pinned to') }}
            </dt><dd class="m-0 break-all text-toned">
              {{ stored.pinnedHash }}
            </dd>
            <dt class="text-muted">
              {{ tt('On disk') }}
            </dt><dd class="m-0 break-all text-toned">
              {{ stored.contentHash }}
            </dd>
            <dt class="text-muted">
              {{ tt('Folder') }}
            </dt><dd class="m-0 break-all text-toned">
              {{ stored.folder }}
            </dd>
            <dt class="text-muted">
              {{ tt('Files') }}
            </dt><dd class="m-0 break-all text-toned">
              {{ stored.files.join(', ') }}
            </dd>
          </dl>
        </template>

        <dl
          v-if="target.row.command"
          class="m-0 grid grid-cols-[110px_1fr] gap-x-3 gap-y-1.5 text-[12px] max-sm:grid-cols-1"
        >
          <dt class="text-muted">
            {{ tt('Source') }}
          </dt><dd class="m-0 text-toned">
            {{ sourceLabel(target.row.command.source) }}
          </dd>
          <template v-if="target.row.command.path && !stored">
            <dt class="text-muted">
              {{ tt('File') }}
            </dt><dd class="m-0 break-all text-toned">
              {{ target.row.command.path }}
            </dd>
          </template>
          <template v-if="target.row.updatedAt">
            <dt class="text-muted">
              {{ tt('Changed') }}
            </dt><dd class="m-0 text-toned">
              {{ shortDate(target.row.updatedAt) }}
            </dd>
          </template>
          <template v-if="commandFacts(target.row.command).length">
            <dt class="text-muted">
              {{ tt('What it may do') }}
            </dt><dd class="m-0 text-toned">
              {{ commandFacts(target.row.command).join(' · ') }}
            </dd>
          </template>
          <template v-if="target.row.command.argumentHint">
            <dt class="text-muted">
              {{ tt('Arguments') }}
            </dt><dd class="m-0 text-toned">
              {{ target.row.command.argumentHint }}
            </dd>
          </template>
          <template v-if="target.row.command.skippedFields.length">
            <dt class="text-muted">
              {{ tt('Ignored') }}
            </dt><dd class="m-0 text-muted">
              {{ target.row.command.skippedFields.join(', ') }}
            </dd>
          </template>
        </dl>
        <p
          v-for="p in target.row.command?.problems ?? []"
          :key="p"
          class="m-0 text-[12px] text-warn"
        >
          {{ p }}
        </p>

        <section
          v-if="skillDetail?.files.length"
          class="flex flex-col gap-1"
        >
          <h3 class="m-0 text-[12px] font-normal text-muted">
            {{ tt('Files') }}
          </h3>
          <p class="m-0 break-all text-[12px] text-toned">
            {{ skillDetail.files.join(', ') }}
          </p>
        </section>
        <section
          v-if="skillDetail?.body"
          class="flex flex-col gap-1"
        >
          <h3 class="m-0 text-[12px] font-normal text-muted">
            {{ tt('Instructions') }}
          </h3>
          <pre class="m-0 max-h-[320px] overflow-auto border border-default p-2.5 text-[12px] break-words whitespace-pre-wrap text-toned">{{ skillDetail.body }}<template v-if="skillDetail.truncated">…</template></pre>
        </section>
      </template>

      <!-- A plugin -->
      <template v-else-if="target.kind === 'plugin'">
        <p
          v-if="target.row.plugin.description"
          class="m-0 text-toned"
        >
          {{ target.row.plugin.description }}
        </p>
        <p
          v-if="target.row.plugin.reason"
          class="m-0 text-[12px] text-muted"
        >
          {{ target.row.plugin.reason }}
        </p>
        <p
          v-if="target.row.plugin.managed"
          class="m-0 border border-l-2 border-default border-l-warn px-2.5 py-1.5 text-toned"
        >
          {{ tt('Set by your administrator: it stays on, and each device still approves what runs.') }}
        </p>
        <section class="flex flex-col gap-1.5">
          <h3 class="m-0 text-[12px] font-normal text-muted">
            {{ tt('What it holds') }}
          </h3>
          <ExtensionsContents :contents="target.row.plugin.contents" />
        </section>
        <dl class="m-0 grid grid-cols-[110px_1fr] gap-x-3 gap-y-1.5 text-[12px] max-sm:grid-cols-1">
          <template v-if="target.row.publisher">
            <dt class="text-muted">
              {{ tt('Publisher') }}
            </dt><dd class="m-0 text-toned">
              {{ target.row.publisher }}
            </dd>
          </template>
          <dt class="text-muted">
            {{ tt('Source') }}
          </dt><dd class="m-0 break-all text-toned">
            {{ target.row.plugin.source || target.row.plugin.marketplace }}
          </dd>
          <template v-if="target.row.plugin.version">
            <dt class="text-muted">
              {{ tt('Version') }}
            </dt><dd class="m-0 text-toned">
              {{ target.row.plugin.version }}<template v-if="target.row.plugin.pinned">
                {{ tt(', pinned') }}
              </template>
            </dd>
          </template>
          <template v-if="target.row.plugin.contentHash">
            <dt class="text-muted">
              {{ tt('Content') }}
            </dt><dd class="m-0 break-all text-toned">
              <template v-if="target.row.plugin.commit">
                {{ tt('commit {0},', [target.row.plugin.commit]) }}
              </template>{{ target.row.plugin.contentHash }}
            </dd>
          </template>
          <template v-if="target.row.license">
            <dt class="text-muted">
              {{ tt('License') }}
            </dt><dd class="m-0 text-toned">
              {{ target.row.license }}
            </dd>
          </template>
          <template v-if="target.row.plugin.suggestedBy">
            <dt class="text-muted">
              {{ tt('Asked for by') }}
            </dt><dd class="m-0 break-all text-toned">
              {{ target.row.plugin.suggestedBy }}
            </dd>
          </template>
          <template v-if="target.row.homepage">
            <dt class="text-muted">
              {{ tt('Homepage') }}
            </dt><dd class="m-0">
              <SettingsLink :href="target.row.homepage">
                {{ target.row.homepage }}
              </SettingsLink>
            </dd>
          </template>
        </dl>
        <p
          v-for="(x, i) in target.row.plugin.problems"
          :key="i"
          class="m-0 text-[12px] text-bad"
        >
          {{ x }}
        </p>
      </template>

      <!-- A connector -->
      <template v-else-if="target.kind === 'connector'">
        <p
          v-if="target.row.statusDetail"
          class="m-0 text-[12px] text-muted"
        >
          {{ target.row.statusDetail }}
        </p>
        <dl class="m-0 grid grid-cols-[110px_1fr] gap-x-3 gap-y-1.5 text-[12px] max-sm:grid-cols-1">
          <dt class="text-muted">
            {{ tt('Type') }}
          </dt><dd class="m-0 text-toned">
            {{ target.row.remote ? tt('Remote') : tt('Local') }} ({{ target.row.transport }})<template v-if="target.row.badges.length">
              · {{ target.row.badges.join(', ') }}
            </template>
          </dd>
          <dt class="text-muted">
            {{ target.row.remote ? 'URL' : tt('Runs') }}
          </dt><dd class="m-0 break-all text-toned">
            <code>{{ target.row.endpoint }}</code>
          </dd>
        </dl>
        <section
          v-if="target.row.providers.length"
          class="flex flex-col gap-1.5"
          data-providers
        >
          <h3 class="m-0 text-[12px] font-normal text-muted">
            {{ providedBy(target.row) }}
          </h3>
          <div
            v-for="p in target.row.providers"
            :key="p.server"
            class="flex items-center gap-2 border-b border-default py-1.5 text-[12px]"
          >
            <span class="min-w-0 flex-1 truncate text-toned">{{ p.server }}</span>
            <HBtn
              size="xs"
              @click="emit('open-plugin', p.pluginId)"
            >
              {{ tt('Open {0}', [p.pluginName]) }}
            </HBtn>
          </div>
          <p
            v-if="!entry"
            class="m-0 text-[12px] text-muted"
          >
            {{ tt('It comes with its plugins: approve or turn it off there.') }}
          </p>
        </section>

        <ExtensionsMcpForm
          v-if="editing && entry"
          :entry="entry"
          :can-manage-org="canManageOrg"
          :api="mcp()"
          @saved="editing = false; emit('changed'); emit('close')"
          @cancel="editing = false"
        />
        <ExtensionsMcpServer
          v-else-if="entry"
          :entry="entry"
          :folder="cwd"
          :api="mcp()"
          :inspection="inspection"
          :inspecting="inspecting"
          @changed="emit('changed'); emit('close')"
          @edit="editing = true"
          @failed="(m: string) => error = m"
        />
      </template>

      <!-- Something to add -->
      <template v-else-if="target.kind === 'discover'">
        <p
          v-if="target.item.refusal"
          class="m-0 border border-l-2 border-default border-l-warn px-2.5 py-1.5 text-toned"
        >
          {{ tt('Set by your administrator: {0}', [target.item.refusal]) }}
        </p>
        <p
          v-if="target.item.problem"
          class="m-0 text-[12px] text-warn"
        >
          {{ target.item.problem }}
        </p>
        <p
          v-if="target.item.description || external?.description"
          class="m-0 text-toned"
        >
          {{ target.item.description || external?.description }}
        </p>
        <section
          v-if="target.item.externalId"
          class="flex flex-col gap-1.5"
        >
          <h3 class="m-0 text-[12px] font-normal text-muted">
            {{ tt('Audits, as the source gives them') }}
          </h3>
          <ExtensionsAudits :audits="external?.audits ?? target.item.audits" />
        </section>
        <section
          v-if="discoverContents(target.item)"
          class="flex flex-col gap-1.5"
        >
          <h3 class="m-0 text-[12px] font-normal text-muted">
            {{ tt('What it holds') }}
          </h3>
          <ExtensionsContents :contents="discoverContents(target.item)!" />
          <p
            v-if="runsPrograms(discoverContents(target.item)!)"
            class="m-0 text-[12px] text-warn"
          >
            {{ tt('It can run programs with your rights, once you approve it.') }}
          </p>
        </section>
        <p
          v-else-if="target.item.pluginId"
          class="m-0 text-[12px] text-muted"
        >
          {{ tt('Its files aren\'t on this device yet, so what it holds is shown after installing, before anything of it runs.') }}
        </p>
        <section
          v-for="o in target.item.options"
          :key="o.label"
          class="flex flex-col gap-1 border border-default p-3"
          data-option
        >
          <div class="flex items-center gap-2">
            <h3 class="m-0 flex-1 text-[12.5px] text-strong">
              {{ o.label }}
            </h3>
            <HBtn
              size="xs"
              :disabled="!!target.item.refusal || target.item.installed"
              @click="emit('add-connector', o)"
            >
              {{ tt('Set up') }}
            </HBtn>
          </div>
          <code class="break-all text-[12px] text-toned">{{ o.remote ? o.server?.remote?.url : [o.server?.stdio?.command, ...(o.server?.stdio?.args ?? [])].join(' ') }}</code>
          <p
            v-for="r in o.required"
            :key="r.name"
            class="m-0 text-[12px] text-muted"
          >
            {{ tt('Needs {0}', [r.name]) }}<template v-if="r.description">
              : {{ r.description }}
            </template>
          </p>
        </section>
        <dl class="m-0 grid grid-cols-[110px_1fr] gap-x-3 gap-y-1.5 text-[12px] max-sm:grid-cols-1">
          <template v-if="target.item.publisher">
            <dt class="text-muted">
              {{ tt('Publisher') }}
            </dt><dd class="m-0 text-toned">
              {{ target.item.publisher }}
            </dd>
          </template>
          <dt class="text-muted">
            {{ tt('Source') }}
          </dt><dd class="m-0 break-all text-toned">
            {{ target.item.sourceLabel }}<template v-if="target.item.externalId">
              : {{ target.item.externalId }}
            </template>
          </dd>
          <template v-if="target.item.version">
            <dt class="text-muted">
              {{ tt('Version') }}
            </dt><dd class="m-0 text-toned">
              {{ target.item.version }}
            </dd>
          </template>
          <template v-if="installsLabel(external?.installs ?? target.item.installs)">
            <dt class="text-muted">
              {{ tt('Installs') }}
            </dt><dd class="m-0 text-toned">
              {{ tt('{0}, as the source counts', [installsLabel(external?.installs ?? target.item.installs)]) }}
            </dd>
          </template>
          <template v-if="external?.license || target.item.license">
            <dt class="text-muted">
              {{ tt('License') }}
            </dt><dd class="m-0 text-toned">
              {{ external?.license || target.item.license }}
            </dd>
          </template>
          <template v-if="target.item.category">
            <dt class="text-muted">
              {{ tt('Category') }}
            </dt><dd class="m-0 text-toned">
              {{ target.item.category }}
            </dd>
          </template>
          <template v-if="target.item.updatedAt">
            <dt class="text-muted">
              {{ tt('Updated') }}
            </dt><dd class="m-0 text-toned">
              {{ shortDate(target.item.updatedAt) }}
            </dd>
          </template>
          <template v-if="external?.hash">
            <dt class="text-muted">
              {{ tt('Would pin') }}
            </dt><dd class="m-0 break-all text-toned">
              {{ external.hash }}
            </dd>
          </template>
          <template v-if="target.item.homepage">
            <dt class="text-muted">
              {{ tt('Homepage') }}
            </dt><dd class="m-0">
              <SettingsLink :href="target.item.homepage">
                {{ target.item.homepage }}
              </SettingsLink>
            </dd>
          </template>
        </dl>
        <section
          v-if="external?.files.length"
          class="flex flex-col gap-1"
        >
          <h3 class="m-0 text-[12px] font-normal text-muted">
            {{ tt('Files') }}
          </h3>
          <div
            v-for="f in external.files"
            :key="f.path"
            class="flex flex-col gap-0.5"
          >
            <span class="break-all text-[12px] text-toned">{{ f.path }}</span>
            <pre
              v-if="f.path === 'SKILL.md'"
              class="m-0 max-h-[280px] overflow-auto border border-default p-2.5 text-[12px] break-words whitespace-pre-wrap text-toned"
            >{{ f.contents }}</pre>
          </div>
        </section>
      </template>

      <!-- A marketplace -->
      <template v-else>
        <p
          v-if="target.market.description"
          class="m-0 text-toned"
        >
          {{ target.market.description }}
        </p>
        <p
          v-if="target.market.refusal || target.market.problem"
          class="m-0 text-[12px] text-warn"
        >
          {{ target.market.refusal || target.market.problem }}
        </p>
        <dl class="m-0 grid grid-cols-[110px_1fr] gap-x-3 gap-y-1.5 text-[12px] max-sm:grid-cols-1">
          <dt class="text-muted">
            {{ tt('Source') }}
          </dt><dd class="m-0 break-all text-toned">
            {{ target.market.builtin ? tt('Ships with this client') : target.market.source }}
          </dd>
          <template v-if="target.market.commit">
            <dt class="text-muted">
              {{ tt('Commit') }}
            </dt><dd class="m-0 break-all text-toned">
              {{ target.market.commit }}
            </dd>
          </template>
          <template v-if="target.market.updatedAt">
            <dt class="text-muted">
              {{ tt('Fetched') }}
            </dt><dd class="m-0 text-toned">
              {{ shortDate(target.market.updatedAt) }}
            </dd>
          </template>
          <dt class="text-muted">
            {{ tt('Plugins') }}
          </dt><dd class="m-0 text-toned">
            {{ tt('{0}, {1} installed', [target.market.pluginCount, target.market.installedCount]) }}
          </dd>
        </dl>
      </template>
    </div>

    <template #actions>
      <template v-if="target.kind === 'skill'">
        <template v-if="stored">
          <HBtn
            v-if="!stored.approved && !stored.changedOnDisk"
            variant="primary"
            size="md"
            :disabled="busy"
            @click="approveExternal(stored, true)"
          >
            {{ tt('Approve on this device') }}
          </HBtn>
          <HBtn
            v-else-if="stored.approved"
            size="md"
            :disabled="busy"
            @click="approveExternal(stored, false)"
          >
            {{ tt('Take back approval') }}
          </HBtn>
        </template>
        <HBtn
          v-if="target.row.command?.switchable && !target.row.blockedByPolicy"
          size="md"
          :disabled="busy || !!target.row.command.shadowedBy"
          @click="toggleSkill(target.row)"
        >
          {{ target.row.command.enabled ? tt('Turn off') : tt('Turn on') }}
        </HBtn>
        <HBtn
          v-if="target.row.pluginId"
          size="md"
          @click="emit('open-plugin', target.row.pluginId)"
        >
          {{ tt('Open its plugin') }}
        </HBtn>
        <HBtn
          v-if="target.row.folder && desktop"
          size="md"
          @click="reveal(target.row.folder)"
        >
          {{ tt('Show in folder') }}
        </HBtn>
        <span class="flex-1" />
        <HBtn
          v-if="stored"
          size="md"
          tone="danger"
          :disabled="busy"
          @click="removeExternal(stored)"
        >
          {{ tt('Remove') }}
        </HBtn>
        <HBtn
          v-else-if="target.row.canRemove"
          size="md"
          tone="danger"
          :disabled="busy"
          @click="removeSkill(target.row)"
        >
          {{ tt('Remove') }}
        </HBtn>
      </template>

      <template v-else-if="target.kind === 'plugin'">
        <HBtn
          v-if="target.row.plugin.status === 'not_installed'"
          variant="primary"
          size="md"
          :disabled="busy"
          @click="pluginChange(target.row, () => plugins().install(target.kind === 'plugin' ? target.row.plugin.id : '', cwd))"
        >
          {{ tt('Install') }}
        </HBtn>
        <template v-else>
          <HBtn
            v-if="!target.row.plugin.approved"
            variant="primary"
            size="md"
            :disabled="busy"
            @click="pluginChange(target.row, () => plugins().setApproval(target.kind === 'plugin' ? target.row.plugin.id : '', true, target.kind === 'plugin' ? target.row.plugin.contentHash : ''), 'Approved on this device.')"
          >
            {{ runsPrograms(target.row.plugin.contents) ? tt('Approve: let these run with my rights') : tt('Approve on this device') }}
          </HBtn>
          <HBtn
            v-else
            size="md"
            :disabled="busy"
            @click="pluginChange(target.row, () => plugins().setApproval(target.kind === 'plugin' ? target.row.plugin.id : '', false, ''))"
          >
            {{ tt('Take back approval') }}
          </HBtn>
          <HBtn
            size="md"
            :disabled="busy || target.row.plugin.managed"
            @click="pluginChange(target.row, () => plugins().setEnabled(target.kind === 'plugin' ? target.row.plugin.id : '', target.kind === 'plugin' && !target.row.plugin.enabled))"
          >
            {{ target.row.plugin.enabled ? tt('Turn off') : tt('Turn on') }}
          </HBtn>
          <HBtn
            size="md"
            :disabled="busy"
            @click="updatePlugin(target.row)"
          >
            {{ tt('Update') }}
          </HBtn>
          <HBtn
            size="md"
            :disabled="busy"
            @click="pluginChange(target.row, () => plugins().setPinned(target.kind === 'plugin' ? target.row.plugin.id : '', target.kind === 'plugin' && !target.row.plugin.pinned))"
          >
            {{ target.row.plugin.pinned ? tt('Unpin') : tt('Pin') }}
          </HBtn>
          <span class="flex-1" />
          <HBtn
            v-if="!target.row.plugin.managed"
            size="md"
            tone="danger"
            :disabled="busy"
            @click="removePlugin(target.row)"
          >
            {{ tt('Remove') }}
          </HBtn>
        </template>
      </template>

      <template v-else-if="target.kind === 'discover'">
        <HBtn
          v-if="target.item.externalId"
          variant="primary"
          size="md"
          :disabled="busy || !external || !external.hash || external.installed"
          @click="installExternal(target.item)"
        >
          {{ external?.installed ? tt('Stored') : tt('Store, pinned to this hash') }}
        </HBtn>
        <HBtn
          v-else-if="target.item.pluginId"
          variant="primary"
          size="md"
          :disabled="busy || target.item.installed || !!target.item.refusal"
          @click="installListing(target.item)"
        >
          {{ target.item.installed ? tt('Installed') : target.item.kind === 1 ? tt('Install the plugin that holds it') : tt('Install') }}
        </HBtn>
      </template>

      <template v-else-if="target.kind === 'marketplace'">
        <HBtn
          v-if="!target.market.builtin"
          size="md"
          :disabled="busy"
          @click="run(() => plugins().updateMarketplace(target.kind === 'marketplace' ? target.market.name : ''), 'Up to date.')"
        >
          {{ tt('Update') }}
        </HBtn>
        <span class="flex-1" />
        <HBtn
          v-if="!target.market.managed && !target.market.builtin"
          size="md"
          tone="danger"
          :disabled="busy"
          @click="removeMarket(target.market)"
        >
          {{ tt('Remove') }}
        </HBtn>
      </template>
    </template>
  </ExtensionsSheet>
</template>
