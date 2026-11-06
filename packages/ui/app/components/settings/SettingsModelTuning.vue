<script setup lang="ts">
// Settings > Models: how this user's requests run, on every device (synced preferences). A speed tier or cache lifetime
// applies where the model has it and is ignored elsewhere; the Advisor tool exists only with a model chosen here. The
// choices follow the models the catalog says have them; sessions opened from then on use them.
import type { ProviderView } from '~/lib/providers/types'
import { slotModelOptions } from '~/lib/providers/types'
import type { ModelTuningPreferences } from '~/lib/transport/types'

const props = defineProps<{ providers: ProviderView[] }>()
const store = useSessionStore()

const tuning = ref<ModelTuningPreferences>({ speedTier: '', cacheTtl: '', advisorModel: '' })
const loaded = ref(false)
const error = ref('')

const SPEEDS = [
  { value: '', label: 'Provider default' },
  { value: 'fast', label: 'Fast', hint: 'Faster output at a higher price' },
  { value: 'flex', label: 'Flex', hint: 'Slower and cheaper' }
]
const TTLS = [
  { value: '', label: 'Provider default' },
  { value: '1h', label: '1 hour', hint: 'Costs more to write; worth it with long pauses' },
  { value: '24h', label: '24 hours' }
]
const advisors = computed(() => [{ value: '', label: 'None' }, ...slotModelOptions(props.providers)])

// What the models here take, so a choice nothing has is not offered (unless it is already set).
const speedOptions = computed(() => SPEEDS.filter(o => !o.value || o.value === tuning.value.speedTier || store.models.some(m => m.speedTiers?.includes(o.value))))
const ttlOptions = computed(() => TTLS.filter(o => !o.value || o.value === tuning.value.cacheTtl || store.models.some(m => m.cacheTtls?.includes(o.value))))

onMounted(async () => {
  if (!store.transport) return
  try {
    tuning.value = (await store.transport.getUserPreferences()).modelTuning
    loaded.value = true
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
})

async function save(patch: Partial<ModelTuningPreferences>) {
  if (!store.transport) return
  const before = tuning.value
  tuning.value = { ...before, ...patch }
  error.value = ''
  try {
    tuning.value = (await store.transport.updateUserPreferences({ modelTuning: tuning.value })).modelTuning
  } catch (e) {
    tuning.value = before
    error.value = e instanceof Error ? e.message : String(e)
  }
}
</script>

<template>
  <section
    v-if="loaded"
    class="flex flex-col gap-3"
    aria-label="How requests run"
  >
    <h3 class="text-[12.5px] font-medium text-strong">
      How requests run
    </h3>
    <div class="grid gap-3 sm:grid-cols-3">
      <label class="flex flex-col gap-1 text-[11.5px] text-muted">
        Speed
        <HSelect
          :model-value="tuning.speedTier"
          :options="speedOptions"
          aria-label="Speed tier"
          @update:model-value="save({ speedTier: String($event ?? '') })"
        />
      </label>
      <label class="flex flex-col gap-1 text-[11.5px] text-muted">
        Prompt cache lifetime
        <HSelect
          :model-value="tuning.cacheTtl"
          :options="ttlOptions"
          aria-label="Prompt cache lifetime"
          @update:model-value="save({ cacheTtl: String($event ?? '') })"
        />
      </label>
      <label class="flex flex-col gap-1 text-[11.5px] text-muted">
        Advisor
        <HSelect
          :model-value="tuning.advisorModel"
          :options="advisors"
          aria-label="Advisor model"
          @update:model-value="save({ advisorModel: String($event ?? '') })"
        />
      </label>
    </div>
    <p class="text-[11.5px] text-muted">
      New sessions use these. Speed and cache apply only where the model has them. The advisor is a second model the agent can consult: it reads the conversation and costs what that model costs.
    </p>
    <p
      v-if="error"
      role="alert"
      class="text-[11.5px] text-bad"
    >
      {{ error }}
    </p>
  </section>
</template>
