<script setup lang="ts">
// Browser pane (docs/design/ui/Browser.dc.html), on demo fixtures. Opens on a new tab like the
// design's default; "Preview your app instead" (or typing localhost:3000) loads the dev page,
// typing a github.com URL asks for the site permission. URL `menu` values reach the other states:
// settings · empty · page · agent · viewport · approval.
import { usePanes } from '~/composables/usePanes'
import type { SessionState } from '~/lib/session/types'
import {
  AGENT_STEP, DEV_TAB, NEW_TAB, PR_TAB, PREVIEW_APP, PROVIDERS, REQUEST_BARS, SITE_PERMISSION, VIEWPORTS,
  type BrowserTab
} from '~/lib/fixtures/browser'

const props = defineProps<{ session: SessionState, menu: string }>()

const { closePane } = usePanes()

let counter = 0
function makeTab(base: Omit<BrowserTab, 'id'>): BrowserTab {
  counter += 1
  return { ...base, id: `tab-${counter}` }
}

const loaded = ['page', 'agent', 'viewport', 'approval'].includes(props.menu)
const tabs = ref<BrowserTab[]>(loaded ? [makeTab(DEV_TAB), makeTab(PR_TAB)] : [makeTab(NEW_TAB)])
const selected = ref(tabs.value[props.menu === 'approval' ? 1 : 0]!.id)
const tab = computed(() => tabs.value.find(t => t.id === selected.value) ?? tabs.value[0]!)

const menuOpen = ref(props.menu === 'settings')
watch(() => props.menu, (m) => {
  menuOpen.value = m === 'settings'
})

const agent = ref(props.menu === 'agent')
const viewport = ref<typeof VIEWPORTS[number]['name'] | null>(props.menu === 'viewport' ? 'Mobile' : null)
const vp = computed(() => VIEWPORTS.find(v => v.name === viewport.value) ?? null)
const allowed = ref<string[]>([])
const approval = computed(() => tab.value.kind === 'pr' && !allowed.value.includes(tab.value.id))

const address = ref(tab.value.url)
watch(selected, () => {
  address.value = tab.value.url
})

const onPage = computed(() => tab.value.kind !== 'new')

const settings = reactive({ builtInLinks: false, autoOpen: true, autoVerify: false })

function navigate(url: string) {
  const t = tab.value
  const u = url.trim()
  if (!u) return
  if (u.includes('github.com')) Object.assign(t, PR_TAB, { url: u })
  else if (u.includes('localhost:3000')) Object.assign(t, DEV_TAB, { url: u })
  else Object.assign(t, { url: u, title: u })
  address.value = t.url
}

function openPreview() {
  Object.assign(tab.value, DEV_TAB)
  address.value = tab.value.url
}

function addTab() {
  const t = makeTab(NEW_TAB)
  tabs.value.push(t)
  selected.value = t.id
}

function closeTab(id: string) {
  const i = tabs.value.findIndex(t => t.id === id)
  if (i < 0) return
  tabs.value.splice(i, 1)
  if (!tabs.value.length) {
    closePane('browser')
    return
  }
  if (selected.value === id) selected.value = tabs.value[Math.max(0, i - 1)]!.id
}

// Show: the page opens in the blank tab when one is selected, else in a new tab.
const { target: showTarget } = useShow()
watch(showTarget, (t) => {
  if (t?.kind !== 'url') return
  showTarget.value = null
  if (tab.value.kind !== 'new') addTab()
  navigate(t.url)
}, { immediate: true })

function toggleViewport() {
  viewport.value = viewport.value ? null : 'Mobile'
}

function pickViewport(name: typeof VIEWPORTS[number]['name']) {
  viewport.value = name === 'Desktop' ? null : name
}
</script>

<template>
  <section
    aria-label="Browser"
    class="relative flex h-full flex-col overflow-hidden bg-default"
  >
    <div class="flex h-[44px] shrink-0 items-stretch border-b border-line pr-1.5">
      <div
        role="tablist"
        aria-label="Browser tabs"
        class="flex min-w-0 items-stretch overflow-hidden"
      >
        <PaneTab
          v-for="t in tabs"
          :key="t.id"
          :selected="t.id === selected"
          class="max-w-[200px] gap-2 px-3"
          @select="selected = t.id"
          @close="closeTab(t.id)"
        >
          <span
            class="flex size-3.5 shrink-0 items-center justify-center text-[9px] not-italic"
            :style="{ background: t.favBg, color: t.favFg }"
          >{{ t.fav }}</span>
          <span class="truncate">{{ t.title }}</span>
        </PaneTab>
        <button
          type="button"
          aria-label="New browser tab"
          title="New browser tab"
          class="flex w-9 shrink-0 items-center justify-center border-r border-line text-muted hover:text-strong"
          @click="addTab"
        >
          <HIcon name="plus" />
        </button>
      </div>
      <span class="flex-1" />
      <div class="flex items-center gap-[2px]">
        <PaneBtn
          icon="layout"
          label="Toggle layout"
        />
        <PaneBtn
          icon="more"
          label="Browser settings"
          white
          :pressed="menuOpen"
          data-menu-anchor
          @click="menuOpen = !menuOpen"
        />
        <PaneBtn
          icon="full"
          label="Maximize"
        />
        <PaneBtn
          icon="close"
          label="Close browser"
          @click="closePane('browser')"
        />
      </div>
    </div>

    <div class="flex h-[44px] shrink-0 items-center gap-1 border-b border-line px-2">
      <PaneBtn
        icon="arrow-left"
        label="Back"
        :disabled="!onPage"
      />
      <PaneBtn
        icon="arrow-right"
        label="Forward"
        disabled
      />
      <PaneBtn
        icon="reload"
        label="Reload"
      />
      <label class="flex h-[30px] min-w-0 flex-1 items-center gap-2 border border-line bg-surface px-2.5 focus-within:border-accent">
        <HIcon
          name="globe"
          :size="13"
          class="text-muted"
        />
        <span class="sr-only">Address</span>
        <input
          v-model="address"
          type="text"
          spellcheck="false"
          placeholder="Type a URL"
          class="min-w-0 flex-1 truncate bg-transparent text-[12.5px] text-strong outline-none placeholder:text-muted"
          @keydown.enter="navigate(address)"
        >
      </label>
      <PaneBtn
        icon="phone"
        label="Viewport"
        :pressed="!!vp"
        @click="toggleViewport"
      />
      <PaneBtn
        icon="cursor"
        label="Pick an element for the agent"
      />
    </div>

    <div
      v-if="agent && onPage"
      class="flex items-center gap-[9px] border-b border-line bg-surface px-3 py-1.5 text-[12px] text-muted"
    >
      <HIcon
        name="ai"
        :size="14"
        class="text-accent"
      />
      <span class="text-strong">The agent is using this tab</span>· step {{ AGENT_STEP.step }} of {{ AGENT_STEP.of }}<span class="flex-1" />
      <button
        type="button"
        class="border border-line px-2 py-px text-[11px] text-strong hover:bg-hover"
        @click="agent = false"
      >
        Take over
      </button>
      <button
        type="button"
        class="border border-line px-2 py-px text-[11px] text-bad hover:bg-hover"
        @click="agent = false"
      >
        Stop
      </button>
    </div>

    <div
      v-if="vp"
      class="flex items-center justify-center gap-2.5 border-b border-line px-3 py-[5px] text-[11.5px] text-muted"
    >
      <button
        v-for="v in VIEWPORTS"
        :key="v.name"
        type="button"
        :class="v.name === vp.name ? 'border border-accent px-1.5 text-strong' : 'hover:text-strong'"
        @click="pickViewport(v.name)"
      >
        {{ v.name }}
      </button><span class="text-line">│</span>{{ vp.width }} × {{ vp.height }}{{ vp.touch ? ' · touch' : '' }}
    </div>

    <div
      class="relative min-h-0 flex-1 overflow-hidden"
      :class="vp && onPage ? 'bg-hover' : 'bg-default'"
    >
      <div
        v-if="!onPage"
        class="flex h-full flex-col items-center justify-center gap-2.5 p-6 text-center"
      >
        <HIcon
          name="globe"
          :size="30"
          class="text-muted"
        />
        <span class="font-display text-[15px] text-strong">Browse with the agent</span>
        <span class="max-w-[340px] text-[12.5px] leading-[1.55] text-muted">Type a URL, or ask the agent to open, click and fill in pages for you.</span>
        <span class="h-2" />
        <span class="text-[12px] text-muted">Preview your app instead</span>
        <a
          href="#"
          class="flex items-center gap-2.5 border border-line bg-surface px-3 py-[7px] text-[12.5px] text-strong hover:bg-hover"
          @click.prevent="openPreview"
        >{{ PREVIEW_APP.name }}<span class="text-muted">{{ PREVIEW_APP.detail }}</span><HIcon
          name="right"
          :size="12"
          class="text-accent"
        /></a>
      </div>

      <div
        v-else
        class="mx-auto flex h-full max-w-full flex-col overflow-y-auto bg-surface"
        :class="vp && 'border-x border-line'"
        :style="{ width: vp ? `${vp.width}px` : '100%' }"
      >
        <div class="flex flex-wrap items-center gap-3.5 border-b border-line px-4 py-3 text-[12px]">
          <span class="font-display text-[13px] font-bold text-strong">harness core</span>
          <span class="border-b border-accent text-strong">Providers</span><span class="text-muted">Keys</span><span class="text-muted">Usage</span>
        </div>
        <div class="flex flex-col gap-3 p-4">
          <div class="flex flex-wrap items-center gap-2.5">
            <span class="flex-1 font-display text-[17px] font-bold text-strong">Providers</span>
            <span class="relative inline-flex">
              <button
                type="button"
                class="bg-accent px-3 py-1.5 text-[12px] text-on-accent"
                :class="agent && 'outline-2 outline-offset-[3px] outline-accent'"
              >+ Add provider</button>
              <span
                v-if="agent"
                class="absolute right-[-3px] bottom-[calc(100%+8px)] bg-accent px-1.5 py-px text-[10.5px] whitespace-nowrap text-on-accent"
              >{{ AGENT_STEP.action }}</span>
            </span>
          </div>
          <div class="border border-line text-[12px]">
            <div
              v-for="(p, i) in PROVIDERS"
              :key="p.name"
              class="flex gap-2.5 px-3 py-2"
              :class="i < PROVIDERS.length - 1 && 'border-b border-muted'"
            >
              <span class="flex-1 text-strong">{{ p.name }}</span><span class="text-muted">{{ p.key }}</span><span :style="{ color: p.color }">{{ p.state }}</span>
            </div>
          </div>
          <div class="flex flex-col gap-1.5 border border-line px-3 py-2.5 text-[12px]">
            <span class="text-muted">Requests, last hour</span>
            <div class="flex h-12 items-end gap-[3px]">
              <span
                v-for="(b, i) in REQUEST_BARS"
                :key="i"
                class="flex-1 bg-accent"
                :style="{ height: `${b}px` }"
              />
            </div>
          </div>
        </div>
      </div>

      <div
        v-if="approval"
        class="absolute inset-0 flex items-center justify-center bg-ground/70 p-5"
      >
        <div
          role="dialog"
          aria-label="Site permission"
          class="flex w-[340px] max-w-full flex-col border border-line-strong bg-hover"
        >
          <div class="flex flex-col gap-1.5 border-b border-line px-3.5 py-3">
            <span class="text-[13px] text-strong">{{ SITE_PERMISSION.title }}</span>
            <span class="text-[12px] leading-[1.5] text-muted">{{ SITE_PERMISSION.body }}</span>
          </div>
          <div class="flex flex-wrap gap-2 px-3.5 py-2.5">
            <button
              type="button"
              class="bg-accent px-3 py-[5px] text-[12px] text-on-accent hover:brightness-110"
              @click="allowed.push(tab.id)"
            >
              Allow once
            </button>
            <button
              type="button"
              class="border border-line px-3 py-[5px] text-[12px] text-strong hover:bg-raised"
              @click="allowed.push(tab.id)"
            >
              Always allow
            </button>
            <button
              type="button"
              class="border border-line px-3 py-[5px] text-[12px] text-strong hover:bg-raised"
              @click="closeTab(tab.id)"
            >
              Deny
            </button>
          </div>
        </div>
      </div>
    </div>

    <PaneMenu v-model:open="menuOpen">
      <div
        role="menu"
        aria-label="Browser settings"
        class="absolute top-[46px] right-[74px] z-30 menu-panel w-[300px]"
      >
        <PaneMenuItem :disabled="!onPage">
          Open in system browser
        </PaneMenuItem>
        <PaneMenuItem :disabled="!onPage">
          Copy URL
        </PaneMenuItem>
        <PaneMenuItem :disabled="!onPage">
          Save screenshot
        </PaneMenuItem>
        <PaneMenuItem :disabled="!onPage">
          Show annotation tools
        </PaneMenuItem>
        <PaneMenuItem @click="menuOpen = false">
          Open HTML file…
        </PaneMenuItem>
        <div class="my-1 h-px bg-line" />
        <PaneMenuItem
          disabled
          gap="gap-0"
          value="Responsive"
        >
          Viewport
        </PaneMenuItem>
        <div class="my-1 h-px bg-line" />
        <PaneMenuItem
          role="menuitemcheckbox"
          :checked="settings.builtInLinks"
          gap="gap-0"
          @click="settings.builtInLinks = !settings.builtInLinks"
        >
          Open links in built-in browser
        </PaneMenuItem>
        <PaneMenuItem
          role="menuitemcheckbox"
          :checked="settings.autoOpen"
          gap="gap-0"
          @click="settings.autoOpen = !settings.autoOpen"
        >
          Auto-open files the agent writes
        </PaneMenuItem>
        <PaneMenuItem
          role="menuitemcheckbox"
          :checked="settings.autoVerify"
          gap="gap-0"
          @click="settings.autoVerify = !settings.autoVerify"
        >
          Auto-verify changes
        </PaneMenuItem>
        <PaneMenuItem @click="menuOpen = false">
          Manage allowed sites…
        </PaneMenuItem>
        <div class="my-1 h-px bg-line" />
        <div class="px-[9px] pt-1.5 pb-0.5 text-[11.5px] text-muted">
          Cookies
        </div>
        <PaneMenuItem @click="menuOpen = false">
          Import cookies…
        </PaneMenuItem>
        <PaneMenuItem
          value="Until quit"
          sub
        >
          Keep cookies
        </PaneMenuItem>
        <PaneMenuItem
          danger
          @click="menuOpen = false"
        >
          Clear browsing data
        </PaneMenuItem>
      </div>
    </PaneMenu>
  </section>
</template>
