<script setup lang="ts">
// Start a session on the connected client: a folder on the client's machine and a model. Shown for
// ?new=1 (sidebar "New session", ⌘N) and when the client has no sessions yet. The last folder is remembered.
const store = useSessionStore()
const route = useRoute()
const router = useRouter()

const LAST_CWD = 'harness-last-cwd'
const cwd = ref('')
const model = ref('')
const busy = ref(false)
const error = ref('')

onMounted(() => {
  try {
    cwd.value = localStorage.getItem(LAST_CWD) ?? ''
  } catch {
    // Storage unavailable: start with an empty folder.
  }
})

async function start() {
  const dir = cwd.value.trim()
  if (!dir || busy.value) return
  busy.value = true
  error.value = ''
  try {
    await store.createSession(dir, model.value)
    try {
      localStorage.setItem(LAST_CWD, dir)
    } catch {
      // Not remembered; harmless.
    }
    const query = { ...route.query }
    delete query.new
    router.replace({ query })
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <form
    class="mx-auto flex w-full max-w-[560px] flex-col gap-3 px-5"
    @submit.prevent="start"
  >
    <h2 class="m-0 text-[13.5px] font-semibold text-white">
      New session
    </h2>
    <label class="flex flex-col gap-1.5 text-[12px] text-muted">
      Folder on the client's machine
      <input
        v-model="cwd"
        type="text"
        spellcheck="false"
        autocomplete="off"
        placeholder="/Users/you/Projects/app"
        aria-label="Folder"
        class="h-9 border border-accented bg-surface px-2.5 text-[12.5px] text-white outline-none placeholder:text-dimmed focus:border-accent"
        autofocus
      >
    </label>
    <label
      v-if="store.models.length"
      class="flex flex-col gap-1.5 text-[12px] text-muted"
    >
      Model
      <select
        v-model="model"
        aria-label="Model"
        class="h-9 border border-accented bg-surface px-2 text-[12.5px] text-white outline-none focus:border-accent"
      >
        <option value="">
          Client default
        </option>
        <option
          v-for="m in store.models"
          :key="m.id"
          :value="m.id"
        >
          {{ m.id }}{{ m.provider ? ` · ${m.provider}` : '' }}
        </option>
      </select>
    </label>
    <div class="flex items-center gap-3">
      <button
        type="submit"
        :disabled="!cwd.trim() || busy"
        class="h-8 bg-accent px-3.5 text-[12.5px] text-white disabled:opacity-50"
      >
        {{ busy ? 'Starting…' : 'Start session' }}
      </button>
      <span class="inline-flex items-center gap-1 text-[11.5px] text-muted"><HKeys keys="enter" />to start</span>
    </div>
    <p
      v-if="error"
      class="m-0 border border-bad px-2.5 py-1.5 text-[12px] text-bad"
    >
      {{ error }}
    </p>
  </form>
</template>
