<script setup lang="ts">
// Settings > About: the wordmark, the slogan, versions of this app, the client and the server it is
// signed in to, links, author and license.
import { isDesktop, openExternal } from '~/lib/desktop'

const store = useSessionStore()
const desktop = isDesktop()

const host = computed(() => {
  try {
    return store.account.server ? new URL(store.account.server).host : ''
  } catch {
    return store.account.server
  }
})

const facts = computed(() => [
  { label: 'App', value: desktop ? 'Desktop' : 'Web' },
  { label: 'Client', value: store.clientVersion || '—' },
  { label: 'Server', value: host.value || (desktop ? '—' : 'this page\'s server') },
  { label: 'Models', value: store.models.length ? `${store.models.length} available` : '—' }
])

const LINKS = [
  { label: 'Website', detail: 'harness.xlsft.com', url: 'https://harness.xlsft.com', icon: 'globe' },
  { label: 'Source code', detail: 'github.com/xlsft/harness', url: 'https://github.com/xlsft/harness', icon: 'code' },
  { label: 'Self-host the server', detail: 'deploy guide', url: 'https://github.com/xlsft/harness/tree/main/deploy', icon: 'box' },
  { label: 'Report an issue', detail: 'GitHub issues', url: 'https://github.com/xlsft/harness/issues', icon: 'flag' }
] as const

function open(url: string) {
  if (desktop) openExternal(url)
  else window.open(url, '_blank', 'noopener')
}
</script>

<template>
  <div class="flex flex-col gap-8">
    <div class="flex flex-col items-start gap-5 border border-default bg-surface px-6 py-7">
      <HWordmark class="h-[30px] w-auto self-start text-white" />
      <div class="flex flex-col gap-2">
        <h2 class="m-0 text-[17px] font-semibold text-white">
          Your workflow, unbounded.
        </h2>
        <p class="m-0 max-w-[460px] text-[12.5px] leading-[1.6] text-toned">
          The coding agent you already know, with any model and a server you own.
        </p>
      </div>
    </div>

    <dl class="m-0 grid grid-cols-2 border border-default max-sm:grid-cols-1">
      <div
        v-for="(f, i) in facts"
        :key="f.label"
        class="flex flex-col gap-1 px-4 py-3"
        :class="[i % 2 ? 'border-l border-default max-sm:border-l-0' : '', i > 1 ? 'border-t border-default' : i === 1 ? 'max-sm:border-t max-sm:border-default' : '']"
      >
        <dt class="text-[11px] text-muted">
          {{ f.label }}
        </dt>
        <dd class="m-0 truncate text-[12.5px] text-white tabular-nums">
          {{ f.value }}
        </dd>
      </div>
    </dl>

    <div class="flex flex-col">
      <h3 class="m-0 mb-2 text-[11px] font-normal text-muted">
        Links
      </h3>
      <div class="flex flex-col border border-default">
        <button
          v-for="l in LINKS"
          :key="l.url"
          type="button"
          class="group flex min-h-11 items-center gap-3 border-b border-default px-4 text-left last:border-b-0 hover:bg-hover"
          @click="open(l.url)"
        >
          <HIcon
            :name="l.icon"
            class="text-muted group-hover:text-white"
          />
          <span class="text-[12.5px] text-white">{{ l.label }}</span>
          <span class="min-w-0 flex-1 truncate text-[11.5px] text-muted">{{ l.detail }}</span>
          <HIcon
            name="arrow-up-right"
            :size="13"
            class="text-muted group-hover:text-white"
          />
        </button>
      </div>
    </div>

    <p class="m-0 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11.5px] text-muted">
      <span>Made by</span>
      <button
        type="button"
        class="text-white hover:text-accent"
        @click="open('https://github.com/xlsft')"
      >
        xlsft
      </button>
      <span>·</span>
      <span>MIT License</span>
      <span>·</span>
      <span>© 2026</span>
    </p>
  </div>
</template>
