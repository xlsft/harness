<script setup lang="ts">
// Roles (docs/design/roles.md): the built-ins and the organization's custom roles, each a set of permissions from the
// server's fixed catalog. Built-ins are shown read-only; a custom role can be changed by whoever holds roles.write and
// every permission it grants. The server enforces all of it; this only offers what it would accept.
import type { PermissionView, RoleView } from '~/lib/enterprise/types'

const store = useSessionStore()
const access = useAccess()
const shared = useRoles()
const api = computed(() => store.transport?.enterprise())

const roles = ref<RoleView[]>([])
const permissions = ref<PermissionView[]>([])
const error = ref('')
const loading = ref(true)
const busy = ref(false)
const open = ref('')
const confirming = ref('')

// The role being edited: '' is a new one.
const draft = reactive({ id: '', name: '', description: '', permissions: [] as string[] })

const canWrite = computed(() => access.can('roles.write'))
const message = (err: unknown) => (err instanceof Error ? err.message : String(err))

const areas = computed(() => {
  const out: { area: string, label: string, permissions: PermissionView[] }[] = []
  for (const p of permissions.value) {
    const a = out.find(x => x.area === p.area) ?? out[out.push({ area: p.area, label: p.areaLabel, permissions: [] }) - 1]!
    a.permissions.push(p)
  }
  return out
})

async function load() {
  const a = api.value
  if (!a) return
  try {
    const [r, p] = await Promise.all([a.listRoles(), a.listPermissions()])
    roles.value = r
    permissions.value = p
    error.value = ''
  } catch (err) {
    error.value = message(err)
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  await access.load()
  await load()
})

function edit(r?: RoleView) {
  confirming.value = ''
  if (r && open.value === r.id) {
    open.value = ''
    return
  }
  open.value = r?.id ?? 'new'
  draft.id = r?.id ?? ''
  draft.name = r?.name ?? ''
  draft.description = r?.description ?? ''
  draft.permissions = [...(r?.permissions ?? [])]
}

function toggle(id: string) {
  draft.permissions = draft.permissions.includes(id) ? draft.permissions.filter(p => p !== id) : [...draft.permissions, id]
}

// The caller can only grant what they hold themselves.
const grantable = (id: string) => access.can(id)

async function run(fn: () => Promise<unknown>) {
  busy.value = true
  try {
    await fn()
    error.value = ''
    await load()
    await shared.load(true)
  } catch (err) {
    error.value = message(err)
  } finally {
    busy.value = false
  }
}

function save() {
  const input = { name: draft.name, description: draft.description, permissions: draft.permissions }
  void run(async () => {
    const saved = draft.id ? await api.value!.updateRole(draft.id, input) : await api.value!.createRole(input)
    open.value = saved.id
    draft.id = saved.id
  })
}

function remove(r: RoleView) {
  if (confirming.value !== r.id) {
    confirming.value = r.id
    return
  }
  void run(async () => {
    await api.value!.deleteRole(r.id, true)
    open.value = ''
    confirming.value = ''
  })
}

// A new role shows as a last row until it is created.
const rows = computed<RoleView[]>(() => open.value === 'new'
  ? [...roles.value, { id: 'new', name: tt('New role'), description: '', builtin: false, editable: true, permissions: [], memberCount: 0 }]
  : roles.value)

const has = (r: RoleView, id: string) => r.permissions.includes(id)
</script>

<template>
  <section
    :aria-label="tt('Roles')"
    class="flex flex-col"
  >
    <h2 class="set-h">
      {{ tt('Roles') }}
    </h2>
    <p class="set-sub">
      {{ tt('A role is a set of administration permissions. Everyone uses the product the same way; roles decide who administers the server. Built-in roles cannot be changed. You can grant only permissions you hold yourself.') }}
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>

    <div
      v-if="canWrite"
      class="mb-2"
    >
      <HBtn
        size="sm"
        :disabled="busy"
        @click="edit()"
      >
        {{ tt('New role') }}
      </HBtn>
    </div>

    <p
      v-if="loading"
      class="m-0 text-[12px] text-muted"
    >
      {{ tt('Loading…') }}
    </p>
    <div
      v-else
      class="overflow-hidden border border-default"
    >
      <div
        v-for="r in rows"
        :key="r.id"
        class="flex flex-col border-b border-default last:border-b-0"
      >
        <button
          type="button"
          class="flex items-center gap-2 rounded-none px-3 py-2 text-left text-[12.5px] hover:bg-hover"
          :aria-expanded="open === r.id"
          @click="r.id === 'new' ? (open = '') : edit(r)"
        >
          <span class="min-w-0 flex-1 truncate text-strong">{{ r.name }}</span>
          <span
            v-if="r.builtin"
            class="border border-default px-1 text-[10.5px] text-muted"
          >{{ tt('Built-in') }}</span>
          <span
            v-if="r.id !== 'new'"
            class="text-[11.5px] text-dimmed"
          >{{ tt('{0} permissions', [r.permissions.length]) }} · {{ r.memberCount }} {{ r.memberCount === 1 ? tt('member') : tt('members') }}</span>
          <HIcon
            :name="open === r.id ? 'up' : 'down'"
            :size="12"
            class="text-muted"
          />
        </button>

        <div
          v-if="open === r.id"
          class="flex flex-col gap-3 border-t border-default px-3 py-3 text-[12px]"
        >
          <template v-if="r.editable && canWrite">
            <label class="flex flex-col gap-1 text-[11.5px] text-muted">{{ tt('Name') }}
              <input
                v-model="draft.name"
                :aria-label="tt('Role name')"
                class="h-8 max-w-[320px] border border-accented bg-default px-2 text-[12.5px] text-strong outline-none focus:border-accent"
              >
            </label>
            <label class="flex flex-col gap-1 text-[11.5px] text-muted">{{ tt('Description') }}
              <input
                v-model="draft.description"
                :aria-label="tt('Role description')"
                class="h-8 border border-accented bg-default px-2 text-[12.5px] text-strong outline-none focus:border-accent"
              >
            </label>
          </template>
          <p
            v-else-if="r.description"
            class="m-0 text-toned"
          >
            {{ r.description }}
          </p>

          <div
            v-for="a in areas"
            :key="a.area"
            class="flex flex-col gap-1"
          >
            <span class="text-[11.5px] font-semibold text-strong">{{ a.label }}</span>
            <label
              v-for="p in a.permissions"
              :key="p.id"
              class="flex items-start gap-2 text-toned"
              :title="p.id"
            >
              <input
                type="checkbox"
                class="mt-[3px]"
                :checked="r.editable && canWrite ? draft.permissions.includes(p.id) : has(r, p.id)"
                :disabled="!(r.editable && canWrite) || busy || (!grantable(p.id) && !draft.permissions.includes(p.id))"
                @change="toggle(p.id)"
              >
              <span>{{ p.label }}<span
                v-if="p.description"
                class="text-dimmed"
              > · {{ p.description }}</span></span>
            </label>
          </div>

          <div
            v-if="r.editable && canWrite"
            class="flex flex-wrap items-center gap-2"
          >
            <HBtn
              size="sm"
              variant="primary"
              :disabled="busy || !draft.name.trim()"
              @click="save"
            >
              {{ draft.id ? tt('Save') : tt('Create role') }}
            </HBtn>
            <span class="flex-1" />
            <HBtn
              v-if="draft.id"
              size="sm"
              tone="danger"
              :disabled="busy"
              @click="remove(r)"
            >
              {{ confirming === r.id ? (r.memberCount ? tt('Confirm: {0} members become users', [r.memberCount]) : tt('Confirm delete {0}', [r.name])) : tt('Delete role') }}
            </HBtn>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
