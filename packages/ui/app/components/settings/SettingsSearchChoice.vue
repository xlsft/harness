<script setup lang="ts">
// Which search provider is in use, for one provider row of Settings > Search: the default for everyone
// (admins switch it; only providers everyone can use) and the caller's own choice (off follows the default).
const props = defineProps<{
  providerId: string
  name: string
  // Everyone can use it: a global provider or the built-in one.
  shared: boolean
  enabled: boolean
  // The built-in SearXNG: the default whenever nothing else is.
  builtin?: boolean
}>()

const state = inject(SEARCH)!
const s = computed(() => state.settings.value)
const isDefault = computed(() => s.value?.defaultProviderId === props.providerId)
const mine = computed(() => s.value?.myProviderId === props.providerId)
const searching = computed(() => state.effectiveProviderId.value === props.providerId)

function toggleDefault() {
  // Off goes back to the built-in, which cannot be switched off itself.
  if (props.builtin && isDefault.value) return
  void state.setDefault(isDefault.value ? '' : props.providerId)
}
</script>

<template>
  <div
    v-if="s"
    class="flex flex-wrap items-center gap-x-4 gap-y-1.5 pr-3 pb-2 pl-[52px] text-[11.5px] max-sm:pl-3"
  >
    <template v-if="shared">
      <SettingsCheckSwitch
        v-if="s.editable && (enabled || isDefault)"
        :checked="isDefault"
        :disabled="builtin && isDefault"
        :label="`Use ${name} by default for everyone`"
        @toggle="toggleDefault"
      >
        default for everyone
      </SettingsCheckSwitch>
      <span
        v-else-if="isDefault"
        class="text-muted"
      >default for everyone</span>
    </template>
    <SettingsCheckSwitch
      v-if="enabled"
      :checked="mine"
      :label="`Use ${name} for me`"
      @toggle="state.setMine(mine ? '' : providerId)"
    >
      use for me
    </SettingsCheckSwitch>
    <span
      v-if="searching"
      class="text-ok"
    >searching for you</span>
  </div>
</template>
