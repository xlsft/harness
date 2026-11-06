<script setup lang="ts">
// Connect form of a preset (Settings > Providers): only the fields the service needs (usually the API key,
// a URL for a local server), a link to where its keys are made, and, for admins only, whether to share the
// connection with everyone. Classifier providers are always server-wide. A cloud platform also asks how to sign in:
// its credential mode decides which fields and options apply, and the server's own identity is for shared connections.
import type { PresetView, ProviderDraft } from '~/lib/providers/types'

const props = defineProps<{ preset: PresetView, canManageGlobal: boolean, busy: boolean }>()
const emit = defineEmits<{ submit: [draft: ProviderDraft], cancel: [] }>()

const values = reactive<Record<string, string>>(
  Object.fromEntries(props.preset.fields.map(f => [f.name, f.name === 'base_url' ? props.preset.baseUrl : '']))
)
const cloud = computed(() => !!props.preset.cloud)
const optionValues = reactive<Record<string, string>>(
  cloud.value ? Object.fromEntries((props.preset.options ?? []).map(o => [o.name, o.defaultValue])) : {}
)
const mode = computed(() => optionValues.credential_mode ?? '')
const applies = (s: { modes?: string[] }) => !s.modes?.length || s.modes.includes(mode.value)
const fields = computed(() => props.preset.fields.filter(applies))
const options = computed(() => cloud.value ? (props.preset.options ?? []).filter(applies) : [])
const modeChoices = (name: string) => (props.preset.options ?? []).find(o => o.name === name)?.choices
  .filter(c => shared.value || !c.globalOnly) ?? []
// Classifiers and local servers can only be connected for everyone.
const forcedGlobal = computed(() => props.preset.group === 'classifier' || props.preset.local)
const shared = ref(forcedGlobal.value)
const ready = computed(() => fields.value.every(f => !f.required || values[f.name]?.trim())
  && options.value.every(o => !o.required || optionValues[o.name]?.trim()))

// Sharing off takes the server's own identity away.
watch(shared, () => {
  const spec = props.preset.options?.find(o => o.name === 'credential_mode')
  if (cloud.value && spec?.choices.find(c => c.value === mode.value)?.globalOnly && !shared.value) optionValues.credential_mode = spec.defaultValue
})

function submit() {
  emit('submit', {
    scope: shared.value || forcedGlobal.value ? 'global' : 'personal',
    presetId: props.preset.id,
    apiKey: values.api_key?.trim() || undefined,
    baseUrl: values.base_url?.trim() || undefined,
    options: cloud.value ? Object.fromEntries(options.value.map(o => [o.name, optionValues[o.name]?.trim() ?? ''])) : undefined
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
      v-for="o in options"
      :key="o.name"
      class="flex flex-col gap-1 text-[11.5px] text-muted"
    >
      {{ o.label }}
      <select
        v-if="o.kind === 'select'"
        v-model="optionValues[o.name]"
        :class="input"
      >
        <option
          v-for="c in modeChoices(o.name)"
          :key="c.value"
          :value="c.value"
        >{{ c.label }}</option>
      </select>
      <input
        v-else
        v-model="optionValues[o.name]"
        type="text"
        autocomplete="off"
        spellcheck="false"
        :required="o.required"
        :placeholder="o.placeholder"
        :class="input"
      >
    </label>
    <label
      v-for="(f, i) in fields"
      :key="f.name + f.label"
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
      <textarea
        v-if="f.multiline"
        v-model="values[f.name]"
        rows="5"
        autocomplete="off"
        spellcheck="false"
        :required="f.required"
        :placeholder="f.placeholder"
        class="w-full border border-accented bg-default px-2 py-1 font-mono text-[11.5px] text-strong outline-none [-webkit-text-security:disc] focus:border-accent"
      />
      <input
        v-else
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
