<script setup lang="ts">
// Settings > Users, under the list: groups and the quotas per group and per role, and the budget of one session. Local groups
// are edited here; those from SCIM, an identity provider or LDAP follow their source, but can carry a quota all the same.
import type { GroupView, QuotaLimits, Quotas } from '~/lib/enterprise/types'
import { ROLE_LABEL } from '~/lib/enterprise/types'
import type { UserRole, UserView } from '~/lib/users/types'

const props = defineProps<{ users: UserView[] }>()
const emit = defineEmits<{ changed: [] }>()
const store = useSessionStore()
const access = useAccess()

const groups = ref<GroupView[]>([])
const quotas = ref<Quotas>({ users: {}, groups: {}, roles: {} })
const error = ref('')
const newName = ref('')
const open = ref('')
const confirming = ref('')
const sessionBudget = ref('')

const canGroups = computed(() => access.can('directory.write'))
const canQuotas = computed(() => access.can('quotas.write'))

function api() {
  return store.transport!.enterprise()
}

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function run(fn: () => Promise<unknown>) {
  try {
    await fn()
    error.value = ''
    return true
  } catch (e) {
    error.value = message(e)
    return false
  }
}

async function load() {
  await run(async () => {
    const [g, q] = await Promise.all([api().listGroups(), api().getQuotas()])
    groups.value = g
    quotas.value = q
    sessionBudget.value = q.sessionUsd === undefined ? '' : String(q.sessionUsd)
  })
}
onMounted(load)

async function create() {
  if (!newName.value.trim()) return
  if (await run(() => api().createGroup(newName.value))) {
    newName.value = ''
    await load()
  }
}

async function remove(g: GroupView) {
  if (confirming.value !== g.id) {
    confirming.value = g.id
    return
  }
  confirming.value = ''
  if (await run(() => api().deleteGroup(g.id))) await load()
}

async function toggleMember(g: GroupView, id: string) {
  const next = g.memberIds.includes(id) ? g.memberIds.filter(x => x !== id) : [...g.memberIds, id]
  if (await run(() => api().setGroupMembers(g.id, next))) {
    await load()
    emit('changed')
  }
}

async function saveGroup(g: GroupView, l: QuotaLimits) {
  if (await run(() => api().setGroupQuota(g.id, l))) await load()
}

async function saveRole(r: UserRole, l: QuotaLimits) {
  if (await run(() => api().setRoleQuota(r, l))) await load()
}

async function saveBudget() {
  const t = sessionBudget.value.trim()
  const n = t ? Number(t) : undefined
  if (n !== undefined && !Number.isFinite(n)) {
    error.value = `Not an amount: ${t}`
    return
  }
  if (await run(() => api().setSessionBudget(n))) await load()
}

const SOURCE: Record<string, string> = { scim: 'SCIM', sso: 'identity provider', ldap: 'LDAP' }
const ROLES: UserRole[] = ['user', 'admin', 'auditor', 'billing']
const memberNames = (g: GroupView) => g.memberIds.map(id => props.users.find(u => u.id === id)?.name ?? id)
</script>

<template>
  <section
    :aria-label="tt('Groups and quotas')"
    class="mt-8 flex flex-col"
  >
    <h2 class="set-h">
      {{ tt('Groups and quotas') }}
    </h2>
    <p class="set-sub">
      {{ tt('Spend limits the organization sets, in USD per UTC day and calendar month. For a user, their own limit applies, else the strictest of their groups\', else their role\'s. Spend on someone\'s personal providers counts towards none of these, and their own provider limits stay visible only to them.') }}
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>

    <h3 class="m-0 mb-2 text-[12.5px] font-semibold text-strong">
      {{ tt('Per role') }}
    </h3>
    <div class="mb-4 flex flex-col gap-2 border border-default px-3 py-3">
      <div
        v-for="r in ROLES"
        :key="r"
        class="flex flex-wrap items-center gap-3"
      >
        <span class="w-[70px] text-[12px] text-toned">{{ ROLE_LABEL[r] }}</span>
        <SettingsQuotaForm
          :limits="quotas.roles[r] ?? {}"
          :name="ROLE_LABEL[r]"
          :readonly="!canQuotas"
          @save="saveRole(r, $event)"
        />
      </div>
      <form
        class="flex flex-wrap items-center gap-3 border-t border-default pt-2 text-[11.5px] text-muted"
        @submit.prevent="saveBudget"
      >
        <span class="w-[70px] text-[12px] text-toned">{{ tt('Session') }}</span>
        <label class="flex items-center gap-1.5">{{ tt('One session $') }}
          <input
            v-model="sessionBudget"
            inputmode="decimal"
            placeholder="no limit"
            :aria-label="tt('Budget of one session in USD')"
            :disabled="!canQuotas"
            class="h-7 w-[92px] border border-accented bg-default px-2 text-[12.5px] text-strong outline-none focus:border-accent disabled:opacity-60"
          >
        </label>
        <HBtn
          v-if="canQuotas"
          size="sm"
          type="submit"
        >
          {{ tt('Save') }}
        </HBtn>
      </form>
    </div>

    <h3 class="m-0 mb-2 text-[12.5px] font-semibold text-strong">
      {{ tt('Groups') }}
    </h3>
    <form
      v-if="canGroups"
      class="mb-2 flex items-center gap-2"
      @submit.prevent="create"
    >
      <input
        v-model="newName"
        :placeholder="tt('New group')"
        :aria-label="tt('New group name')"
        class="h-8 max-w-[260px] flex-1 border border-accented bg-default px-2 text-[12.5px] text-strong outline-none focus:border-accent"
      >
      <HBtn
        size="sm"
        type="submit"
        :disabled="!newName.trim()"
      >
        {{ tt('Add group') }}
      </HBtn>
    </form>

    <div
      v-if="groups.length"
      class="overflow-hidden border border-default"
    >
      <div
        v-for="g in groups"
        :key="g.id"
        class="flex flex-col border-b border-default last:border-b-0"
      >
        <button
          type="button"
          class="flex items-center gap-2 rounded-none px-3 py-2 text-left text-[12.5px] hover:bg-hover"
          :aria-expanded="open === g.id"
          @click="open = open === g.id ? '' : g.id"
        >
          <span class="min-w-0 flex-1 truncate text-strong">{{ g.name }}</span>
          <span
            v-if="SOURCE[g.source]"
            class="border border-default px-1 text-[10.5px] text-muted"
          >{{ SOURCE[g.source] }}</span>
          <span class="text-[11.5px] text-dimmed">{{ g.memberIds.length }} {{ g.memberIds.length === 1 ? 'member' : 'members' }}</span>
          <HIcon
            :name="open === g.id ? 'up' : 'down'"
            :size="12"
            class="text-muted"
          />
        </button>
        <div
          v-if="open === g.id"
          class="flex flex-col gap-3 border-t border-default px-3 py-3 text-[12px]"
        >
          <SettingsQuotaForm
            :limits="quotas.groups[g.id] ?? {}"
            :name="g.name"
            :readonly="!canQuotas"
            @save="saveGroup(g, $event)"
          />
          <div
            v-if="g.source === 'local' && canGroups"
            class="flex flex-col gap-1"
          >
            <span class="text-[11.5px] text-muted">{{ tt('Members') }}</span>
            <label
              v-for="u in users"
              :key="u.id"
              class="flex items-center gap-2 text-[12px] text-toned"
            >
              <input
                type="checkbox"
                :checked="g.memberIds.includes(u.id)"
                @change="toggleMember(g, u.id)"
              >{{ u.name }} <span class="text-dimmed">{{ u.email }}</span>
            </label>
          </div>
          <p
            v-else
            class="m-0 text-[11.5px] text-muted"
          >
            {{ memberNames(g).join(', ') || tt('No members.') }}<template v-if="g.source !== 'local'">
              {{ tt('Membership comes from {0}.', [SOURCE[g.source] ?? g.source]) }}
            </template>
          </p>
          <div v-if="g.source === 'local' && canGroups">
            <HBtn
              size="xs"
              tone="danger"
              @click="remove(g)"
            >
              {{ confirming === g.id ? tt('Confirm delete {0}', [g.name]) : tt('Delete group') }}
            </HBtn>
          </div>
        </div>
      </div>
    </div>
    <p
      v-else
      class="m-0 border border-default px-3 py-4 text-center text-[12.5px] text-muted"
    >
      {{ tt('No groups yet.') }}
    </p>
  </section>
</template>
