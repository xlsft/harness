<script setup lang="ts">
// Connect form of a preset (Settings > Providers): only the fields the service needs (usually the API key,
// a URL for a local server), a link to where its keys are made, and, for admins only, whether to share the
// connection with everyone. Classifier providers are always server-wide.
import type { PresetView, ProviderDraft } from '~/lib/providers/types'

const props = defineProps<{ preset: PresetView, canManageGlobal: boolean, busy: boolean }>()
const emit = defineEmits<{ submit: [draft: ProviderDraft], cancel: [] }>()

const values = reactive<Record<string, string>>(
  Object.fromEntries(props.preset.fields.map(f => [f.name, f.name === 'base_url' ? props.preset.baseUrl : '']))
)
// Classifiers and local servers can only be connected for everyone.
const forcedGlobal = computed(() => props.preset.group === 'classifier' || props.preset.local)
const shared = ref(forcedGlobal.value)
const ready = computed(() => props.preset.fields.every(f => !f.required || values[f.name]?.trim()))

function submit() {
  emit('submit', {
    scope: shared.value || forcedGlobal.value ? 'global' : 'personal',
    presetId: props.preset.id,
    apiKey: values.api_key?.trim() || undefined,
    baseUrl: values.base_url?.trim() || undefined
  })
}

const input = 'h-8 w-full border border-accented bg-default px-2 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <form
    class="flex flex-col gap-3 border-t border-default px-3 py-3"
    :aria-label="`Connect ${preset.name}`"
    @submit.prevent="submit"
  >
    <label
      v-for="(f, i) in preset.fields"
      :key="f.name"
      class="flex flex-col gap-1 text-[11.5px] text-muted"
    >
      <span class="flex items-center gap-2">
        {{ f.label }}
        <span class="flex-1" />
        <SettingsLink
          v-if="f.name === 'api_key' && preset.keyUrl"
          :href="preset.keyUrl"
        >Get a key</SettingsLink>
      </span>
      <input
        v-model="values[f.name]"
        :type="f.name === 'api_key' ? 'password' : 'url'"
        :autocomplete="f.name === 'api_key' ? 'off' : undefined"
        :autofocus="i === 0"
        :required="f.required"
        :placeholder="f.placeholder || (f.name === 'api_key' ? 'Stored encrypted, never shown again' : '')"
        :class="input"
      >
    </label>
    <div class="flex flex-wrap items-center gap-3">
      <label
        v-if="canManageGlobal && !forcedGlobal"
        class="flex items-center gap-2 text-[11.5px] text-muted"
      >
        <HToggle
          v-model="shared"
          label="Share with everyone on this server"
        />Share with everyone on this server
      </label>
      <span
        v-else-if="forcedGlobal"
        class="text-[11.5px] text-muted"
      >{{ preset.group === 'classifier' ? 'Server-wide: auto mode uses it for every user.' : 'Shared with everyone: it runs on the server\'s network.' }}</span>
      <span
        v-else
        class="text-[11.5px] text-muted"
      >Only you will see and use it.</span>
      <span class="flex-1" />
      <HBtn
        size="md"
        variant="ghost"
        tone="muted"
        @click="emit('cancel')"
      >
        Cancel
      </HBtn>
      <HBtn
        size="md"
        variant="primary"
        type="submit"
        :disabled="busy || !ready"
      >
        {{ busy ? 'Connecting…' : 'Connect' }}
      </HBtn>
    </div>
  </form>
</template>
