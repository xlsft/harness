<script setup lang="ts">
// Settings > Providers > Classifier (admins): auto mode decides tool calls with rules, then a typed decision
// model (Jev through OpenRouter, or TypeSafe) paid with a global provider's key, then the model classifier.
import type { ClassifierSettings, ProviderView } from '~/lib/providers/types'

const props = defineProps<{ providers: ProviderView[] }>()
const store = useSessionStore()

const c = ref<ClassifierSettings | null>(null)
const error = ref('')
const saved = ref(false)
const saving = ref(false)

const keyed = computed(() => props.providers.filter(p => p.keySet))
const models = computed(() => props.providers.flatMap(p => p.models))

onMounted(async () => {
  try {
    c.value = await store.transport!.providers().getClassifier()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
})

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
</script>

<template>
  <section
    aria-label="Classifier"
    class="flex flex-col"
  >
    <h2 class="set-h">
      Classifier
    </h2>
    <p class="set-sub">
      How auto mode decides tool calls: built-in rules, then a typed decision model, then the model classifier when it is unsure. Applies to every user.
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
          <span>Decision model</span>
          <span class="set-desc">A model that answers allow / ask / deny with probabilities.</span>
        </div>
        <HToggle
          v-model="c.enabled"
          label="Use a decision model"
        />
      </div>
      <template v-if="c.enabled">
        <div class="set-row">
          <div class="set-label">
            <span>Provider</span>
            <span class="set-desc">A global provider whose key pays for decisions (OpenRouter or TypeSafe).</span>
          </div>
          <select
            v-model="c.providerId"
            aria-label="Decision model provider"
            :class="input"
          >
            <option value="">
              Choose a provider
            </option>
            <option
              v-for="p in keyed"
              :key="p.id"
              :value="p.id"
            >
              {{ p.name }} ({{ p.baseUrl }})
            </option>
          </select>
        </div>
        <div class="set-row">
          <div class="set-label">
            <span>Model</span>
            <span class="set-desc">Empty: the endpoint's default (typesafe/jev-1.13 on OpenRouter).</span>
          </div>
          <input
            v-model="c.model"
            aria-label="Decision model id"
            placeholder="typesafe/jev-1.13"
            :class="input"
          >
        </div>
        <div class="set-row">
          <div class="set-label">
            <span>Endpoint</span>
            <span class="set-desc">Empty: from the provider (openrouter, typesafe), or an https URL.</span>
          </div>
          <input
            v-model="c.endpoint"
            aria-label="Decision endpoint"
            placeholder="from the provider"
            :class="input"
          >
        </div>
        <div class="set-row">
          <div class="set-label">
            <span>Confidence</span>
            <span class="set-desc">Minimum probability of the answer and its lead over the runner-up.</span>
          </div>
          <div class="flex gap-2">
            <input
              v-model.number="c.minProbability"
              type="number"
              min="0"
              max="1"
              step="0.05"
              aria-label="Minimum probability"
              :class="input"
              class="w-[120px]"
            >
            <input
              v-model.number="c.minMargin"
              type="number"
              min="0"
              max="1"
              step="0.05"
              aria-label="Minimum margin"
              :class="input"
              class="w-[120px]"
            >
          </div>
        </div>
        <div class="set-row">
          <div class="set-label">
            <span>When unsure</span>
          </div>
          <select
            v-model="c.onUncertain"
            aria-label="When the decision model is unsure"
            :class="input"
          >
            <option value="next">
              Ask the model classifier
            </option>
            <option value="ask">
              Ask the developer
            </option>
          </select>
        </div>
      </template>
      <div class="set-row">
        <div class="set-label">
          <span>Classifier model</span>
          <span class="set-desc">Model of the last step. Empty: the server's classifier or small slot.</span>
        </div>
        <select
          v-model="c.llmModel"
          aria-label="Classifier model"
          :class="input"
        >
          <option value="">
            Server default
          </option>
          <option
            v-for="m in models"
            :key="m.id"
            :value="m.id"
          >
            {{ m.name }} ({{ m.id }})
          </option>
        </select>
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
