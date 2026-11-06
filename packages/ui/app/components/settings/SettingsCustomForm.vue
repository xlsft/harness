<script setup lang="ts">
// Form of a custom provider (Settings > Providers): every field is the user's, protocol, name, base URL,
// key, extra headers. New and edited providers share it; keys and header values are write-only, so an edit
// sends them only when typed. Admins may share an LLM provider with everyone and let it reach private
// networks; classifier providers are always server-wide.
import type { ProtocolView, ProviderDraft, ProviderGroupName, ProviderPatch, ProviderView } from '~/lib/providers/types'
import { parseHeaders } from '~/lib/providers/types'

const props = defineProps<{
  group: ProviderGroupName
  protocols: ProtocolView[]
  canManageGlobal: boolean
  busy: boolean
  // The provider being edited; none for a new one.
  provider?: ProviderView
}>()
const emit = defineEmits<{ create: [draft: ProviderDraft], update: [patch: ProviderPatch], cancel: [] }>()

const p = props.provider
const form = reactive({
  type: p?.type ?? props.protocols[0]?.id ?? '',
  name: p?.name === p?.id ? '' : p?.name ?? '',
  baseUrl: p?.baseUrl ?? '',
  apiKey: '',
  clearKey: false,
  headers: '',
  shared: props.group === 'classifier' || p?.scope === 'global',
  allowPrivateNetwork: p?.allowPrivateNetwork ?? false
})
const headerError = ref('')
const protocol = computed(() => props.protocols.find(x => x.id === form.type))
const global = computed(() => props.group === 'classifier' || form.shared)

function submit() {
  const headers = form.headers.trim() ? parseHeaders(form.headers) : undefined
  if (typeof headers === 'string') {
    headerError.value = headers
    return
  }
  headerError.value = ''
  if (p) {
    emit('update', {
      type: form.type,
      name: form.name,
      baseUrl: form.baseUrl,
      apiKey: form.clearKey ? '' : form.apiKey || undefined,
      headers,
      allowPrivateNetwork: p.scope === 'global' ? form.allowPrivateNetwork : undefined
    })
    return
  }
  emit('create', {
    scope: global.value ? 'global' : 'personal',
    group: props.group,
    type: form.type,
    name: form.name,
    baseUrl: form.baseUrl,
    apiKey: form.apiKey || undefined,
    headers,
    allowPrivateNetwork: global.value && form.allowPrivateNetwork
  })
}

const input = 'h-8 w-full border border-accented bg-default px-2 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <form
    class="flex flex-col gap-3 border-t border-default px-3 py-3"
    :aria-label="provider ? tt('Edit {0}', [provider.name]) : tt('New custom provider')"
    @submit.prevent="submit"
  >
    <div class="grid grid-cols-2 gap-3 max-sm:grid-cols-1">
      <label class="flex flex-col gap-1 text-[11.5px] text-muted">
        {{ tt('Protocol') }}
        <HSelect
          v-model="form.type"
          :aria-label="tt('Protocol')"
          :options="protocols.map(t => ({ value: t.id, label: t.label }))"
        />
      </label>
      <label class="flex flex-col gap-1 text-[11.5px] text-muted">
        {{ tt('Name') }}
        <input
          v-model="form.name"
          required
          placeholder="e.g. Team gateway"
          :class="input"
        >
      </label>
      <label class="col-span-2 flex flex-col gap-1 text-[11.5px] text-muted max-sm:col-span-1">
        {{ group === 'classifier' ? tt('Endpoint URL') : tt('Base URL') }}
        <input
          v-model="form.baseUrl"
          type="url"
          required
          :placeholder="protocol?.baseUrlHint ?? 'https://api.example.com/v1'"
          :class="input"
        >
      </label>
      <label class="col-span-2 flex flex-col gap-1 text-[11.5px] text-muted max-sm:col-span-1">
        {{ group === 'classifier' ? tt('API key or bearer token') : tt('API key') }}
        <input
          v-model="form.apiKey"
          type="password"
          autocomplete="off"
          :disabled="form.clearKey"
          :placeholder="provider?.keySet ? tt('Set ({0}); leave empty to keep it', [provider.keyHint]) : tt('Optional; stored encrypted, never shown again')"
          :class="input"
        >
      </label>
      <label class="col-span-2 flex flex-col gap-1 text-[11.5px] text-muted max-sm:col-span-1">
        {{ tt('Extra headers') }}
        <textarea
          v-model="form.headers"
          rows="2"
          :placeholder="provider?.headerNames.length ? tt('Set: {0}. Type new ones to replace them', [provider.headerNames.join(', ')]) : tt('One per line, e.g. X-Team: core')"
          class="w-full resize-y border border-accented bg-default px-2 py-1.5 font-mono text-[12px] text-strong outline-none focus:border-accent"
        />
        <span
          v-if="headerError"
          role="alert"
          class="text-bad"
        >{{ headerError }}</span>
      </label>
    </div>
    <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
      <label
        v-if="provider?.keySet"
        class="flex items-center gap-2 text-[11.5px] text-muted"
      >
        <HToggle
          v-model="form.clearKey"
          :label="tt('Remove the key')"
        />{{ tt('Remove the key') }}
      </label>
      <label
        v-if="!provider && canManageGlobal && group === 'llm'"
        class="flex items-center gap-2 text-[11.5px] text-muted"
      >
        <HToggle
          v-model="form.shared"
          :label="tt('Share with everyone on this server')"
        />{{ tt('Share with everyone on this server') }}
      </label>
      <label
        v-if="canManageGlobal && (provider ? provider.scope === 'global' : global)"
        class="flex items-center gap-2 text-[11.5px] text-muted"
      >
        <HToggle
          v-model="form.allowPrivateNetwork"
          :label="tt('Allow private network')"
        />{{ tt('Allow private network') }}
      </label>
      <span class="flex-1" />
      <HBtn
        size="md"
        variant="ghost"
        tone="muted"
        @click="emit('cancel')"
      >
        {{ tt('Cancel') }}
      </HBtn>
      <HBtn
        size="md"
        variant="primary"
        type="submit"
        :disabled="busy || !form.baseUrl || !form.type"
      >
        {{ provider ? tt('Save') : tt('Add') }}
      </HBtn>
    </div>
  </form>
</template>
