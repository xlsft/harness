<script setup lang="ts">
// The Create PR button's dialog: the client reads the branch and has the agent draft the title and body; the user edits
// them and sends back what they kept. Pushing, choosing the provider and binding the PR are the client's business.
import type { PullRequestDraft } from '~/lib/transport/types'
import { openExternal } from '~/lib/desktop'

const props = defineProps<{ sessionId: string }>()
const open = defineModel<boolean>('open', { default: false })

const store = useSessionStore()
const toast = useToast()

const draft = ref<PullRequestDraft | null>(null)
const title = ref('')
const body = ref('')
const base = ref('')
const asDraft = ref(false)
const watch_ = ref(false)
const loading = ref(false)
const creating = ref(false)
const error = ref('')

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function load() {
  loading.value = true
  error.value = ''
  draft.value = null
  try {
    const d = await store.draftPullRequest(props.sessionId)
    draft.value = d
    title.value = d.title
    body.value = d.body
    base.value = d.baseBranch
  } catch (e) {
    error.value = message(e)
  } finally {
    loading.value = false
  }
}

watch(open, (v) => {
  if (v) void load()
})

const facts = computed(() => {
  const d = draft.value
  if (!d) return ''
  return [`${d.commits} ${d.commits === 1 ? 'commit' : 'commits'}`, d.needsPush && 'the branch is pushed first', d.dirty && 'uncommitted changes stay out of it'].filter(Boolean).join('; ')
})

const canCreate = computed(() => !!draft.value?.provider && !!title.value.trim() && !creating.value)

async function create() {
  if (!canCreate.value) return
  creating.value = true
  error.value = ''
  try {
    const pr = await store.createPullRequest(props.sessionId, { title: title.value, body: body.value, baseBranch: base.value, draft: asDraft.value, monitor: watch_.value })
    open.value = false
    toast.add({ title: pr.existed ? `PR ${pr.number} was already open` : `Opened PR ${pr.number}`, description: pr.title, actions: [{ label: 'Open', onClick: () => void openExternal(pr.url) }] })
  } catch (e) {
    error.value = message(e)
  } finally {
    creating.value = false
  }
}
</script>

<template>
  <UModal
    v-model:open="open"
    title="Create pull request"
    :description="draft ? `${draft.headBranch} into ${draft.baseBranch} in ${draft.repositoryKey}` : 'Reading the branch'"
  >
    <template #body>
      <div class="flex flex-col gap-3">
        <p
          v-if="error"
          role="alert"
          class="m-0 border border-bad px-3 py-2 text-[12px] text-bad"
        >
          {{ error }}
        </p>
        <p
          v-if="loading"
          class="m-0 flex items-center gap-2 text-[12px] text-muted"
        >
          <HLoader /> The agent is drafting the title and description
        </p>
        <template v-if="draft">
          <p
            v-if="draft.note"
            class="m-0 border border-line px-3 py-2 text-[12px] text-muted"
          >
            {{ draft.note }}
          </p>
          <label class="flex flex-col gap-1 text-[12px] text-muted">
            Title
            <input
              v-model="title"
              aria-label="Title"
              class="border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent"
            >
          </label>
          <label class="flex flex-col gap-1 text-[12px] text-muted">
            Description
            <textarea
              v-model="body"
              aria-label="Description"
              rows="10"
              class="resize-y border border-line bg-transparent px-2 py-1 font-mono text-[12px] text-strong outline-none focus:border-accent"
            />
          </label>
          <label class="flex flex-col gap-1 text-[12px] text-muted">
            Base branch
            <input
              v-model="base"
              aria-label="Base branch"
              class="border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent"
            >
          </label>
          <div class="flex items-center gap-2 text-[12px] text-muted">
            <HToggle
              v-model="asDraft"
              label="Open as a draft"
            /> Open as a draft
          </div>
          <div class="flex items-center gap-2 text-[12px] text-muted">
            <HToggle
              v-model="watch_"
              label="Watch it for auto-fix"
            /> Watch it for auto-fix
          </div>
          <p class="m-0 text-[11.5px] text-muted">
            {{ facts }}
          </p>
        </template>
      </div>
    </template>
    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <HBtn
          size="md"
          tone="muted"
          @click="open = false"
        >
          Cancel
        </HBtn>
        <HBtn
          variant="primary"
          size="md"
          :disabled="!canCreate"
          @click="create"
        >
          {{ creating ? 'Creating' : 'Create' }}
        </HBtn>
      </div>
    </template>
  </UModal>
</template>
