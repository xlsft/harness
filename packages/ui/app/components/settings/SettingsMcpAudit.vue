<script setup lang="ts">
// The MCP audit log, for admins (the server refuses everyone else): who used which server when, and how it went. It
// holds no secret values and no tool arguments.
import { AUDIT_ACTIONS, type AuditEntry, type McpApi } from '~/lib/mcp/types'

const props = defineProps<{ api: McpApi }>()

const entries = ref<AuditEntry[]>([])
const cursor = ref('')
const action = ref('all')
const error = ref('')
const loading = ref(false)
const open = ref(false)

const actions = computed(() => [{ value: 'all', label: tt('Everything') }, ...Object.entries(AUDIT_ACTIONS).map(([value, label]) => ({ value, label }))])

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function load(more = false) {
  loading.value = true
  try {
    const r = await props.api.audit({ action: action.value === 'all' ? '' : action.value, cursor: more ? cursor.value : '', limit: 50 })
    entries.value = more ? [...entries.value, ...r.entries] : r.entries
    cursor.value = r.nextCursor
    error.value = ''
  } catch (e) {
    error.value = message(e)
  } finally {
    loading.value = false
  }
}

watch(action, () => load())
watch(open, (v) => {
  if (v) void load()
})

const when = (at: string) => new Date(at).toLocaleString()
</script>

<template>
  <section
    :aria-label="tt('MCP audit log')"
    class="flex flex-col"
  >
    <div class="mb-1 flex items-center gap-2">
      <h3 class="m-0 flex-1 text-[13px] font-semibold text-strong">
        {{ tt('Audit log') }}
      </h3>
      <HBtn
        size="sm"
        :aria-expanded="open"
        @click="open = !open"
      >
        {{ open ? tt('Hide') : tt('Show') }}
      </HBtn>
    </div>
    <p class="m-0 mb-2 text-[12px] text-muted">
      {{ tt('Calls through the server, local server starts and sign-ins, for everyone. No secrets, no tool arguments. Servers added on a device are not in it: they never reach the server.') }}
    </p>
    <template v-if="open">
      <div class="mb-2 flex items-center gap-2">
        <HSelect
          v-model="action"
          :options="actions"
          :aria-label="tt('Filter by action')"
          class="w-[220px]"
        />
      </div>
      <p
        v-if="error"
        role="alert"
        class="m-0 mb-2 border border-bad px-3 py-2 text-[12px] text-bad"
      >
        {{ error }}
      </p>
      <p
        v-if="!entries.length && !loading && !error"
        class="m-0 border border-default px-3 py-2.5 text-[12px] text-muted"
      >
        {{ tt('Nothing yet.') }}
      </p>
      <table
        v-if="entries.length"
        class="w-full border border-default text-left text-[12px]"
      >
        <thead class="text-muted">
          <tr>
            <th class="px-2 py-1 font-normal">
              {{ tt('When') }}
            </th>
            <th class="px-2 py-1 font-normal">
              {{ tt('Who') }}
            </th>
            <th class="px-2 py-1 font-normal">
              {{ tt('Server') }}
            </th>
            <th class="px-2 py-1 font-normal">
              {{ tt('What') }}
            </th>
            <th class="px-2 py-1 font-normal">
              {{ tt('Result') }}
            </th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="e in entries"
            :key="e.id"
            class="border-t border-default text-strong"
          >
            <td class="px-2 py-1 whitespace-nowrap">
              {{ when(e.createdAt) }}
            </td>
            <td class="px-2 py-1">
              {{ e.userName || e.userId }}
            </td>
            <td class="px-2 py-1">
              {{ e.serverName || e.serverId }}
            </td>
            <td class="px-2 py-1">
              {{ AUDIT_ACTIONS[e.action] ?? e.action }}<template v-if="e.tool">
                · {{ e.tool }}
              </template>
            </td>
            <td
              class="px-2 py-1"
              :class="e.isError ? 'text-bad' : 'text-muted'"
            >
              <template v-if="e.isError">
                {{ e.errorMessage || tt('Failed') }}
              </template>
              <template v-else-if="e.durationMs">
                {{ tt('{0} ms', [e.durationMs]) }}
              </template>
            </td>
          </tr>
        </tbody>
      </table>
      <HBtn
        v-if="cursor"
        size="sm"
        class="mt-2 self-start"
        :disabled="loading"
        @click="load(true)"
      >
        {{ tt('More') }}
      </HBtn>
    </template>
  </section>
</template>
