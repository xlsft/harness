<script setup lang="ts">
// DemoSkillSave: the skill the agent drafted from a demo recording. The user edits the SKILL.md, picks where it goes
// (the project's .harness/skills or their personal skills) and saves it, or discards it with a note for the agent.
// The client writes the file only on Save; the card is the approval. Resolved by DemoSkillReviewResolved, then one
// muted line.
import type { DemoSkillReviewItem } from '~/lib/session/types'

const props = defineProps<{ item: DemoSkillReviewItem, sessionId: string }>()
const store = useSessionStore()

const pending = computed(() => props.item.state === 'pending')
const remote = computed(() => !!store.sessions[props.sessionId]?.meta.remote)
const content = ref(props.item.content)
const scope = ref<string>(props.item.paths[props.item.scope] ? props.item.scope : props.item.scope === 'project' ? 'personal' : 'project')
const note = ref('')
const sending = ref(false)
const error = ref('')

const scopes = computed(() => (['project', 'personal'] as const).filter(s => props.item.paths[s]).map(s => ({
  value: s,
  label: s === 'project' ? tt('This project') : tt('My skills'),
  hint: props.item.paths[s]
})))

async function answer(save: boolean) {
  if (!pending.value || sending.value) return
  sending.value = true
  error.value = ''
  try {
    await store.respondToDemoSkillReview(props.sessionId, props.item.id, { save, content: content.value, scope: scope.value === 'personal' ? 'personal' : 'project', note: note.value })
  } catch (e) {
    error.value = e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
  } finally {
    sending.value = false
  }
}

const summary = computed(() => {
  switch (props.item.state) {
    case 'saved': return tt('Skill {0} saved to {1}', [props.item.name, props.item.paths[props.item.scope] || props.item.scope])
    case 'discarded': return tt('Skill {0} discarded', [props.item.name])
    default: return tt('Skill {0} was not reviewed', [props.item.name])
  }
})
</script>

<template>
  <div
    v-if="pending"
    class="border border-accent bg-surface"
  >
    <div class="flex items-center gap-[9px] border-b border-default px-3 py-2 text-[12.5px]">
      <HIcon
        name="page"
        class="text-accent"
      />
      <span class="text-strong">{{ tt('Save this skill from your demo?') }}</span>
      <span class="truncate text-muted">{{ item.name }}</span>
      <span class="flex-1" />
      <span class="text-[11px] text-muted">{{ tt('waiting') }}</span>
    </div>
    <div class="flex flex-col gap-2.5 px-3 py-2.5 text-[12.5px]">
      <span class="text-toned">{{ tt('Check the steps and fix anything the agent got wrong. Nothing is written until you save.') }}</span>
      <textarea
        v-model="content"
        :aria-label="tt('Skill')"
        :disabled="remote || sending"
        spellcheck="false"
        rows="14"
        class="min-h-[160px] w-full resize-y border border-default bg-default px-2.5 py-2 font-mono text-[12px] text-strong outline-none focus:border-accent"
      />
      <div
        v-if="scopes.length"
        class="flex flex-wrap items-center gap-2"
      >
        <span class="text-muted">{{ tt('Save to') }}</span>
        <HSelect
          v-model="scope"
          :options="scopes"
          :aria-label="tt('Save to')"
          :disabled="remote || sending"
          class="min-w-[180px]"
        />
        <span class="min-w-0 truncate text-[11.5px] text-muted">{{ item.paths[scope === 'personal' ? 'personal' : 'project'] }}</span>
      </div>
      <input
        v-model="note"
        type="text"
        :placeholder="tt('Note for the agent (optional)')"
        :aria-label="tt('Note for the agent')"
        :disabled="remote || sending"
        class="h-[30px] border border-default bg-default px-2.5 text-[12.5px] text-strong outline-none placeholder:text-muted focus:border-accent"
      >
      <p
        v-if="error"
        role="alert"
        class="m-0 text-[12px] text-bad"
      >
        {{ error }}
      </p>
      <span
        v-if="remote"
        class="text-[12px] text-muted"
      >{{ tt('Only someone at the machine running this session can answer.') }}</span>
      <div
        v-else
        class="flex flex-wrap gap-2"
      >
        <HBtn
          variant="primary"
          size="lg"
          :disabled="sending || !content.trim() || !scopes.length"
          @click="answer(true)"
        >
          {{ tt('Save skill') }}
        </HBtn>
        <HBtn
          size="lg"
          :disabled="sending"
          @click="answer(false)"
        >
          {{ tt('Discard') }}
        </HBtn>
      </div>
    </div>
  </div>
  <div
    v-else
    class="flex items-center gap-[9px] text-[12px] text-muted"
  >
    <HIcon
      name="page"
      :size="14"
      :class="item.state === 'saved' ? 'text-ok' : 'text-muted'"
    />
    <span class="truncate">{{ summary }}</span>
  </div>
</template>
