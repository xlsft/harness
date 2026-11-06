<script setup lang="ts">
// Group to role rules, the same for LDAP, identity providers and SCIM: first match wins, no match means a plain user.
import type { RoleMapping } from '~/lib/enterprise/types'
import { ROLE_LABEL } from '~/lib/enterprise/types'
import type { UserRole } from '~/lib/users/types'

const props = defineProps<{ modelValue: RoleMapping[], placeholder?: string, readonly?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [RoleMapping[]] }>()

const ROLES = (['admin', 'auditor', 'billing', 'user'] as UserRole[]).map(value => ({ value, label: ROLE_LABEL[value] }))

function set(i: number, patch: Partial<RoleMapping>) {
  emit('update:modelValue', props.modelValue.map((m, j) => j === i ? { ...m, ...patch } : m))
}
function move(i: number, by: number) {
  const list = [...props.modelValue]
  const [m] = list.splice(i, 1)
  list.splice(i + by, 0, m!)
  emit('update:modelValue', list)
}
function remove(i: number) {
  emit('update:modelValue', props.modelValue.filter((_, j) => j !== i))
}
function add() {
  emit('update:modelValue', [...props.modelValue, { group: '', role: 'user' }])
}

const input = 'h-8 min-w-0 flex-1 border border-accented bg-default px-2 text-[12.5px] text-strong outline-none focus:border-accent disabled:opacity-60'
</script>

<template>
  <div class="flex w-full flex-col gap-1.5">
    <div
      v-for="(m, i) in modelValue"
      :key="i"
      class="flex items-center gap-1.5"
    >
      <input
        :value="m.group"
        :disabled="readonly"
        :aria-label="`Group of rule ${i + 1}`"
        :placeholder="placeholder ?? 'Group'"
        spellcheck="false"
        autocomplete="off"
        :class="input"
        @input="set(i, { group: ($event.target as HTMLInputElement).value })"
      >
      <span class="text-[11.5px] text-muted">gets</span>
      <HSelect
        v-if="!readonly"
        :model-value="m.role"
        :aria-label="`Role of rule ${i + 1}`"
        :options="ROLES"
        class="w-[110px]"
        @update:model-value="set(i, { role: $event as UserRole })"
      />
      <span
        v-else
        class="text-[12px] text-toned"
      >{{ ROLE_LABEL[m.role] }}</span>
      <template v-if="!readonly">
        <HIconBtn
          icon="up"
          :size="26"
          :icon-size="12"
          label="Move up"
          :disabled="i === 0"
          @click="move(i, -1)"
        />
        <HIconBtn
          icon="down"
          :size="26"
          :icon-size="12"
          label="Move down"
          :disabled="i === modelValue.length - 1"
          @click="move(i, 1)"
        />
        <HIconBtn
          icon="x"
          :size="26"
          :icon-size="12"
          label="Remove rule"
          @click="remove(i)"
        />
      </template>
    </div>
    <p
      v-if="readonly && !modelValue.length"
      class="m-0 text-[12px] text-muted"
    >
      No rules: roles are managed in Users.
    </p>
    <div v-if="!readonly">
      <HBtn
        size="sm"
        @click="add"
      >
        <HIcon
          name="plus"
          :size="12"
        />Add rule
      </HBtn>
    </div>
  </div>
</template>
