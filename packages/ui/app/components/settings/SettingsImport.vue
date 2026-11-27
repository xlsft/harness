<script setup lang="ts">
// Settings > Import: bringing work from another tool. The sources are listed; a row opens that source's own setup.
// Claude Code is the only tool for now: what is on this computer, and a folder's own files.
import { IMPORT_SOURCES, type ImportSource } from '~/lib/import/types'

const source = ref<ImportSource['id'] | null>(null)
</script>

<template>
  <SettingsImportClaude
    v-if="source === 'claude-code'"
    @back="source = null"
  />
  <SettingsImportProject
    v-else-if="source === 'claude-project'"
    @back="source = null"
  />
  <template v-else>
    <h2 class="set-h">
      {{ tt('Import') }}
    </h2>
    <p class="set-sub">
      {{ tt('Bring your work from another tool. Nothing moves until you choose what to bring.') }}
    </p>
    <section
      :aria-label="tt('Sources')"
      class="mb-8 flex flex-col"
    >
      <div class="overflow-hidden border border-default">
        <div
          v-for="(s, i) in IMPORT_SOURCES"
          :key="s.id"
          class="flex items-center gap-3 px-3 py-2.5"
          :class="{ 'border-t border-default': i > 0 }"
        >
          <!-- The tool's logo; a folder import also shows a folder. -->
          <span class="relative shrink-0">
            <SettingsMonogram text="claude" />
            <span
              v-if="s.icon === 'folder'"
              class="absolute -right-1 -bottom-1 flex size-3.5 items-center justify-center rounded-chip border border-line bg-default text-muted"
            >
              <HIcon
                name="folder"
                :size="9"
              />
            </span>
          </span>
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="text-[12.5px] text-strong">{{ s.name }}</span>
            <span class="truncate text-[11.5px] text-muted">{{ s.description }}</span>
          </span>
          <HBtn
            size="sm"
            @click="source = s.id"
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
