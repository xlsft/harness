<script setup lang="ts">
// The forms behind Add, and the folder chooser on the web, where there's no system dialog.
import type { ConnectorOption } from '~/lib/extensions/types'

export type AddMode = 'skill' | 'install' | 'connector' | 'marketplace' | 'folder'

const props = defineProps<{ mode: AddMode, cwd: string, trusted: boolean, folders: string[], option: ConnectorOption | null, canManageOrg: boolean }>()
const emit = defineEmits<{ close: [], done: [string], folder: [string] }>()
const store = useSessionStore()

const name = ref('')
const description = ref('')
const body = ref('')
const inProject = ref(!!props.cwd && props.trusted)
const spec = ref('')
const source = ref('')
const path = ref(props.cwd)
const error = ref('')
const busy = ref(false)

const TITLES: Record<AddMode, string> = { skill: 'Create a skill', install: 'Install from a folder or URL', connector: 'Add a connector', marketplace: 'Add a marketplace', folder: 'Open a folder' }
const field = 'h-8 w-full min-w-0 border border-accented bg-default px-2.5 text-[12.5px] text-strong outline-none focus:border-accent'
const area = 'min-h-24 w-full min-w-0 border border-accented bg-default px-2.5 py-1.5 text-[12.5px] text-strong outline-none focus:border-accent'

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function submit() {
  busy.value = true
  error.value = ''
  try {
    if (props.mode === 'skill') {
      const where = await store.transport!.extensions().createSkill({ name: name.value.trim(), description: description.value.trim(), body: body.value, cwd: inProject.value ? props.cwd : '' })
      emit('done', tt('Created {0}.', [where]))
    } else if (props.mode === 'install') {
      const p = await store.transport!.plugins().install(spec.value.trim(), props.cwd)
      emit('done', tt('Installed {0}. Nothing of it runs until you approve it.', [p.id]))
    } else if (props.mode === 'marketplace') {
      const n = await store.transport!.plugins().addMarketplace(source.value.trim())
      emit('done', tt('Added {0}. Nothing was installed.', [n]))
    } else if (props.mode === 'folder') {
      emit('folder', path.value.trim())
    }
  } catch (e) {
    error.value = message(e)
  } finally {
    busy.value = false
  }
}
const canSubmit = computed(() => {
  if (props.mode === 'skill') return !!name.value.trim() && !!description.value.trim()
  if (props.mode === 'install') return !!spec.value.trim()
  if (props.mode === 'marketplace') return !!source.value.trim()
  return !!path.value.trim()
})
</script>

<template>
  <ExtensionsSheet
    :label="TITLES[mode]"
    @close="emit('close')"
  >
    <template #header>
      <h2 class="m-0 text-[15px] font-semibold text-strong">
        {{ TITLES[mode] }}
      </h2>
    </template>

    <ExtensionsMcpForm
      v-if="mode === 'connector'"
      :entry="null"
      :initial="option"
      :can-manage-org="canManageOrg"
      :api="store.transport!.mcp()"
      @saved="emit('done', 'Added the connector.')"
      @cancel="emit('close')"
    />

    <form
      v-else
      class="flex flex-col gap-3 text-[12.5px]"
      :aria-label="TITLES[mode]"
      @submit.prevent="submit"
    >
      <p
        v-if="error"
        role="alert"
        class="m-0 border border-l-2 border-default border-l-bad px-2.5 py-1.5 text-toned"
      >
        {{ error }}
      </p>

      <template v-if="mode === 'skill'">
        <label class="flex flex-col gap-1">
          <span class="text-muted">{{ tt('Name') }}</span>
          <input
            v-model="name"
            :class="field"
            spellcheck="false"
            placeholder="tidy-imports"
          >
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-muted">{{ tt('What it\'s for, and when the agent should use it') }}</span>
          <input
            v-model="description"
            :class="field"
          >
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-muted">{{ tt('Instructions') }}</span>
          <textarea
            v-model="body"
            :class="area"
          />
        </label>
        <label
          v-if="cwd && trusted"
          class="flex items-center gap-2"
        >
          <input
            v-model="inProject"
            type="checkbox"
          >
          <span class="text-toned">{{ tt('In this project, so it travels with the repository') }}</span>
        </label>
        <p class="m-0 text-[12px] text-muted">
          {{ inProject ? tt('Written to {0}/.harness/skills.', [cwd]) : 'Written to your personal skills, ~/.harness/skills.' }}
        </p>
      </template>

      <template v-else-if="mode === 'install'">
        <label class="flex flex-col gap-1">
          <span class="text-muted">{{ tt('name@marketplace, a folder or a repository') }}</span>
          <input
            v-model="spec"
            :class="field"
            spellcheck="false"
            placeholder="owner/repo"
          >
        </label>
        <p class="m-0 text-[12px] text-muted">
          {{ tt('It stays on this device. Nothing of it runs until you read what it holds and approve exactly that.') }}
        </p>
      </template>

      <template v-else-if="mode === 'marketplace'">
        <label class="flex flex-col gap-1">
          <span class="text-muted">{{ tt('owner/repo, a git URL, a URL of a marketplace.json, or a folder') }}</span>
          <input
            v-model="source"
            :class="field"
            spellcheck="false"
            placeholder="owner/repo"
          >
        </label>
        <p class="m-0 text-[12px] text-muted">
          {{ tt('Adding one fetches its catalog. Nothing is installed.') }}
        </p>
      </template>

      <template v-else>
        <label class="flex flex-col gap-1">
          <span class="text-muted">{{ tt('Path on this device') }}</span>
          <input
            v-model="path"
            :class="field"
            spellcheck="false"
          >
        </label>
        <div
          v-if="folders.length"
          class="flex flex-col"
        >
          <span class="pb-1 text-muted">{{ tt('Recent') }}</span>
          <button
            v-for="f in folders"
            :key="f"
            type="button"
            class="cursor-pointer truncate border-b border-default py-1.5 text-left text-toned hover:bg-hover"
            :title="f"
            @click="emit('folder', f)"
          >
            {{ f }}
          </button>
        </div>
      </template>

      <div class="flex items-center gap-2 pt-1">
        <HBtn
          type="submit"
          variant="primary"
          size="md"
          :disabled="!canSubmit || busy"
        >
          {{ mode === 'skill' ? tt('Create') : mode === 'install' ? tt('Install') : mode === 'marketplace' ? tt('Add') : tt('Open') }}
        </HBtn>
        <HBtn
          size="md"
          @click="emit('close')"
        >
          {{ tt('Cancel') }}
        </HBtn>
      </div>
    </form>
  </ExtensionsSheet>
</template>
