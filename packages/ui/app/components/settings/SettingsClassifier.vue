<script setup lang="ts">
// Settings > Providers > Classifiers > Auto-mode classifier (admins): auto mode decides tool calls with the
// built-in rules, then the decision step (a connected classifier provider: a typed decision model or a
// policy webhook), then an LLM model as classifier when the step is unsure or off. Server-wide.
import type { ClassifierSettings, PresetView, ProviderView } from '~/lib/providers/types'

const props = defineProps<{ providers: ProviderView[], presets: PresetView[] }>()
const store = useSessionStore()

const c = ref<ClassifierSettings | null>(null)
const error = ref('')
const saved = ref(false)
const saving = ref(false)

// Global classifier providers, plus an LLM provider the chain already uses (set up before classifier
// providers existed: its key is reused on a decision endpoint of the same host).
const choices = computed(() => props.providers.filter(p => p.scope === 'global' && (p.group === 'classifier' || p.id === c.value?.providerId)))
const chosen = computed(() => props.providers.find(p => p.id === c.value?.providerId))
const webhook = computed(() => chosen.value?.type === 'decision-http')
const suggested = computed(() => props.presets.find(p => p.id === chosen.value?.presetId)?.defaultModels ?? [])
const llmModels = computed(() => props.providers.filter(p => p.scope === 'global' && p.group === 'llm').flatMap(p => p.models.map(m => ({ ...m, provider: p.name }))))

onMounted(async () => {
  try {
    c.value = await store.transport!.providers().getClassifier()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
})

function pick(id: string) {
  if (!c.value) return
  c.value.providerId = id
  // The new provider's own default model applies unless one is typed.
  c.value.model = ''
  c.value.endpoint = ''
}

async function save() {
  if (!c.value) return
  saving.value = true
  saved.value = false
  try {
    c.value = await store.transport!.providers().setClassifier({ ...c.value })
    error.value = ''
    saved.value = true
  } catch (e) {
    error.value = e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
  } finally {
    saving.value = false
  }
}

const input = 'h-8 w-[300px] border border-accented bg-default px-2 text-[12.5px] text-white outline-none focus:border-accent max-sm:w-full'
const small = 'h-8 w-[120px] border border-accented bg-default px-2 text-[12.5px] text-white outline-none focus:border-accent'
</script>

<template>
  <section
    aria-label="Auto-mode classifier"
    class="mt-4 flex flex-col"
  >
    <h3 class="m-0 text-[13px] font-semibold text-white">
      Auto-mode classifier
    </h3>
    <p class="m-0 mt-1 mb-2 text-[12px] text-muted">
      Built-in rules first, then the classifier below, then an LLM model when it is unsure. Applies to every user.
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
    <form
      v-if="c"
      class="flex flex-col"
      @submit.prevent="save"
    >
      <div class="set-row">
        <div class="set-label">
          <span>Classifier</span>
          <span class="set-desc">A connected classifier answers allow / ask / deny before the LLM model is asked.</span>
        </div>
        <HToggle
          v-model="c.enabled"
          label="Use a classifier"
        />
      </div>
      <template v-if="c.enabled">
        <div class="set-row">
          <div class="set-label">
            <span>Provider</span>
            <span class="set-desc">{{ choices.length ? 'One of the classifiers connected above.' : 'Connect a classifier above first.' }}</span>
          </div>
          <HSelect
            :model-value="c.providerId"
            aria-label="Classifier provider"
            placeholder="Choose a classifier"
            :options="choices.map(p => ({ value: p.id, label: p.name, hint: p.group === 'llm' ? 'LLM key' : undefined }))"
            @update:model-value="pick"
          />
        </div>
        <template v-if="!webhook">
          <div class="set-row">
            <div class="set-label">
              <span>Decision model</span>
              <span class="set-desc">Empty: the provider's default{{ suggested[0] ? ` (${suggested[0].upstream})` : '' }}.</span>
            </div>
            <input
              v-model="c.model"
              aria-label="Decision model id"
              list="classifier-models"
              :placeholder="suggested[0]?.upstream ?? 'model id'"
              :class="input"
            >
            <datalist id="classifier-models">
              <option
                v-for="m in suggested"
                :key="m.upstream"
                :value="m.upstream"
              >
                {{ m.name }}
              </option>
            </datalist>
          </div>
          <div class="set-row">
            <div class="set-label">
              <span>Confidence</span>
              <span class="set-desc">Minimum probability of the answer, and its lead over the runner-up.</span>
            </div>
            <div class="flex gap-2">
              <input
                v-model.number="c.minProbability"
                type="number"
                min="0"
                max="1"
                step="0.05"
                aria-label="Minimum probability"
                :class="small"
              >
              <input
                v-model.number="c.minMargin"
                type="number"
                min="0"
                max="1"
                step="0.05"
                aria-label="Minimum margin"
                :class="small"
              >
            </div>
          </div>
          <div class="set-row">
            <div class="set-label">
              <span>When unsure</span>
            </div>
            <HSelect
              v-model="c.onUncertain"
              aria-label="When the classifier is unsure"
              :options="[{ value: 'next', label: 'Ask the LLM model' }, { value: 'ask', label: 'Ask the developer' }]"
            />
          </div>
        </template>
      </template>
      <div class="set-row">
        <div class="set-label">
          <span>LLM model as classifier</span>
          <span class="set-desc">Decides when the classifier is unsure or off. Empty: the server's classifier or small slot.</span>
        </div>
        <HSelect
          v-model="c.llmModel"
          aria-label="LLM model as classifier"
          :options="[{ value: '', label: 'Server default' }, ...llmModels.map(m => ({ value: m.id, label: m.name, hint: m.provider }))]"
        />
      </div>
      <div class="flex items-center justify-end gap-3 border-t border-default pt-3">
        <span
          v-if="saved"
          class="text-[11.5px] text-muted"
        >Saved</span>
        <HBtn
          size="md"
          variant="primary"
          type="submit"
          :disabled="saving || (c.enabled && !c.providerId)"
        >
          Save classifier
        </HBtn>
      </div>
    </form>
  </section>
</template>
