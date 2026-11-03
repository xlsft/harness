<script setup lang="ts">
// Connect form of Settings > Search: a preset's fields only (usually the API key, an instance URL for
// SearXNG), or for a custom provider a name and a SearXNG-compatible URL. Admins may share the connection
// with everyone; a shared SearXNG may then reach the server's private network.
import type { PresetView, ProviderDraft } from '~/lib/providers/types'

const props = defineProps<{ preset?: PresetView, canManageGlobal: boolean, busy: boolean }>()
const emit = defineEmits<{ submit: [draft: ProviderDraft], cancel: [] }>()

const custom = computed(() => !props.preset)
const fields = computed(() => props.preset?.fields ?? [
  { name: 'name', label: 'Name', required: false, placeholder: 'Team SearXNG' },
  { name: 'base_url', label: 'Instance URL', required: true, placeholder: 'https://searx.example.com' }
])
const values = reactive<Record<string, string>>(Object.fromEntries(fields.value.map(f => [f.name, ''])))
const shared = ref(false)
const privateNetwork = ref(false)
const ready = computed(() => fields.value.every(f => !f.required || values[f.name]?.trim()))
const reachesNetwork = computed(() => custom.value || !!props.preset?.local)

function submit() {
  const scope = shared.value && props.canManageGlobal ? 'global' : 'personal'
  emit('submit', {
    scope,
    ...(props.preset ? { presetId: props.preset.id } : { group: 'search', type: 'searxng', name: values.name?.trim() || undefined }),
    apiKey: values.api_key?.trim() || undefined,
    baseUrl: values.base_url?.trim() || undefined,
    allowPrivateNetwork: custom.value && scope === 'global' && privateNetwork.value
  })
}

const input = 'h-8 w-full border border-accented bg-default px-2 text-[12.5px] text-white outline-none focus:border-accent'
</script>

<template>
  <form
    class="flex flex-col gap-3 border-t border-default px-3 py-3"
    :aria-label="preset ? `Connect ${preset.name}` : 'Add a custom search provider'"
    @submit.prevent="submit"
  >
    <label
      v-for="(f, i) in fields"
      :key="f.name"
      class="flex flex-col gap-1 text-[11.5px] text-muted"
    >
      <span class="flex items-center gap-2">
        {{ f.label }}
        <span class="flex-1" />
        <SettingsLink
          v-if="f.name === 'api_key' && preset?.keyUrl"
          :href="preset.keyUrl"
        >Get a key</SettingsLink>
      </span>
      <input
        v-model="values[f.name]"
        :type="f.name === 'api_key' ? 'password' : f.name === 'base_url' ? 'url' : 'text'"
        :autocomplete="f.name === 'api_key' ? 'off' : undefined"
        :autofocus="i === 0"
        :required="f.required"
        :placeholder="f.placeholder || (f.name === 'api_key' ? 'Stored encrypted, never shown again' : '')"
        :class="input"
      >
    </label>
    <div class="flex flex-wrap items-center gap-3">
      <label
        v-if="canManageGlobal"
        class="flex items-center gap-2 text-[11.5px] text-muted"
      >
        <HToggle
          v-model="shared"
          label="Share with everyone on this server"
        />Share with everyone on this server
      </label>
      <label
        v-if="canManageGlobal && shared && custom"
        class="flex items-center gap-2 text-[11.5px] text-muted"
      >
        <HToggle
          v-model="privateNetwork"
          label="Allow private network addresses"
        />Private network
      </label>
      <span
        v-if="!shared || !canManageGlobal"
        class="text-[11.5px] text-muted"
      >Only you will see and use it{{ reachesNetwork ? '; it must be reachable from the internet' : '' }}.</span>
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
