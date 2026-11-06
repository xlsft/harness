<script setup lang="ts">
// Settings > Import: bringing work from another tool. The sources are listed; a row opens that source's own setup.
// Claude Code is the only one for now.
import { IMPORT_SOURCES, type ImportSource } from '~/lib/import/types'

const source = ref<ImportSource['id'] | null>(null)
</script>

<template>
  <SettingsImportClaude
    v-if="source === 'claude-code'"
    @back="source = null"
  />
  <template v-else>
    <h2 class="set-h">
      {{ tt('Import') }}
    </h2>
    <p class="set-sub">
      {{ tt('Bring your work from another tool. Nothing moves until you choose what to bring.') }}
    </p>
    <button
      v-for="s in IMPORT_SOURCES"
      :key="s.id"
      type="button"
      class="set-row w-[calc(100%+2*var(--set-gutter,0px))] items-center text-left hover:bg-hover"
      @click="source = s.id"
    >
      <span class="flex min-w-0 items-center gap-3">
        <HIcon
          :name="s.icon"
          class="shrink-0 text-muted"
        />
        <span class="set-label">
          <span>{{ s.name }}</span>
          <span class="set-desc">{{ s.description }}</span>
        </span>
      </span>
      <HIcon
        name="arrow-right"
        :size="13"
        class="shrink-0 text-muted"
      />
    </button>
  </template>
</template>
