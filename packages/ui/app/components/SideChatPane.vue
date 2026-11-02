<script setup lang="ts">
// Side chat pane (docs/design/ui/SideChat.dc.html). Threads are local demo state: Enter appends the
// question and streams a canned answer (caret + Stop), like the design's streaming state.
// URL `menu` values: empty, quote (started from a selection), streaming; default is the conversation.
import { usePanes } from '~/composables/usePanes'
import type { SessionState } from '~/lib/session/types'
import {
  CANNED_ANSWERS, SIDE_EMPTY, SIDE_MODEL, SIDE_QUOTE, STREAM_CONTINUATION, sideChats,
  type SideChat, type SideMessage
} from '~/lib/fixtures/sidechat'

const props = defineProps<{ session: SessionState, menu: string }>()

const { setPane } = usePanes()

// utils/icons.ts carries only the outer contour of this glyph; the tab uses the full outline.
const CHAT_ICON = 'M10.094 19.841c-.775.613-1.605 1.051-2.609 1.136c-.521.044-1.056.033-1.35-.476c-.262-.451-.052-1.468-.166-2.031l-.069-.228l-.022-.02l-.513-.359l-.583-.476c-1.124-.985-2.028-2.289-2.456-3.728a7.5 7.5 0 0 1-.036-4.2c1.008-3.589 4.55-5.841 8.082-6.347a11.54 11.54 0 0 1 5.496.546c3.872 1.397 6.776 4.926 6.004 9.203c-.249 1.376-.925 2.645-1.832 3.698c-2.417 2.806-6.376 3.842-9.946 3.282m-2.08-1.026c.579-.265 1.075-.873 1.693-.984c.354-.063.726.05 1.069.093l.605.056c1.483.093 2.992-.145 4.358-.736c3.032-1.309 5.196-4.426 3.872-7.744c-.395-.991-1.071-1.854-1.888-2.535c-3.527-2.94-9.753-2.604-12.572 1.182c-.834 1.12-1.265 2.505-1.121 3.902c.153 1.49.95 2.832 2.055 3.82c.343.307.728.53 1.081.822c.645.533.795 1.332.848 2.124'

function initialChats(): SideChat[] {
  const all = sideChats()
  if (props.menu === 'empty' || props.menu === 'quote') return [all[1]!]
  if (props.menu === 'streaming') return [all[0]!]
  return all
}

const chats = ref<SideChat[]>(initialChats())
const selected = ref(chats.value[0]!.id)
const chat = computed(() => chats.value.find(c => c.id === selected.value) ?? chats.value[0])

const input = ref(props.menu === 'quote' ? 'Why this and not plain gRPC?' : '')
const quote = ref(props.menu === 'quote' ? SIDE_QUOTE : '')

let timer: ReturnType<typeof setInterval> | null = null
let streamingMsg: SideMessage | null = null
const streaming = ref(false)
let answers = 0
let counter = 0

function stop() {
  if (timer) clearInterval(timer)
  timer = null
  if (streamingMsg) streamingMsg.streaming = false
  streamingMsg = null
  streaming.value = false
}

function stream(msg: SideMessage, rest: string, meta: boolean) {
  stop()
  msg.streaming = true
  streamingMsg = msg
  streaming.value = true
  let i = 0
  timer = setInterval(() => {
    const step = 2 + Math.floor(Math.random() * 4)
    msg.text += rest.slice(i, i + step)
    i += step
    if (i >= rest.length) {
      if (meta) msg.meta = `${SIDE_MODEL.name} · ${formatTokens(Math.max(1, Math.round(msg.text.length / 4)))} tok`
      stop()
    }
  }, 35)
}

function send() {
  const text = input.value.trim()
  const c = chat.value
  if (!text || !c || streaming.value) return
  counter += 1
  c.messages.push({ id: `u-${counter}`, role: 'user', text, quote: quote.value || undefined })
  c.messages.push({ id: `a-${counter}`, role: 'assistant', text: '' })
  const msg = c.messages[c.messages.length - 1]!
  input.value = ''
  quote.value = ''
  stream(msg, CANNED_ANSWERS[answers % CANNED_ANSWERS.length]!, true)
  answers += 1
}

function addChat() {
  counter += 1
  const id = `chat-${counter}`
  chats.value.push({ id, title: 'Side chat', messages: [] })
  selected.value = id
}

function closeChat(id: string) {
  const i = chats.value.findIndex(c => c.id === id)
  if (i < 0) return
  if (chats.value[i]!.messages.includes(streamingMsg as SideMessage)) stop()
  chats.value.splice(i, 1)
  if (!chats.value.length) {
    setPane(null)
    return
  }
  if (selected.value === id) selected.value = chats.value[Math.max(0, i - 1)]!.id
}

function segments(text: string) {
  return text.split('`').map((t, i) => ({ t, code: i % 2 === 1 }))
}

function plain(m: SideMessage) {
  return [m.text, ...(m.points ?? []).map((p, i) => `${i + 1}. ${p}`), m.outro ?? ''].filter(Boolean).join('\n').replaceAll('`', '')
}

function copy(m: SideMessage) {
  navigator.clipboard?.writeText(plain(m)).catch(() => {})
}

const thread = ref<HTMLElement | null>(null)
watch(() => chat.value?.messages.map(m => m.text.length).join(), () => {
  nextTick(() => {
    if (thread.value) thread.value.scrollTop = thread.value.scrollHeight
  })
})

onMounted(() => {
  if (props.menu !== 'streaming') return
  const last = chat.value?.messages.at(-1)
  if (last) stream(last, STREAM_CONTINUATION, false)
})

onBeforeUnmount(stop)
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
        class="flex min-w-0 items-stretch overflow-hidden"
      >
        <PaneTab
          v-for="c in chats"
          :key="c.id"
          :selected="c.id === selected"
          class="max-w-[220px] gap-2 px-3"
          @select="selected = c.id"
          @close="closeChat(c.id)"
        >
          <svg
            viewBox="0 0 24 24"
            aria-hidden="true"
            class="size-[13px] shrink-0 fill-current"
          ><path :d="CHAT_ICON" /></svg>
          <span class="truncate">{{ c.title }}</span>
        </PaneTab>
        <button
          type="button"
          aria-label="New side chat"
          title="New side chat"
          class="flex w-9 shrink-0 items-center justify-center border-r border-line text-muted hover:text-white"
          @click="addChat"
        >
          <HIcon name="plus" />
        </button>
      </div>
      <span class="flex-1" />
      <div class="flex items-center gap-[2px]">
        <PaneBtn
          icon="full"
          label="Maximize"
        />
        <PaneBtn
          icon="close"
          label="Close side chat"
          @click="setPane(null)"
        />
      </div>
    </div>

    <div class="flex min-h-0 flex-1 flex-col overflow-hidden">
      <div
        v-if="!chat?.messages.length"
        class="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center"
      >
        <span class="text-[13.5px] text-white">{{ SIDE_EMPTY.title }}</span>
        <span class="max-w-[320px] text-[12px] leading-[1.55] text-muted">{{ SIDE_EMPTY.body }}</span>
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
            class="flex max-w-[85%] flex-col gap-1.5 self-end border border-line bg-hover px-[11px] py-2 text-white"
          >
            <span
              v-if="m.quote"
              class="border-l-2 border-line-strong pl-[9px] text-[12px] text-muted"
            >{{ m.quote }}</span>
            <span>{{ m.text }}</span>
          </div>
          <div
            v-else-if="m.points || m.meta"
            class="flex flex-col gap-2 text-toned"
          >
            <p class="m-0">
              <span
                v-for="(s, i) in segments(m.text)"
                :key="i"
                :class="s.code && 'border border-line bg-hover px-1 text-[12px] text-white'"
              >{{ s.t }}</span><PaneCaret
                v-if="m.streaming && !m.points"
                class="ml-[2px] h-[14px] w-[7px] align-[-2px]"
              />
            </p>
            <div
              v-for="(p, i) in m.points"
              :key="i"
              class="flex gap-2"
            >
              <span class="text-accent">{{ i + 1 }}</span><span>{{ p }}</span>
            </div>
            <p
              v-if="m.outro"
              class="m-0"
            >
              {{ m.outro }}
            </p>
            <div
              v-if="m.meta && !m.streaming"
              class="flex items-center gap-1 text-[11px] text-muted"
            >
              <button
                type="button"
                class="flex items-center gap-[5px] border border-line px-2 py-[2px] text-[11px] text-white hover:bg-hover"
              >
                <HIcon
                  name="up"
                  :size="11"
                />Send to main chat
              </button>
              <button
                type="button"
                class="px-1.5 py-[2px] text-[11px] text-muted hover:text-white"
                @click="copy(m)"
              >
                Copy
              </button>
              <span class="flex-1" />{{ m.meta }}
            </div>
          </div>
          <div
            v-else
            class="text-toned"
          >
            <span
              v-for="(s, i) in segments(m.text)"
              :key="i"
              :class="s.code && 'border border-line bg-hover px-1 text-[12px] text-white'"
            >{{ s.t }}</span><PaneCaret
              v-if="m.streaming"
              class="ml-[2px] h-[14px] w-[7px] align-[-2px]"
            />
          </div>
        </template>
      </div>
    </div>

    <div class="flex shrink-0 flex-col gap-1.5 px-3 pt-2.5 pb-3">
      <div
        class="border bg-surface"
        :class="input.trim() ? 'border-accent' : 'border-line-strong'"
      >
        <div
          v-if="quote"
          class="mx-2 mt-2 flex items-start gap-2 border-l-2 border-accent bg-hover px-2 py-1.5 text-[12px] text-muted"
        >
          <HIcon
            name="quote"
            :size="13"
            class="mt-[2px] text-accent"
          />
          <span class="flex-1 leading-[1.5]">{{ quote }}</span>
          <button
            type="button"
            aria-label="Remove quote"
            class="mt-[3px] text-muted hover:text-white"
            @click="quote = ''"
          >
            <HIcon
              name="close"
              :size="11"
            />
          </button>
        </div>
        <div class="flex min-h-[42px] items-center gap-2 pr-2 pl-3">
          <input
            v-model="input"
            type="text"
            :disabled="streaming"
            :placeholder="streaming ? 'Answering…' : 'Ask a quick question'"
            aria-label="Ask a quick question"
            class="min-w-0 flex-1 bg-transparent text-[13px] text-white outline-none placeholder:text-muted"
            @keydown.enter.prevent="send"
          >
          <button
            v-if="streaming"
            type="button"
            aria-label="Stop"
            title="Stop"
            class="flex size-[26px] items-center justify-center bg-white"
            @click="stop"
          >
            <span class="size-[9px] bg-black" />
          </button>
          <button
            v-else
            type="button"
            aria-label="Send"
            title="Send"
            class="flex size-[26px] items-center justify-center"
            :class="input.trim() ? 'bg-accent text-white' : 'bg-raised text-disabled'"
            @click="send"
          >
            <HIcon
              name="enter"
              :size="14"
            />
          </button>
        </div>
      </div>
      <div class="flex gap-3 text-[11px] text-muted">
        <span>read-only · sees the main session</span><span class="flex-1" /><span><span class="text-white">{{ SIDE_MODEL.name }}</span> · {{ SIDE_MODEL.provider }}</span>
      </div>
    </div>
  </section>
</template>
