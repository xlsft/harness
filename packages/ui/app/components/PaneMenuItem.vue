<script setup lang="ts">
// Row of a pane menu (6px 9px, 12.5px): label, optional muted value, then a check (14px accent)
// or a submenu chevron. The highlighted row gets #1c1c1c, white text and the 2px accent rule.
import type { IconName } from '~/utils/icons'

withDefaults(defineProps<{
  role?: 'menuitem' | 'menuitemcheckbox' | 'menuitemradio'
  checked?: boolean
  value?: string
  // Shortcut shown as UKbd keys ("meta+k"), in place of a value.
  kbd?: string
  valueClass?: string
  sub?: boolean
  subSize?: number
  icon?: IconName
  highlight?: boolean
  active?: boolean
  danger?: boolean
  disabled?: boolean
  gap?: string
  // Keep the 14px check slot when unchecked, so values line up.
  reserve?: boolean
}>(), {
  role: 'menuitem',
  checked: false,
  value: '',
  kbd: '',
  valueClass: 'text-muted',
  sub: false,
  subSize: 12,
  icon: undefined,
  highlight: false,
  active: false,
  danger: false,
  disabled: false,
  gap: 'gap-2',
  reserve: false
})
</script>

<template>
  <div
    :role="role"
    :aria-checked="role === 'menuitem' ? undefined : checked"
    :aria-disabled="disabled || undefined"
    class="flex cursor-default items-center border-l-2 px-[9px] py-1.5"
    :class="[
      gap,
      highlight ? 'border-accent bg-raised text-white' : 'border-transparent',
      !highlight && (disabled ? 'text-muted' : danger ? 'text-bad hover:bg-raised' : [active ? 'text-white' : 'text-toned', 'hover:border-accent hover:bg-raised hover:text-white'])
    ]"
  >
    <HIcon
      v-if="icon"
      :name="icon"
      :size="14"
    />
    <slot />
    <span class="flex-1" />
    <HKeys
      v-if="kbd"
      :keys="kbd"
    />
    <span
      v-else-if="value"
      :class="valueClass"
    >{{ value }}</span>
    <HIcon
      v-if="sub"
      name="right"
      :size="subSize"
      class="text-muted"
    />
    <HIcon
      v-else-if="checked"
      name="check"
      :size="14"
      class="text-accent"
    />
    <span
      v-else-if="reserve"
      class="size-3.5 shrink-0"
    />
  </div>
</template>
