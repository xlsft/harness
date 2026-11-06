<script setup lang="ts">
// Customize: what the client finds for a folder, with where each thing comes from and a switch. The folder decides the
// project's skills; the rest are the same everywhere. A switch follows the user across machines.
import type { RecentFolder } from '~/gen/harness/agent/v1/agent_pb'
import type { SlashCommand } from '~/gen/harness/agent/v1/slash_commands_pb'
import { commandFacts, customizeSections, sourceLabel, type SlashCatalog } from '~/lib/session/slashCommands'

const store = useSessionStore()
const folders = ref<RecentFolder[]>([])
const cwd = ref(store.current?.meta.noFolder ? '' : store.current?.meta.cwd ?? '')
const catalog = ref<SlashCatalog | null>(null)
const error = ref('')
const loading = ref(false)

const folderOptions = computed(() => {
  const paths = new Set(folders.value.map(f => f.path))
  if (cwd.value) paths.add(cwd.value)
  return [{ value: '', label: 'No folder' }, ...[...paths].map(p => ({ value: p, label: p }))]
})
const sections = computed(() => customizeSections(catalog.value?.commands ?? []))
const nothing = computed(() => !loading.value && !sections.value.some(s => s.title !== 'Built-in commands'))

let asked = 0
async function load() {
  if (!store.transport) return
  const n = ++asked
  loading.value = true
  try {
    const res = await store.transport.listSlashCommands({ cwd: cwd.value })
    if (n !== asked) return
    catalog.value = res
    error.value = ''
  } catch (err) {
    if (n === asked) error.value = err instanceof Error ? err.message : String(err)
  } finally {
    if (n === asked) loading.value = false
  }
}
watch(cwd, load)
onMounted(async () => {
  await load()
  try {
    folders.value = (await store.transport?.listRecentFolders()) ?? []
  } catch { /* the current folder is still there */ }
})

async function flip(c: SlashCommand, enabled: boolean) {
  if (!store.transport) return
  const before = c.enabled
  c.enabled = enabled
  try {
    await store.transport.setSkillEnabled(c.name, enabled)
    error.value = ''
    await load()
  } catch (err) {
    c.enabled = before
    error.value = err instanceof Error ? err.message : String(err)
  }
}
</script>

<template>
  <section
    aria-label="Customize"
    class="h-full overflow-y-auto bg-default"
  >
    <div class="mx-auto flex max-w-[860px] flex-col gap-[22px] px-10 pt-[52px] pb-10 max-sm:px-4">
      <h1 class="m-0 font-display text-[26px] font-bold text-strong">
        Customize
      </h1>

      <div class="flex flex-wrap items-center gap-3 text-[12.5px] text-muted">
        <span>Folder</span>
        <HSelect
          v-model="cwd"
          class="min-w-[280px] flex-1"
          :options="folderOptions"
          aria-label="Folder"
        />
      </div>

      <p
        v-if="error"
        role="alert"
        class="m-0 border border-l-2 border-default border-l-bad px-2.5 py-1.5 text-[12.5px] text-toned"
      >
        {{ error }}
      </p>
      <p
        v-if="catalog?.projectSkippedUntrusted"
        class="m-0 border border-l-2 border-default border-l-warn px-2.5 py-1.5 text-[12.5px] text-toned"
      >
        This folder has skills or commands of its own, which load once you trust the folder in a session.
      </p>
      <p
        v-if="nothing"
        class="m-0 text-[12.5px] text-muted"
      >
        No skills or commands yet. Put a folder with a SKILL.md under .harness/skills, or a Markdown file under .harness/commands, here or in ~/.harness.
      </p>

      <div
        v-for="s in sections"
        :key="s.title"
        class="flex flex-col"
      >
        <h2 class="m-0 pb-2 text-[12px] font-medium text-muted">
          {{ s.title }}
        </h2>
        <div
          v-for="c in s.rows"
          :key="`${c.kind}:${c.name}:${c.path}`"
          class="flex items-start gap-3 border-t border-default py-2.5"
          :class="c.shadowedBy && 'opacity-60'"
        >
          <div class="flex min-w-0 flex-1 flex-col gap-1 text-[12.5px]">
            <div class="flex flex-wrap items-baseline gap-2">
              <span class="text-strong">/{{ c.name }}</span>
              <span class="text-muted">{{ c.argumentHint }}</span>
              <span class="border border-default px-1 text-[10px] leading-[14px] text-muted">{{ sourceLabel(c.source) }}</span>
              <span
                v-if="c.switchable && c.enabled && !c.shadowedBy && c.modelInvocable && !c.listed"
                class="text-[11px] text-muted"
                title="Files it applies to weren't touched yet, or the model's listing has no room for it. It still runs by name."
              >not in the model's listing</span>
            </div>
            <span class="text-toned">{{ c.description }}</span>
            <span
              v-if="commandFacts(c).length"
              class="text-[11.5px] text-muted"
            >{{ commandFacts(c).join(' · ') }}</span>
            <span
              v-if="c.path"
              class="truncate text-[11px] text-muted"
              :title="c.path"
            >{{ c.path }}</span>
            <span
              v-for="p in c.problems"
              :key="p"
              class="text-[11.5px] text-warn"
            >{{ p }}</span>
            <span
              v-if="c.skippedFields.length"
              class="text-[11.5px] text-muted"
            >Not supported, ignored: {{ c.skippedFields.join(', ') }}</span>
          </div>
          <HToggle
            v-if="c.switchable"
            :model-value="c.enabled"
            :label="`Turn /${c.name} on or off`"
            :disabled="!!c.shadowedBy"
            @update:model-value="flip(c, $event)"
          />
        </div>
      </div>

      <p
        v-if="catalog && catalog.listingUsedChars"
        class="m-0 text-[11.5px] text-muted"
      >
        The model's listing holds names and descriptions only: {{ catalog.listingUsedChars }} of {{ catalog.listingBudgetChars }} characters.
      </p>
    </div>
  </section>
</template>
