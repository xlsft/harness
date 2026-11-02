<script setup lang="ts">
// Artifact pane (docs/design/ui/ArtifactPane.dc.html), on demo fixtures. The page keeps the
// artifact's own look (IBM Plex Sans + JetBrains Mono on warm black). URL `menu` values:
// title (title menu), comments (comment thread), updating (the agent republishing). "Refresh" in
// the title menu also shows the updating state for a few seconds.
import { usePanes } from '~/composables/usePanes'
import type { SessionState } from '~/lib/session/types'
import { ARTIFACT, ARTIFACT_MENU, ARTIFACT_THREAD } from '~/lib/fixtures/artifact'

const props = defineProps<{ session: SessionState, menu: string }>()

useHead({
  link: [{
    rel: 'stylesheet',
    href: 'https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;600;800&family=IBM+Plex+Sans:wght@400;600&display=swap'
  }]
})

const { setPane } = usePanes()

const menuOpen = ref(props.menu === 'title')
const thread = ref(props.menu === 'comments')
const updating = ref(props.menu === 'updating')
watch(() => props.menu, (m) => {
  menuOpen.value = m === 'title'
  thread.value = m === 'comments'
  updating.value = m === 'updating'
})

const reply = ref('')
let timer: ReturnType<typeof setTimeout> | null = null

function choose(label: string) {
  menuOpen.value = false
  if (label === 'Refresh') {
    updating.value = true
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => {
      updating.value = false
    }, 4000)
  }
}

onBeforeUnmount(() => {
  if (timer) clearTimeout(timer)
})

const mono = '\'JetBrains Mono\', monospace'
</script>

<template>
  <section
    aria-label="Artifact"
    class="relative flex h-full flex-col overflow-hidden bg-default"
  >
    <div class="flex h-[44px] shrink-0 items-center gap-1 border-b border-line pr-1.5 pl-2">
      <button
        type="button"
        aria-haspopup="menu"
        :aria-expanded="menuOpen"
        data-menu-anchor
        class="flex h-[30px] items-center gap-1.5 border px-2.5 text-[12.5px] text-white"
        :class="menuOpen ? 'border-line-strong bg-raised' : 'border-transparent hover:bg-hover'"
        @click="menuOpen = !menuOpen"
      >
        {{ ARTIFACT.title }}<HIcon
          name="down"
          :size="12"
          class="text-muted"
        />
      </button>
      <span class="flex-1" />
      <span class="px-1 text-[11.5px] text-muted">{{ ARTIFACT.author }}</span>
      <button
        type="button"
        class="flex h-[30px] items-center gap-1.5 bg-raised px-[9px] text-[12px] text-white"
        @click="setPane('sidechat')"
      >
        <HIcon
          name="ai"
          :size="13"
          class="text-accent"
        />Chat
      </button>
      <button
        type="button"
        class="flex h-[30px] items-center gap-1.5 px-[9px] text-[12px] text-white hover:bg-hover"
      >
        <HIcon
          name="link"
          :size="13"
        />Share
      </button>
      <button
        type="button"
        aria-label="Comments"
        title="Comments"
        :aria-expanded="thread"
        class="flex h-[30px] items-center gap-1 px-[7px] text-[11px] text-white"
        :class="thread ? 'bg-raised' : 'hover:bg-hover'"
        @click="thread = !thread"
      >
        <HIcon name="comment" /><span
          v-if="thread"
          class="text-accent"
        >{{ ARTIFACT_THREAD.comments.length }}</span><HIcon
          name="down"
          :size="11"
          class="text-muted"
        />
      </button>
      <span class="mx-[2px] h-[18px] w-px bg-line" />
      <PaneBtn
        icon="full"
        label="Maximize"
      />
      <PaneBtn
        icon="close"
        label="Close artifact"
        @click="setPane(null)"
      />
    </div>

    <div
      v-if="updating"
      class="flex items-center gap-[9px] border-b border-line bg-surface px-3 py-1.5 text-[12px] text-muted"
    >
      <HIcon
        name="loading"
        :size="14"
        spin
        class="text-accent"
      />
      <span class="text-white">The agent is publishing a new version</span>{{ ARTIFACT.version.from }} → {{ ARTIFACT.version.to }}<span class="flex-1" />
      <a
        href="#"
        class="text-accent hover:text-accent-hover"
        @click.prevent
      >View changes</a>
    </div>

    <div
      class="relative min-h-0 flex-1 overflow-y-auto bg-[#121211] text-[#ECEAE6]"
      style="font-family: 'IBM Plex Sans', system-ui, sans-serif"
    >
      <div
        class="flex flex-col gap-4 px-6 py-7 transition-opacity"
        :style="{ opacity: updating ? 0.55 : 1 }"
      >
        <span
          class="tracking-[.12em] text-[#EA5A4F] uppercase"
          :style="{ font: `600 11px/1 ${mono}` }"
        >{{ ARTIFACT.eyebrow }}</span>
        <h1
          class="m-0 tracking-[-.03em]"
          :style="{ font: `800 40px/1.02 ${mono}` }"
        >
          <span class="text-[#EA5A4F]">{{ ARTIFACT.heading.mark }}</span>{{ ARTIFACT.heading.rest }}
        </h1>
        <p class="m-0 text-[15.5px] leading-[1.6]">
          {{ ARTIFACT.lead }}
        </p>
        <div
          class="flex flex-wrap items-center gap-x-4 gap-y-2 text-[#A39F99]"
          :style="{ font: `400 12px/1.4 ${mono}` }"
        >
          <span
            class="rounded-[3px] bg-[#3A1D1B] px-2 py-[5px] tracking-[.06em] text-[#EA5A4F] uppercase"
            :style="{ font: `600 10.5px/1 ${mono}` }"
          >{{ ARTIFACT.status }}</span><span>{{ ARTIFACT.date }}</span><span>{{ ARTIFACT.repo }}</span>
        </div>
        <nav
          aria-label="Contents"
          class="rounded-[6px] border border-[#2C2A28] px-3.5 py-3 text-[13px]"
        >
          <div
            class="mb-2.5 tracking-[.12em] text-[#A39F99] uppercase"
            :style="{ font: `600 10.5px/1 ${mono}` }"
          >
            Contents
          </div>
          <div class="grid grid-cols-2 gap-x-4 gap-y-[5px]">
            <span
              v-for="t in ARTIFACT.toc"
              :key="t.n"
              class="flex gap-2"
            >
              <span
                class="text-[#A39F99]"
                :style="{ font: `400 11.5px/1.6 ${mono}` }"
              >{{ t.n }}</span>
              <a
                href="#"
                class="text-[#ECEAE6] hover:underline"
                @click.prevent
              >{{ t.t }}</a>
            </span>
          </div>
        </nav>
        <div class="relative flex flex-col gap-2 border-t border-[#2C2A28] pt-[22px]">
          <h2
            class="m-0 flex items-baseline gap-2.5"
            style="font: 600 21px/1.25 'IBM Plex Sans', sans-serif"
          >
            <span
              class="text-[#EA5A4F]"
              :style="{ font: `600 12.5px/1 ${mono}` }"
            >{{ ARTIFACT.section.n }}</span>{{ ARTIFACT.section.title }}
          </h2>
          <p class="m-0 text-[14.5px] leading-[1.62]">
            {{ ARTIFACT.section.before }}<code
              class="rounded-[3px] bg-[#1C1B1A] px-[5px] py-[2px]"
              :style="{ font: `400 .86em/1 ${mono}` }"
            >{{ ARTIFACT.section.code }}</code>{{ ARTIFACT.section.after }}
          </p>
          <button
            v-if="thread"
            type="button"
            aria-label="Comment thread"
            class="absolute top-[18px] right-[-6px] size-6 bg-accent text-center text-white"
            style="font: 600 11px/24px 'Cascadia Mono', 'Cascadia Code', monospace"
            @click="thread = false"
          >
            {{ ARTIFACT_THREAD.marker }}
          </button>
        </div>
      </div>

      <div
        v-if="thread"
        role="dialog"
        aria-label="Comment thread"
        class="absolute top-[330px] right-[30px] w-[300px] border border-line-strong bg-hover font-mono text-[12px] text-toned"
      >
        <div
          v-for="(c, i) in ARTIFACT_THREAD.comments"
          :key="i"
          class="flex flex-col gap-1 border-b border-line px-3 py-2.5"
        >
          <span class="flex items-center gap-2 text-[11px] text-muted">
            <HIcon
              v-if="c.agent"
              name="ai"
              :size="12"
              class="text-accent"
            />
            <span class="text-white">{{ c.author }}</span>{{ c.when }}
          </span>
          <span class="leading-[1.5]">{{ c.text }}</span>
          <span
            v-if="c.note"
            class="text-[11px] text-accent"
          >{{ c.note }}</span>
        </div>
        <div class="flex gap-1.5 px-3 py-2">
          <input
            v-model="reply"
            type="text"
            placeholder="Reply…"
            class="min-w-0 flex-1 border border-line bg-transparent px-2 py-1 text-white outline-none placeholder:text-muted focus:border-accent"
          >
          <button
            type="button"
            class="border border-line px-[9px] py-[3px] text-[11.5px] text-white hover:bg-raised"
            @click="thread = false"
          >
            Resolve
          </button>
        </div>
      </div>
    </div>

    <PaneMenu v-model:open="menuOpen">
      <div
        role="menu"
        aria-label="Artifact"
        class="absolute top-[46px] left-2 z-30 menu-panel w-[250px]"
      >
        <div class="mb-1 border-b border-line px-[9px] pt-1.5 pb-2 text-[11.5px] leading-[1.5] text-muted">
          Artifact by you<br>{{ ARTIFACT.updated }}
        </div>
        <template
          v-for="(m, i) in ARTIFACT_MENU"
          :key="i"
        >
          <div
            v-if="m === 'sep'"
            class="my-1 h-px bg-line"
          />
          <div
            v-else
            role="menuitem"
            :aria-disabled="m.disabled ? 'true' : 'false'"
            class="flex cursor-default items-center gap-2.5 px-[9px] py-1.5"
            :class="m.disabled ? 'text-disabled' : m.danger ? 'text-bad hover:bg-raised' : 'text-toned hover:bg-raised hover:text-white'"
            @click="!m.disabled && choose(m.label)"
          >
            <HIcon
              :name="m.icon"
              :size="14"
            />{{ m.label }}
          </div>
        </template>
      </div>
    </PaneMenu>
  </section>
</template>
