<script setup lang="ts">
// One provider source of Settings > Providers ("LLM models" or "Classifiers"): the presets first, each with
// its connections underneath (one for everyone and/or one of the user's own), then any number of custom
// providers. Connecting a preset asks only for what the service needs.
import type { PresetView, ProtocolView, ProviderDraft, ProviderGroupName, ProviderView, ProvidersApi } from '~/lib/providers/types'

const props = defineProps<{
  group: ProviderGroupName
  title: string
  sub: string
  presets: PresetView[]
  protocols: ProtocolView[]
  providers: ProviderView[]
  canManageGlobal: boolean
  api: ProvidersApi
}>()
const emit = defineEmits<{ changed: [], error: [message: string] }>()

const presetIds = computed(() => new Set(props.presets.map(p => p.id)))
const connectionsOf = (id: string) => props.providers.filter(p => p.presetId === id)
const custom = computed(() => props.providers.filter(p => !p.presetId || !presetIds.value.has(p.presetId)))
// Classifier providers are server-wide: only admins add them.
const canAdd = computed(() => props.group === 'llm' || props.canManageGlobal)

function canConnect(p: PresetView) {
  if (!canAdd.value) return false
  // A local server is reached from the server's network: a global connection, so admins only.
  if (p.local && !props.canManageGlobal) return false
  const mine = connectionsOf(p.id)
  // One connection per scope is plenty; a second one of the same scope is a custom provider's job.
  const scopes = props.canManageGlobal && props.group === 'llm' && !p.local ? 2 : 1
  return mine.filter(c => c.editable).length < scopes
}

const connecting = ref('')
const addingCustom = ref(false)
const busy = ref(false)
// The provider just connected opens its model list.
const fresh = ref('')

async function create(draft: ProviderDraft) {
  busy.value = true
  try {
    const p = await props.api.create(draft)
    fresh.value = p.id
    connecting.value = ''
    addingCustom.value = false
    emit('changed')
  } catch (e) {
    emit('error', e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section
    :aria-label="title || undefined"
    class="mb-8 flex flex-col"
  >
    <template v-if="title">
      <h3 class="m-0 text-[13px] font-semibold text-white">
        {{ title }}
      </h3>
      <p class="m-0 mt-1 mb-3 text-[12px] text-muted">
        {{ sub }}
      </p>
    </template>

    <div class="border border-default">
      <div
        v-for="(preset, i) in presets"
        :key="preset.id"
        class="flex flex-col"
        :class="{ 'border-t border-default': i > 0 }"
      >
        <div class="flex items-center gap-3 px-3 py-2.5">
          <SettingsMonogram
            :text="preset.icon"
            :dim="!connectionsOf(preset.id).length"
          />
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="flex items-center gap-2 text-[12.5px] text-white">
              {{ preset.name }}
              <span
                v-if="connectionsOf(preset.id).length"
                class="text-[11px] text-ok"
              >connected</span>
            </span>
            <span class="truncate text-[11.5px] text-muted">{{ preset.description }}</span>
          </span>
          <SettingsLink
            v-if="preset.docsUrl"
            :href="preset.docsUrl"
            class="max-sm:hidden"
          >
            Docs
          </SettingsLink>
          <HBtn
            v-if="canConnect(preset) && connecting !== preset.id"
            size="sm"
            @click="connecting = preset.id"
          >
            Connect
          </HBtn>
          <span
            v-else-if="!canAdd || (preset.local && !canManageGlobal)"
            class="text-[11px] text-dimmed"
          >admins connect it</span>
        </div>
        <SettingsConnectForm
          v-if="connecting === preset.id"
          :preset="preset"
          :can-manage-global="canManageGlobal"
          :busy="busy"
          @submit="create"
          @cancel="connecting = ''"
        />
        <SettingsProviderCard
          v-for="c in connectionsOf(preset.id)"
          :key="c.id"
          :provider="c"
          :preset="preset"
          :protocols="protocols"
          :can-manage-global="canManageGlobal"
          :api="api"
          :auto-open="fresh === c.id"
          @changed="emit('changed')"
          @error="emit('error', $event)"
        />
      </div>

      <!-- Rows of the page's own (the Classifiers page's model row). -->
      <div
        v-if="$slots.rows"
        class="border-t border-default"
      >
        <slot name="rows" />
      </div>

      <!-- Custom providers -->
      <div class="flex flex-col border-t border-default">
        <div class="flex items-center gap-3 px-3 py-2.5">
          <SettingsMonogram
            text="+"
            :dim="!custom.length"
          />
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="text-[12.5px] text-white">Custom</span>
            <span class="truncate text-[11.5px] text-muted">{{ group === 'llm' ? 'Any compatible API: you set the protocol, URL, key and headers.' : 'A decision model or policy webhook of your own.' }}</span>
          </span>
          <HBtn
            v-if="canAdd && !addingCustom"
            size="sm"
            @click="addingCustom = true"
          >
            <HIcon
              name="plus"
              :size="12"
            />Add custom
          </HBtn>
        </div>
        <SettingsCustomForm
          v-if="addingCustom"
          :group="group"
          :protocols="protocols"
          :can-manage-global="canManageGlobal"
          :busy="busy"
          @create="create"
          @cancel="addingCustom = false"
        />
        <SettingsProviderCard
          v-for="c in custom"
          :key="c.id"
          :provider="c"
          :protocols="protocols"
          :can-manage-global="canManageGlobal"
          :api="api"
          :auto-open="fresh === c.id"
          @changed="emit('changed')"
          @error="emit('error', $event)"
        />
      </div>
    </div>

    <slot />
  </section>
</template>
