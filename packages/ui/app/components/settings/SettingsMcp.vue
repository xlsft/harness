<script setup lang="ts">
// Settings > MCP: every server this device can use, grouped by who manages it. The client decides what each server
// needs (approval, sign-in, a trusted folder); this screen shows it and passes on the user's decisions. Connecting to a
// server to see its tools is the client's work too: each ready server is inspected when the list loads.
import { McpServerSource, McpServerState, SOURCE_GROUPS, type InspectMcpServerResponse, type ListMcpServersResponse, type McpApi, type McpServerEntry } from '~/lib/mcp/types'

const store = useSessionStore()
const list = ref<ListMcpServersResponse | null>(null)
const loading = ref(true)
const error = ref('')
const inspected = reactive<Record<string, InspectMcpServerResponse>>({})
const inspecting = reactive<Record<string, boolean>>({})
const adding = ref(false)
const editing = ref<McpServerEntry | null>(null)
const folder = ref('')
const folders = ref<{ value: string, label: string }[]>([])

function api(): McpApi {
  if (!store.transport) throw new Error('not connected to the client')
  return store.transport.mcp()
}
function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function inspect(e: McpServerEntry) {
  const id = e.server?.id
  if (!id || e.state !== McpServerState.READY) return
  inspecting[id] = true
  try {
    inspected[id] = await api().inspect(id, folder.value)
  } catch (err) {
    error.value = message(err)
  } finally {
    Reflect.deleteProperty(inspecting, id)
  }
}

async function load() {
  try {
    list.value = await api().list(folder.value)
    error.value = ''
    // Not awaited: a slow server does not hold up the list; each row fills in when its answer arrives.
    for (const e of list.value.servers) void inspect(e)
  } catch (e) {
    error.value = message(e)
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  try {
    const recent = await store.transport?.listRecentFolders()
    folders.value = (recent ?? []).map(f => ({ value: f.path, label: f.path }))
    folder.value = folders.value[0]?.value ?? ''
  } catch {
    // No folders known: the project group just stays empty.
  }
  await load()
})
watch(folder, () => load())

const groups = computed(() => SOURCE_GROUPS.map(g => ({ ...g, entries: (list.value?.servers ?? []).filter(e => e.source === g.source) })))
const projectGroup = McpServerSource.PROJECT

async function act(fn: () => Promise<unknown>) {
  try {
    await fn()
    error.value = ''
  } catch (e) {
    error.value = message(e)
  }
  await load()
}

async function trust() {
  await act(() => api().setTrust(folder.value, true))
}

function saved() {
  adding.value = false
  editing.value = null
  void load()
}
</script>

<template>
  <section
    :aria-label="tt('MCP servers')"
    class="flex flex-col"
  >
    <div class="mb-1 flex items-center gap-2">
      <h2 class="set-h m-0! flex-1">
        {{ tt('MCP servers') }}
      </h2>
      <HBtn
        size="sm"
        variant="primary"
        @click="adding = true; editing = null"
      >
        {{ tt('Add server') }}
      </HBtn>
    </div>
    <p class="set-sub">
      {{ tt('Servers give the agent tools, prompts and data. Changes apply to new sessions; rules on tools too.') }}
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <p
      v-if="list?.registryError"
      class="m-0 mb-3 border border-default px-3 py-2 text-[12px] text-muted"
    >
      {{ tt('Servers from your account and your admin cannot be loaded: {0}', [list.registryError]) }}
    </p>

    <SettingsMcpForm
      v-if="adding || editing"
      :entry="editing"
      :can-manage-org="list?.canManageOrg ?? false"
      :api="api()"
      class="mb-4"
      @saved="saved"
      @cancel="adding = false; editing = null"
    />

    <p
      v-if="loading"
      class="m-0 py-2 text-[12.5px] text-muted"
    >
      {{ tt('Loading…') }}
    </p>

    <template v-else>
      <div
        v-for="g in groups"
        :key="g.source"
        class="mb-5 flex flex-col"
      >
        <h3 class="m-0 text-[13px] font-semibold text-strong">
          {{ g.title }}
        </h3>
        <p class="m-0 mb-2 text-[12px] text-muted">
          {{ g.note }}
        </p>

        <div
          v-if="g.source === projectGroup"
          class="mb-2 flex items-center gap-2"
        >
          <span class="text-[12px] text-muted">{{ tt('Folder') }}</span>
          <HSelect
            v-model="folder"
            :options="folders"
            :aria-label="tt('Folder for project servers')"
            :placeholder="tt('No recent folders')"
            class="min-w-0 flex-1"
          />
          <HBtn
            v-if="list?.projectHasFile && !list?.projectTrusted"
            size="sm"
            @click="trust"
          >
            {{ tt('Trust this folder') }}
          </HBtn>
        </div>
        <p
          v-if="g.source === projectGroup && list?.projectError"
          class="m-0 mb-2 text-[12px] text-bad"
        >
          {{ list.projectError }}
        </p>

        <p
          v-if="!g.entries.length"
          class="m-0 border border-default px-3 py-2.5 text-[12px] text-muted"
        >
          {{ g.source === projectGroup && !folder ? tt('Open a folder first.') : tt('None.') }}
        </p>
        <div
          v-else
          class="overflow-hidden border border-default"
        >
          <SettingsMcpServer
            v-for="(e, i) in g.entries"
            :key="e.server?.id || e.server?.name"
            :entry="e"
            :folder="folder"
            :api="api()"
            :inspection="e.server?.id ? inspected[e.server.id] : undefined"
            :inspecting="!!(e.server?.id && inspecting[e.server.id])"
            :class="i && 'border-t border-default'"
            @changed="load"
            @edit="editing = e; adding = false"
            @failed="(m: string) => error = m"
          />
        </div>
      </div>
    </template>

    <SettingsMcpAudit
      v-if="list?.canManageOrg"
      :api="api()"
    />
  </section>
</template>
