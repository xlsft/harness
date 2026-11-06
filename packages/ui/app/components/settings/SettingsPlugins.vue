<script setup lang="ts">
// Settings > Plugins: what this device has installed and what its marketplaces offer. The client keeps the store, decides what may
// run and enforces the organization's policy; this screen shows that and sends intent. Approving sends the hash of what is shown
// here, so content that changed meanwhile isn't approved unseen. Nothing here syncs.
import type { PluginsState, PluginVM } from '~/lib/plugins/types'
import { runsPrograms, statusLabel } from '~/lib/plugins/types'

const store = useSessionStore()
const state = ref<PluginsState | null>(null)
const error = ref('')
const note = ref('')
const busy = ref('')
const spec = ref('')
const source = ref('')
const open = ref('')

const field = 'min-w-0 flex-1 border border-accented bg-default px-2 py-1 font-mono text-[12px] text-strong outline-none focus:border-accent'

const cwd = computed(() => store.current?.meta?.cwd ?? '')

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

function api() {
  return store.transport!.plugins()
}

async function refresh() {
  if (store.transport) state.value = await api().list(cwd.value)
}

onMounted(async () => {
  try {
    await refresh()
  } catch (e) {
    error.value = message(e)
  }
})

// Runs one change and reads the list again.
async function act(key: string, f: () => Promise<unknown>) {
  busy.value = key
  note.value = ''
  try {
    const said = await f()
    note.value = typeof said === 'string' ? said : ''
    error.value = ''
    await refresh()
  } catch (e) {
    error.value = message(e)
  } finally {
    busy.value = ''
  }
}

function install(id: string) {
  return act(`install:${id}`, async () => {
    const p = await api().install(id, cwd.value)
    open.value = p.id
    spec.value = ''
    return `Installed ${p.id}. Nothing of it runs until you approve it.`
  })
}

function update(p: PluginVM) {
  return act(`update:${p.id}`, async () => {
    const res = await api().update(p.id, cwd.value)
    if (res.changed) {
      open.value = p.id
      return `${p.id} is now ${res.plugin?.version}. Read what it holds and approve it again.`
    }
    return res.note
  })
}

function remove(p: PluginVM) {
  if (!window.confirm(`Remove ${p.id} and its data folder?`)) return
  return act(`remove:${p.id}`, () => api().remove(p.id, false))
}

function removeMarketplace(name: string) {
  if (!window.confirm(`Remove the marketplace ${name} and the plugins installed from it?`)) return
  return act(`market:${name}`, () => api().removeMarketplace(name))
}

function statusClass(p: PluginVM) {
  if (p.status === 'active') return 'text-ok'
  return p.status === 'disabled' || p.status === 'not_installed' ? 'text-muted' : 'text-warn'
}

const installable = computed(() => (state.value?.marketplaces ?? []).flatMap(m => m.plugins.filter(l => !l.installed).map(l => ({ ...l, marketplace: m.name }))))
</script>

<template>
  <section
    aria-label="Plugins"
    class="flex flex-col"
  >
    <h2 class="set-h">
      Plugins
    </h2>
    <p class="set-sub">
      Plugins bundle skills, commands, agents, output styles, hooks and MCP servers, in the format of <code>.claude-plugin/plugin.json</code> or <code>.harness-plugin/plugin.json</code>. They stay on this device, each pinned to the content it was installed with. Nothing of one runs until you approve exactly that content here, and only in folders you trust. Hooks and MCP servers run with your rights, outside the sandbox.
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <p
      v-if="note"
      role="status"
      class="m-0 mb-3 text-[12px] text-muted"
    >
      {{ note }}
    </p>
    <p
      v-if="state?.sourcesRestricted"
      class="m-0 mb-3 text-[12px] text-muted"
    >
      Your organization allows plugins only from the sources it lists.
    </p>

    <h3 class="mt-2 mb-1 text-[12.5px] text-strong">
      Installed
    </h3>
    <p
      v-if="state && !state.plugins.length"
      class="m-0 text-[12px] text-muted"
    >
      No plugins are installed.
    </p>
    <ul class="m-0 flex list-none flex-col p-0">
      <li
        v-for="p in state?.plugins ?? []"
        :key="p.id"
        class="flex flex-col"
        data-plugin-row
      >
        <div class="set-row">
          <div class="set-label min-w-0">
            <span>{{ p.displayName || p.name }} <span class="text-muted">{{ p.id }}<template v-if="p.version"> · {{ p.version }}</template></span></span>
            <span
              v-if="p.description"
              class="set-desc"
            >{{ p.description }}</span>
            <span
              v-if="p.reason"
              class="set-desc"
            >{{ p.reason }}</span>
            <span
              v-if="p.managed"
              class="set-desc"
            >From your organization.</span>
            <span
              v-if="p.suggestedBy"
              class="set-desc break-all"
            >Asked for by {{ p.suggestedBy }}.</span>
          </div>
          <div class="flex shrink-0 flex-wrap items-center justify-end gap-2 text-[12px]">
            <span :class="statusClass(p)">{{ statusLabel(p.status) }}</span>
            <HBtn
              size="sm"
              :aria-expanded="open === p.id"
              @click="open = open === p.id ? '' : p.id"
            >
              {{ open === p.id ? 'Hide' : 'Details' }}
            </HBtn>
            <HBtn
              v-if="p.status === 'not_installed'"
              size="sm"
              :disabled="!!busy"
              @click="install(p.id)"
            >
              Install
            </HBtn>
            <HToggle
              v-if="p.status !== 'not_installed'"
              :model-value="p.enabled"
              :label="`Enable ${p.id}`"
              :disabled="!!busy || p.managed"
              @update:model-value="(v: boolean) => act(`enable:${p.id}`, () => api().setEnabled(p.id, v))"
            />
          </div>
        </div>
        <div
          v-if="open === p.id"
          class="mb-3 flex flex-col gap-1 border border-default px-3 py-2 text-[12px]"
          data-plugin-details
        >
          <template v-if="p.status !== 'not_installed'">
            <span class="break-all text-muted">Pinned to {{ p.commit ? `commit ${p.commit}, ` : '' }}{{ p.contentHash }}</span>
            <span v-if="p.contents.skills.length">Skills: {{ p.contents.skills.join(', ') }}</span>
            <span v-if="p.contents.commands.length">Commands: {{ p.contents.commands.join(', ') }}</span>
            <span v-if="p.contents.agents.length">Agents: {{ p.contents.agents.join(', ') }}</span>
            <span v-if="p.contents.outputStyles.length">Output styles: {{ p.contents.outputStyles.join(', ') }}</span>
            <span
              v-if="p.contents.allowedTools.length"
              class="text-warn"
            >Tools its skills may use without asking: {{ p.contents.allowedTools.join(', ') }}</span>
            <span
              v-for="(h, i) in p.contents.hooks"
              :key="`h${i}`"
              class="break-all text-warn"
            >Runs on {{ h.event }}<template v-if="h.matcher"> ({{ h.matcher }})</template><template v-if="h.fromSkill">, while {{ h.fromSkill }} runs</template>: <code>{{ h.command }}</code></span>
            <span
              v-for="m in p.contents.mcpServers"
              :key="m.name"
              class="break-all text-warn"
            >MCP server {{ m.name }} ({{ m.transport }}): <code>{{ m.command }}</code></span>
            <span
              v-if="p.contents.skipped.length"
              class="text-muted"
            >Not acted on by this client: {{ p.contents.skipped.join(', ') }}</span>
            <span
              v-for="(x, i) in p.problems"
              :key="`p${i}`"
              class="text-bad"
            >{{ x }}</span>
            <div class="mt-1 flex flex-wrap gap-2">
              <HBtn
                v-if="!p.approved"
                size="sm"
                :disabled="!!busy"
                @click="act(`approve:${p.id}`, () => api().setApproval(p.id, true, p.contentHash))"
              >
                {{ runsPrograms(p.contents) ? 'Approve: let these run with my rights' : 'Approve on this device' }}
              </HBtn>
              <HBtn
                v-else
                size="sm"
                :disabled="!!busy"
                @click="act(`revoke:${p.id}`, () => api().setApproval(p.id, false, ''))"
              >
                Take back approval
              </HBtn>
              <HBtn
                size="sm"
                :disabled="!!busy"
                @click="update(p)"
              >
                Update
              </HBtn>
              <HBtn
                size="sm"
                :disabled="!!busy"
                @click="act(`pin:${p.id}`, () => api().setPinned(p.id, !p.pinned))"
              >
                {{ p.pinned ? 'Unpin' : 'Pin this version' }}
              </HBtn>
              <HBtn
                v-if="!p.managed"
                size="sm"
                :disabled="!!busy"
                @click="remove(p)"
              >
                Remove
              </HBtn>
            </div>
          </template>
        </div>
      </li>
    </ul>

    <h3 class="mt-5 mb-1 text-[12.5px] text-strong">
      Install
    </h3>
    <form
      class="flex gap-2"
      @submit.prevent="install(spec)"
    >
      <input
        v-model="spec"
        :class="field"
        aria-label="Plugin to install"
        spellcheck="false"
        placeholder="name@marketplace, a folder or a repository"
      >
      <HBtn
        size="sm"
        type="submit"
        :disabled="!spec.trim() || !!busy"
      >
        Install
      </HBtn>
    </form>
    <ul
      v-if="installable.length"
      class="m-0 mt-2 flex list-none flex-col p-0"
    >
      <li
        v-for="l in installable"
        :key="l.id"
        class="set-row"
      >
        <div class="set-label min-w-0">
          <span>{{ l.name }} <span class="text-muted">{{ l.marketplace }}<template v-if="l.version"> · {{ l.version }}</template></span></span>
          <span
            v-if="l.description"
            class="set-desc"
          >{{ l.description }}</span>
        </div>
        <HBtn
          size="sm"
          :disabled="!!busy"
          @click="install(l.id)"
        >
          Install
        </HBtn>
      </li>
    </ul>

    <h3 class="mt-5 mb-1 text-[12.5px] text-strong">
      Marketplaces
    </h3>
    <form
      class="flex gap-2"
      @submit.prevent="act('market', async () => { const n = await api().addMarketplace(source); source = ''; return `Added ${n}. Nothing was installed.` })"
    >
      <input
        v-model="source"
        :class="field"
        aria-label="Marketplace to add"
        spellcheck="false"
        placeholder="owner/repo, a git URL, a URL of a marketplace.json, or a folder"
      >
      <HBtn
        size="sm"
        type="submit"
        :disabled="!source.trim() || !!busy"
      >
        Add
      </HBtn>
    </form>
    <ul class="m-0 mt-2 flex list-none flex-col p-0">
      <li
        v-for="(src, name) in state?.suggestedMarketplaces ?? {}"
        :key="name"
        class="set-row"
      >
        <div class="set-label min-w-0">
          <span>{{ name }}</span>
          <span class="set-desc break-all">This project asks for it: {{ src }}</span>
        </div>
        <HBtn
          size="sm"
          :disabled="!!busy"
          @click="act(`market:${name}`, async () => { await api().addMarketplace(src) })"
        >
          Add
        </HBtn>
      </li>
      <li
        v-for="m in state?.marketplaces ?? []"
        :key="m.name"
        class="set-row"
      >
        <div class="set-label min-w-0">
          <span>{{ m.name }}<template v-if="m.managed"> <span class="text-muted">from your organization</span></template></span>
          <span class="set-desc break-all">{{ m.source }}<template v-if="m.commit"> · {{ m.commit.slice(0, 12) }}</template></span>
          <span
            v-if="m.refusal || m.problem"
            class="set-desc text-warn"
          >{{ m.refusal || m.problem }}</span>
        </div>
        <div class="flex shrink-0 gap-2">
          <HBtn
            size="sm"
            :disabled="!!busy"
            @click="act(`market:${m.name}`, () => api().updateMarketplace(m.name))"
          >
            Update
          </HBtn>
          <HBtn
            v-if="!m.managed"
            size="sm"
            :disabled="!!busy"
            @click="removeMarketplace(m.name)"
          >
            Remove
          </HBtn>
        </div>
      </li>
    </ul>
  </section>
</template>
