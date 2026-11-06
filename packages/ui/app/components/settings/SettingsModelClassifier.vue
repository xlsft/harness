<script setup lang="ts">
// Classifiers page row: the chat model that decides tool calls when no classifier is in use or it is unsure.
// Configure opens a panel under the row: the server default (admins pick it, everyone else reads it) and the
// caller's own choice, both for the classifier (another one or none) and for the model, each able to follow
// the server default. Saved at once.
import type { ClassifierChoiceName, ProviderView } from '~/lib/providers/types'

const props = defineProps<{ providers: ProviderView[], canManageGlobal: boolean }>()
const state = inject(CLASSIFIER)!
const open = ref(false)

const models = computed(() => props.providers.filter(p => p.scope === 'global' && p.group === 'llm').flatMap(p => p.models.map(m => ({ value: m.id, label: m.name, group: p.name }))))
const classifiers = computed(() => props.providers.filter(p => p.scope === 'global' && p.group === 'classifier' && p.enabled))

// An empty server-default model means the classifier / small slot (the Models page's background tasks).
const SLOT_LABEL = 'Same as background tasks'
const modelLabel = (id: string) => (id ? models.value.find(m => m.value === id)?.label ?? id : SLOT_LABEL)
const classifierLabel = (id: string) => (id ? props.providers.find(p => p.id === id)?.name ?? id : 'None')

const my = computed(() => state.my.value)
const defaultModel = computed(() => my.value?.defaultLlmModel ?? '')

// The caller's classifier as one select value: '' follows the default, '-' is none, else a provider id.
const NONE = '-'
const myClassifier = computed(() => {
  const m = my.value?.mine
  if (!m || m.choice === 'default') return ''
  return m.choice === 'none' ? NONE : m.providerId
})
const classifierOptions = computed(() => [
  { value: '', label: tt('Follow server default ({0})', [classifierLabel(my.value?.defaultProviderId ?? '')]) },
  { value: NONE, label: tt('None: rules, then the model') },
  ...classifiers.value.map(p => ({ value: p.id, label: p.name }))
])
function setMyClassifier(v: string) {
  const choice: ClassifierChoiceName = v === '' ? 'default' : v === NONE ? 'none' : 'provider'
  void state.setMine({ choice, providerId: choice === 'provider' ? v : '' })
}

const label = 'w-[110px] shrink-0 text-[11.5px] text-muted max-sm:w-full'
</script>

<template>
  <div class="flex flex-col">
    <div class="flex items-center gap-3 px-3 py-2.5">
      <span
        aria-hidden="true"
        class="flex size-7 shrink-0 items-center justify-center border border-line text-[11px] bg-raised text-strong"
      ><HIcon
        name="ai"
        :size="15"
      /></span>
      <span class="flex min-w-0 flex-1 flex-col gap-0.5">
        <span class="text-[12.5px] text-strong">{{ tt('Model as classifier') }}</span>
        <span class="truncate text-[11.5px] text-muted">{{ tt('Decides when the classifier is unsure.') }}</span>
      </span>
      <HBtn
        size="sm"
        :aria-expanded="open"
        :disabled="!my"
        @click="open = !open"
      >
        {{ tt('Configure') }}
      </HBtn>
    </div>

    <div
      v-if="open && my"
      class="flex flex-col gap-3 border-t border-default py-2.5 pr-3 pl-[52px] max-sm:pl-3"
      role="group"
      :aria-label="tt('Classifier settings')"
    >
      <!-- The model of the model classifier -->
      <div class="flex flex-col gap-1.5">
        <span class="text-[12px] text-strong">{{ tt('Model') }}</span>
        <div class="flex items-center gap-3 max-sm:flex-col max-sm:items-stretch max-sm:gap-1">
          <span :class="label">{{ tt('Server default') }}</span>
          <div
            v-if="canManageGlobal && state.settings.value"
            class="w-[260px] max-sm:w-full"
          >
            <HSelect
              :model-value="state.settings.value.llmModel"
              :aria-label="tt('Server default classifier model')"
              :options="[{ value: '', label: SLOT_LABEL }, ...models]"
              @update:model-value="state.setDefaultModel"
            />
          </div>
          <span
            v-else
            class="text-[12px]"
            :class="defaultModel ? 'text-strong' : 'text-muted'"
          >{{ modelLabel(defaultModel) }}</span>
        </div>
        <div class="flex items-center gap-3 max-sm:flex-col max-sm:items-stretch max-sm:gap-1">
          <span :class="label">{{ tt('For me') }}</span>
          <div class="w-[260px] max-sm:w-full">
            <HSelect
              :model-value="my.mine.llmModel"
              :aria-label="tt('My classifier model')"
              :options="[{ value: '', label: `Follow server default (${modelLabel(defaultModel)})` }, ...models]"
              @update:model-value="state.setMine({ llmModel: $event })"
            />
          </div>
        </div>
      </div>

      <!-- The caller's classifier, including none (the switches on the classifiers above set the same) -->
      <div class="flex flex-col gap-1.5">
        <span class="text-[12px] text-strong">{{ tt('Classifier') }}</span>
        <div class="flex items-center gap-3 max-sm:flex-col max-sm:items-stretch max-sm:gap-1">
          <span :class="label">{{ tt('For me') }}</span>
          <div class="w-[260px] max-sm:w-full">
            <HSelect
              :model-value="myClassifier"
              :aria-label="tt('My classifier')"
              :options="classifierOptions"
              @update:model-value="setMyClassifier"
            />
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
