<script setup lang="ts">
// Settings > Import: bringing work from another tool. The client lists the sources (ListImportSources); a row opens
// that source's screen: the Claude Code imports have their own, every other source the generic one.
import type { ImportSourceInfo } from '~/lib/import/sources'

const store = useSessionStore()
const sources = ref<ImportSourceInfo[]>([])
const loading = ref(true)
const error = ref('')
const open = ref<ImportSourceInfo | null>(null)

onMounted(async () => {
  try {
    if (!store.transport) throw new Error('not connected to the client')
    sources.value = (await store.transport.importSources().list()).sources
  } catch (e) {
    error.value = e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
  }
  loading.value = false
})

function where(s: ImportSourceInfo) {
  if (s.userLevel && s.folderLevel) return tt('This computer or a folder')
  return s.userLevel ? tt('This computer') : tt('A folder')
}
</script>

<template>
  <SettingsImportClaude
    v-if="open?.screen === 'claude-code'"
    @back="open = null"
  />
  <SettingsImportProject
    v-else-if="open?.screen === 'claude-project'"
    @back="open = null"
  />
  <SettingsImportSource
    v-else-if="open"
    :source="open"
    @back="open = null"
  />
  <template v-else>
    <h2 class="set-h">
      {{ tt('Import') }}
    </h2>
    <p class="set-sub">
      {{ tt('Bring your work from another tool. Nothing moves until you choose what to bring.') }}
    </p>
    <p
      v-if="loading"
      class="set-sub"
    >
      {{ tt('Loading…') }}
    </p>
    <p
      v-else-if="error"
      role="alert"
      class="set-sub text-error"
    >
      {{ error }}
    </p>
    <section
      v-else
      :aria-label="tt('Sources')"
      class="mb-8 flex flex-col"
    >
      <div class="overflow-hidden border border-default">
        <div
          v-for="(s, i) in sources"
          :key="s.id"
          class="flex items-center gap-3 px-3 py-2.5"
          :class="{ 'border-t border-default': i > 0 }"
        >
          <!-- The tool's logo; a folder import also shows a folder. -->
          <span class="relative shrink-0">
            <SettingsMonogram
              v-if="s.logo"
              :text="s.logo"
            />
            <SettingsMonogram
              v-else-if="s.folderLevel && !s.userLevel && s.id === 'folder'"
              icon="folder"
            />
            <SettingsMonogram
              v-else
              :text="s.name"
            />
            <span
              v-if="s.folderLevel && !s.userLevel && s.logo"
              class="absolute -right-1 -bottom-1 flex size-3.5 items-center justify-center rounded-chip border border-line bg-default text-muted"
            >
              <HIcon
                name="folder"
                :size="9"
              />
            </span>
          </span>
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="flex items-center gap-1.5 text-[12.5px] text-strong">
              {{ s.name }}
              <span
                v-if="s.detected"
                class="text-[11px] text-muted"
              >· {{ tt('found on this computer') }}</span>
            </span>
            <span class="truncate text-[11.5px] text-muted">{{ s.description }}</span>
            <span class="text-[11px] text-dimmed">{{ where(s) }}</span>
          </span>
          <HBtn
            size="sm"
            @click="open = s"
          >
            <HIcon
              name="download"
              :size="12"
            />{{ tt('Import') }}
          </HBtn>
        </div>
      </div>
    </section>
  </template>
</template>
