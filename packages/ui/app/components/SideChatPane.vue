<script setup lang="ts">
// Side chat pane (docs/design/ui/SideChat.dc.html): the session's side chats as tabs, each a read-only
// conversation that sees the main session but never changes it (lib/sidechat). Answers stream with a caret and
// Stop; the footer picks the chat's model and effort like the main composer (a new chat starts with the main
// session's); "Start a side chat" on a transcript selection opens a new chat quoting it.
import { usePanes } from '~/composables/usePanes'
import type { EffortName, SessionState } from '~/lib/session/types'
import { SideChatController, sideChatDefaults } from '~/lib/sidechat/controller'
import type { SideChatMessage } from '~/lib/sidechat/types'

const props = defineProps<{ session: SessionState, menu: string }>()

const { closePane } = usePanes()
const store = useSessionStore()

// utils/icons.ts carries only the outer contour of this glyph; the tab uses the full outline.
const CHAT_ICON = 'M10.094 19.841c-.775.613-1.605 1.051-2.609 1.136c-.521.044-1.056.033-1.35-.476c-.262-.451-.052-1.468-.166-2.031l-.069-.228l-.022-.02l-.513-.359l-.583-.476c-1.124-.985-2.028-2.289-2.456-3.728a7.5 7.5 0 0 1-.036-4.2c1.008-3.589 4.55-5.841 8.082-6.347a11.54 11.54 0 0 1 5.496.546c3.872 1.397 6.776 4.926 6.004 9.203c-.249 1.376-.925 2.645-1.832 3.698c-2.417 2.806-6.376 3.842-9.946 3.282m-2.08-1.026c.579-.265 1.075-.873 1.693-.984c.354-.063.726.05 1.069.093l.605.056c1.483.093 2.992-.145 4.358-.736c3.032-1.309 5.196-4.426 3.872-7.744c-.395-.991-1.071-1.854-1.888-2.535c-3.527-2.94-9.753-2.604-12.572 1.182c-.834 1.12-1.265 2.505-1.121 3.902c.153 1.49.95 2.832 2.055 3.82c.343.307.728.53 1.081.822c.645.533.795 1.332.848 2.124'
const EMPTY = {
  title: 'No messages yet',
  body: 'A side chat sees this session but never changes it: no files, no tools, nothing added to the main thread.'
}

const sessionId = computed(() => props.session.meta.id)
const remote = computed(() => props.session.meta.remote)

function makeController(id: string) {
  const api = store.transport?.sideChats()
  if (!api) return null
  return reactive(new SideChatController(
    api,
    id,
    // The main composer's model and effort right now.
    () => sideChatDefaults(store.sessions[id]?.meta.model ?? '', store.effort[id], store.models),
    () => store.models
  ))
}

const ctl = shallowRef(makeController(sessionId.value))
const chat = computed(() => ctl.value?.selected)
const streaming = computed(() => !!chat.value?.streamingId)

// The pending "Start a side chat" quote, taken once the side chats are loaded.
const { sideChat } = useQuoteRequests()
const sideInput = ref<HTMLInputElement | null>(null)

async function startFromQuote() {
  const q = sideChat.value
  const c = ctl.value
  if (!q || !c || c.loading) return
  sideChat.value = ''
  await c.newChat(quoteLine(q, 600))
  nextTick(() => sideInput.value?.focus())
}

async function load() {
  const c = ctl.value
  if (!c || remote.value) return
  await c.load()
  await startFromQuote()
}

watch(sessionId, (id) => {
  ctl.value = makeController(id)
  void load()
})
watch(sideChat, () => void startFromQuote())
// The pane can open before the store is connected.
watch(() => store.transport, () => {
  if (ctl.value) return
  ctl.value = makeController(sessionId.value)
  void load()
})
onMounted(() => void load())

const draft = computed({
  get: () => chat.value?.draft ?? '',
  set: (v: string) => {
    if (chat.value) chat.value.draft = v
  }
})
// Typing before any chat exists keeps the text here until the first question creates one.
const orphanDraft = ref('')
const inputText = computed({
  get: () => (chat.value ? draft.value : orphanDraft.value),
  set: (v: string) => {
    if (chat.value) draft.value = v
    else orphanDraft.value = v
  }
})

async function send() {
  const c = ctl.value
  if (!c || streaming.value || !inputText.value.trim()) return
  if (!c.selected) {
    const text = orphanDraft.value
    const v = await c.newChat()
    if (!v) return
    v.draft = text
    orphanDraft.value = ''
  }
  await c.ask()
}

function closeChat(id: string) {
  const c = ctl.value
  if (!c) return
  void c.close(id).then(() => {
    if (!c.chats.length) closePane('sidechat')
  })
}

// Footer: the selected chat's model and effort, or the defaults a new chat would get.
const defaults = computed(() => sideChatDefaults(props.session.meta.model, store.effort[sessionId.value], store.models))
const modelId = computed(() => chat.value?.info.model || defaults.value.model)
const model = computed(() => store.models.find(m => m.id === modelId.value))
const efforts = computed(() => modelEfforts(model.value))
const effort = computed(() => fitEffort(chat.value?.info.effort ?? defaults.value.effort ?? 'medium', efforts.value))
const modelOpen = ref(false)
const effortOpen = ref(false)
const BTN = 'flex shrink-0 items-center gap-[5px] border px-1.5 py-0.5 text-[11px] whitespace-nowrap text-muted hover:text-toned'

async function pickModel(id: string) {
  modelOpen.value = false
  const c = ctl.value
  if (!c) return
  if (!c.selected && !(await c.newChat())) return
  await c.setModel(id)
}

async function pickEffort(e: EffortName) {
  const c = ctl.value
  if (!c) return
  if (!c.selected && !(await c.newChat())) return
  await c.setEffort(e)
}

function meta(m: SideChatMessage) {
  const parts = [m.model]
  if (m.usage) parts.push(`${formatTokens(m.usage.inputTokens + m.usage.cacheReadInputTokens + m.usage.cacheCreationInputTokens + m.usage.outputTokens)} tok`)
  return parts.filter(Boolean).join(' · ')
}

function copy(m: SideChatMessage) {
  navigator.clipboard?.writeText(m.text).catch(() => {})
}

const thread = ref<HTMLElement | null>(null)
watch(() => chat.value?.messages.map(m => m.text.length).join(), () => {
  nextTick(() => {
    if (thread.value) thread.value.scrollTop = thread.value.scrollHeight
  })
})
</script>

<template>
  <section
    aria-label="Side chat"
    class="relative flex h-full flex-col overflow-hidden bg-default"
  >
    <div class="flex h-[44px] shrink-0 items-stretch border-b border-line pr-1.5">
      <div
        role="tablist"
        aria-label="Side chats"
        class="flex min-w-0 items-stretch overflow-x-auto"
      >
        <PaneTab
          v-for="c in ctl?.chats ?? []"
          :key="c.info.id"
          :selected="c.info.id === ctl?.selectedId"
          class="max-w-[220px] gap-2 px-3"
          @select="ctl?.select(c.info.id)"
          @close="closeChat(c.info.id)"
        >
          <svg
            viewBox="0 0 24 24"
            aria-hidden="true"
            class="size-[13px] shrink-0 fill-current"
          ><path :d="CHAT_ICON" /></svg>
          <span class="truncate">{{ c.info.title }}</span>
        </PaneTab>
        <button
          v-if="!remote"
          type="button"
          aria-label="New side chat"
          title="New side chat"
          class="flex w-9 shrink-0 items-center justify-center border-r border-line text-muted hover:text-strong"
          @click="ctl?.newChat()"
        >
          <HIcon name="plus" />
        </button>
      </div>
      <span class="flex-1" />
      <div class="flex items-center gap-[2px]">
        <PaneBtn
          icon="close"
          label="Close side chat"
          @click="closePane('sidechat')"
        />
      </div>
    </div>

    <div class="flex min-h-0 flex-1 flex-col overflow-hidden">
      <div
        v-if="remote"
        class="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center"
      >
        <span class="text-[13.5px] text-strong">Side chats run on the session's machine</span>
        <span class="max-w-[320px] text-[12px] leading-[1.55] text-muted">This session belongs to another machine. Open a side chat from that machine's app.</span>
      </div>

      <div
        v-else-if="ctl?.loading && !ctl.chats.length"
        class="flex flex-1 items-center justify-center"
      >
        <span class="text-[12px] text-muted">Loading side chats…</span>
      </div>

      <div
        v-else-if="!chat?.messages.length"
        class="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center"
      >
        <span class="text-[13.5px] text-strong">{{ EMPTY.title }}</span>
        <span class="max-w-[320px] text-[12px] leading-[1.55] text-muted">{{ EMPTY.body }}</span>
      </div>

      <div
        v-else
        ref="thread"
        class="flex min-h-0 flex-1 flex-col gap-3.5 overflow-y-auto px-4 py-[18px] text-[13px] leading-[1.6]"
      >
        <template
          v-for="m in chat.messages"
          :key="m.id"
        >
          <div
            v-if="m.role === 'user'"
            class="flex max-w-[85%] flex-col gap-1.5 self-end border border-line bg-hover px-[11px] py-2 whitespace-pre-wrap text-strong"
          >
            <span
              v-if="m.quote"
              class="border-l-2 border-line-strong pl-[9px] text-[12px] text-muted"
            >{{ m.quote }}</span>
            <span>{{ m.text }}</span>
          </div>
          <div
            v-else
            class="flex flex-col gap-2 text-toned"
          >
            <MarkdownBlock
              v-if="m.text || m.id === chat.streamingId"
              :text="m.text"
              :streaming="m.id === chat.streamingId"
            />
            <span
              v-if="m.error"
              class="text-[12px] text-bad"
            >The answer failed: {{ m.error }}</span>
            <span
              v-else-if="m.stopped"
              class="text-[11px] text-muted"
            >Stopped</span>
            <div
              v-if="m.id !== chat.streamingId && m.text"
              class="flex items-center gap-1 text-[11px] text-muted"
            >
              <button
                type="button"
                class="px-1.5 py-[2px] text-[11px] text-muted hover:text-strong"
                @click="copy(m)"
              >
                Copy
              </button>
              <span class="flex-1" />{{ meta(m) }}
            </div>
          </div>
        </template>
      </div>
    </div>

    <div
      v-if="!remote"
      class="flex shrink-0 flex-col gap-1.5 px-3 pt-2.5 pb-3"
    >
      <span
        v-if="chat?.error || ctl?.error"
        role="alert"
        class="text-[11.5px] text-bad"
      >{{ chat?.error || ctl?.error }}</span>
      <div
        class="border bg-surface"
        :class="inputText.trim() ? 'border-accent' : 'border-line-strong'"
      >
        <div
          v-if="chat?.quote"
          class="mx-2 mt-2 flex items-start gap-2 border-l-2 border-accent bg-hover px-2 py-1.5 text-[12px] text-muted"
        >
          <HIcon
            name="quote"
            :size="13"
            class="mt-[2px] text-accent"
          />
          <span class="flex-1 leading-[1.5]">{{ chat.quote }}</span>
          <button
            type="button"
            aria-label="Remove quote"
            class="mt-[3px] text-muted hover:text-strong"
            @click="chat.quote = ''"
          >
            <HIcon
              name="close"
              :size="11"
            />
          </button>
        </div>
        <div class="flex min-h-[42px] items-center gap-2 pr-2 pl-3">
          <input
            ref="sideInput"
            v-model="inputText"
            type="text"
            :disabled="streaming"
            :placeholder="streaming ? 'Answering…' : 'Ask a quick question'"
            aria-label="Ask a quick question"
            class="min-w-0 flex-1 bg-transparent text-[13px] text-strong outline-none placeholder:text-muted"
            @keydown.enter.prevent="send"
          >
          <button
            v-if="streaming"
            type="button"
            aria-label="Stop"
            title="Stop"
            class="flex size-[26px] items-center justify-center bg-strong"
            @click="ctl?.stop()"
          >
            <span class="size-[9px] bg-ground" />
          </button>
          <button
            v-else
            type="button"
            aria-label="Send"
            title="Send"
            class="flex size-[26px] items-center justify-center"
            :class="inputText.trim() ? 'bg-accent text-on-accent' : 'bg-raised text-disabled'"
            @click="send"
          >
            <HIcon
              name="enter"
              :size="14"
            />
          </button>
        </div>
      </div>
      <div class="flex min-w-0 items-center gap-0.5 text-[11px] whitespace-nowrap text-muted">
        <span class="min-w-0 flex-1 truncate">read-only · sees the main session</span>

        <UPopover
          v-model:open="modelOpen"
          :content="{ side: 'top', align: 'end', sideOffset: 6 }"
        >
          <button
            type="button"
            aria-label="Side chat model"
            :class="[BTN, modelOpen ? 'border-accent' : 'border-transparent']"
          >
            <span class="text-strong">{{ modelId }}</span><span
              v-if="model?.provider"
              class="max-sm:hidden"
            >· {{ model.provider.toLowerCase().replace(' · local', '') }}</span>
            <HIcon
              name="down"
              :size="11"
            />
          </button>
          <template #content>
            <div
              role="listbox"
              aria-label="Side chat model"
              class="w-[380px] max-w-[calc(100vw-32px)] border border-accented bg-hover text-[12.5px]"
            >
              <ModelList
                :selected="modelId"
                @pick="pickModel"
              />
            </div>
          </template>
        </UPopover>

        <UPopover
          v-if="effort"
          v-model:open="effortOpen"
          :content="{ side: 'top', align: 'end', sideOffset: 6 }"
        >
          <button
            type="button"
            aria-label="Side chat effort"
            :class="[BTN, effortOpen ? 'border-accent' : 'border-transparent', 'max-sm:hidden']"
          >
            {{ EFFORT_LABEL[effort] }}<HIcon
              name="down"
              :size="11"
            />
          </button>
          <template #content>
            <div class="border border-accented bg-hover">
              <EffortSlider
                :model-value="effort"
                :levels="efforts"
                @update:model-value="pickEffort"
              />
            </div>
          </template>
        </UPopover>
      </div>
    </div>
  </section>
</template>
