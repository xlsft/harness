<script setup lang="ts">
// Fields of the provider form (Settings > Providers): type, name, base URL, API key (write-only), plus the
// id and private-network switch for global providers.
import type { ProviderForm } from '~/lib/providers/types'
import { PROVIDER_TYPE_LABELS } from '~/lib/providers/types'

const form = defineModel<ProviderForm>({ required: true })
defineProps<{ types: string[], input: string, keyHint?: string }>()
const emit = defineEmits<{ type: [type: string] }>()
</script>

<template>
  <div class="grid grid-cols-2 gap-3 max-sm:grid-cols-1">
    <label class="flex flex-col gap-1 text-[11.5px] text-muted">
      Type
      <select
        :value="form.type"
        :class="input"
        @change="emit('type', ($event.target as HTMLSelectElement).value)"
      >
        <option
          v-for="t in types"
          :key="t"
          :value="t"
        >
          {{ PROVIDER_TYPE_LABELS[t] ?? t }}
        </option>
      </select>
    </label>
    <label class="flex flex-col gap-1 text-[11.5px] text-muted">
      Name
      <input
        v-model="form.name"
        placeholder="e.g. OpenRouter"
        :class="input"
      >
    </label>
    <label
      v-if="form.scope === 'global' && !form.id"
      class="flex flex-col gap-1 text-[11.5px] text-muted"
    >
      Id
      <input
        v-model="form.newId"
        placeholder="openrouter"
        pattern="[a-z0-9][a-z0-9._\-]*"
        required
        :class="input"
      >
    </label>
    <label class="col-span-2 flex flex-col gap-1 text-[11.5px] text-muted max-sm:col-span-1">
      Base URL
      <input
        v-model="form.baseUrl"
        type="url"
        required
        placeholder="https://api.example.com/v1"
        :class="input"
      >
    </label>
    <label class="col-span-2 flex flex-col gap-1 text-[11.5px] text-muted max-sm:col-span-1">
      API key
      <input
        v-model="form.apiKey"
        type="password"
        autocomplete="off"
        :disabled="form.clearKey"
        :placeholder="keyHint ? `Set (${keyHint}); leave empty to keep it` : 'Not shown again after saving'"
        :class="input"
      >
    </label>
    <label
      v-if="keyHint"
      class="flex items-center gap-2 text-[11.5px] text-muted"
    >
      <HToggle
        v-model="form.clearKey"
        label="Remove the key"
      />Remove the key
    </label>
    <label
      v-if="form.scope === 'global'"
      class="flex items-center gap-2 text-[11.5px] text-muted"
    >
      <HToggle
        v-model="form.allowPrivateNetwork"
        label="Allow private network"
      />Allow private network (local vLLM, Ollama)
    </label>
  </div>
</template>
