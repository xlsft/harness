<script setup lang="ts">
// Settings > About: the wordmark, the slogan, versions of this app, the client and the server it is
// signed in to, whether newer ones exist, links, author and license.
import { isDesktop, openExternal } from '~/lib/desktop'
import { RELEASES_URL, isDevVersion, isNewer, latestReleaseTag, serverUpgradeCommands } from '~/lib/releases'

const store = useSessionStore()
const desktop = isDesktop()
const update = useAppUpdate()
const config = useRuntimeConfig()

// Looked up when this page opens: the server's version compared with the newest release. The server has no part in it.
const latestTag = ref('')
const isAdmin = ref(store.account.role === 'admin')
const copied = ref(false)
onMounted(async () => {
  void update.check()
  try {
    if (store.transport) isAdmin.value = await store.transport.users().isAdmin()
  } catch {
    // Keep what the shell said.
  }
  try {
    latestTag.value = await latestReleaseTag()
  } catch {
    // Offline, or no release yet: nothing to compare with, and nothing worth saying.
  }
})
const serverBehind = computed(() => {
  const v = store.serverVersion
  return !!v && !isDevVersion(v) && !!latestTag.value && isNewer(latestTag.value, v)
})
const upgradeCommands = computed(() => serverUpgradeCommands(latestTag.value))
async function copyCommands() {
  await navigator.clipboard.writeText(upgradeCommands.value)
  copied.value = true
  setTimeout(() => (copied.value = false), 1500)
}

const host = computed(() => {
  try {
    return store.account.server ? new URL(store.account.server).host : ''
  } catch {
    return store.account.server
  }
})

const facts = computed(() => [
  { label: 'App', value: `${desktop ? 'Desktop' : 'Web'} ${update.current.value || String(config.public.version)}` },
  { label: 'Client', value: store.clientVersion || '—' },
  { label: 'Server', value: [host.value || (desktop ? '' : 'this page\'s server'), store.serverVersion].filter(Boolean).join(' · ') || '—' },
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
    <div class="relative flex flex-col items-start gap-5 overflow-hidden border border-default bg-surface px-6 py-7">
      <CatMascot
        aria-hidden="true"
        class="absolute right-5 bottom-0 max-sm:hidden"
      />
      <HWordmark class="h-[30px] w-auto self-start text-strong" />
      <div class="flex flex-col gap-2">
        <h2 class="m-0 text-[17px] font-semibold text-strong">
          Your workflow, unbounded.
        </h2>
        <p class="m-0 max-w-[460px] text-[12.5px] leading-[1.6] text-toned">
          The coding agent you already know, with any model and a server you own.
        </p>
      </div>
    </div>

    <dl class="m-0 grid grid-cols-2 overflow-hidden border border-default max-sm:grid-cols-1">
      <div
        v-for="(f, i) in facts"
        :key="f.label"
        class="flex flex-col gap-1 px-4 py-3"
        :class="[i % 2 ? 'border-l border-default max-sm:border-l-0' : '', i > 1 ? 'border-t border-default' : i === 1 ? 'max-sm:border-t max-sm:border-default' : '']"
      >
        <dt class="text-[11px] text-muted">
          {{ f.label }}
        </dt>
        <dd class="m-0 truncate text-[12.5px] text-strong tabular-nums">
          {{ f.value }}
        </dd>
      </div>
    </dl>

    <div
      v-if="desktop && (update.phase.value !== 'idle' || update.unavailable.value)"
      class="flex flex-col gap-3 border border-default px-4 py-3"
    >
      <p
        v-if="update.phase.value === 'idle'"
        class="m-0 text-[11.5px] text-muted"
      >
        This app can't update itself: {{ update.unavailable.value }}.
      </p>
      <template v-else>
        <div class="flex items-center gap-3">
          <span class="min-w-0 flex-1 truncate text-[12.5px] text-strong">
            {{ update.phase.value === 'installed' ? 'Restart to finish updating' : 'Update available' }} · v{{ update.version.value }}
          </span>
          <HBtn
            v-if="update.phase.value === 'installed'"
            @click="update.restart()"
          >
            Restart
          </HBtn>
          <HBtn
            v-else
            :disabled="update.phase.value === 'installing'"
            @click="update.install()"
          >
            {{ update.phase.value === 'installing' ? 'Downloading…' : 'Update' }}
          </HBtn>
        </div>
        <p
          v-if="update.error.value"
          class="m-0 text-[11.5px] text-bad"
        >
          {{ update.error.value }}
        </p>
        <pre
          v-if="update.notes.value"
          class="m-0 max-h-48 overflow-auto font-[inherit] text-[11.5px] leading-[1.6] whitespace-pre-wrap text-toned"
        >{{ update.notes.value }}</pre>
      </template>
    </div>

    <div
      v-if="serverBehind"
      class="flex flex-col gap-3 border border-default px-4 py-3"
    >
      <div class="flex items-center gap-3">
        <span class="min-w-0 flex-1 truncate text-[12.5px] text-strong">
          Server {{ store.serverVersion }} · {{ latestTag }} is available
        </span>
        <button
          type="button"
          class="text-[11.5px] text-muted hover:text-strong"
          @click="open(`${RELEASES_URL}/tag/${latestTag}`)"
        >
          What's new
        </button>
      </div>
      <template v-if="isAdmin">
        <p class="m-0 text-[11.5px] text-muted">
          The server doesn't update itself. On its host:
        </p>
        <pre class="m-0 overflow-x-auto bg-surface p-3 text-[11.5px] leading-[1.6] text-strong">{{ upgradeCommands }}</pre>
        <div>
          <HBtn @click="copyCommands">
            {{ copied ? 'Copied' : 'Copy commands' }}
          </HBtn>
        </div>
      </template>
      <p
        v-else
        class="m-0 text-[11.5px] text-muted"
      >
        Your server's admin updates it.
      </p>
    </div>

    <div class="flex flex-col">
      <h3 class="m-0 mb-2 text-[11px] font-normal text-muted">
        Links
      </h3>
      <div class="flex flex-col overflow-hidden border border-default">
        <button
          v-for="l in LINKS"
          :key="l.url"
          type="button"
          class="group flex min-h-11 items-center gap-3 border-b border-default px-4 text-left last:border-b-0 hover:bg-hover"
          @click="open(l.url)"
        >
          <HIcon
            :name="l.icon"
            class="text-muted group-hover:text-strong"
          />
          <span class="text-[12.5px] text-strong">{{ l.label }}</span>
          <span class="min-w-0 flex-1 truncate text-[11.5px] text-muted">{{ l.detail }}</span>
          <HIcon
            name="arrow-up-right"
            :size="13"
            class="text-muted group-hover:text-strong"
          />
        </button>
      </div>
    </div>

    <p class="m-0 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11.5px] text-muted">
      <span>Made by</span>
      <button
        type="button"
        class="text-strong hover:text-accent"
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
