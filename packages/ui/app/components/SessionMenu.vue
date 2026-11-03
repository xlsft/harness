<script setup lang="ts">
// Menu of one session (the "…" on a sidebar row, or a right click on it): show in Finder, copy path, pin,
// mark as unread, rename, fork, archive, delete. While it is open the letter keys pick an item. Delete asks
// first: the first click (or D) turns the row into a confirmation, the second deletes. A session of another
// machine has no local folder (no Finder / path), and "Continue here" forks it into a folder of this machine.
import type { SessionState } from '~/lib/session/types'
import { isDesktop, revealInFolder } from '~/lib/desktop'

const props = defineProps<{ session: SessionState }>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ rename: [] }>()

const store = useSessionStore()
const route = useRoute()
const desktop = isDesktop()
const id = computed(() => props.session.meta.id)
const pinned = computed(() => props.session.meta.pinned)
const unread = computed(() => props.session.meta.unread)
const remote = computed(() => props.session.meta.remote)
const confirmDelete = ref(false)

const toast = useToast()

// Runs a menu action; a failure (the server unreachable, say) is shown, not swallowed.
async function run(action: () => unknown) {
  open.value = false
  try {
    await action()
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err)
    toast.add({ title: 'Could not update the session', description: msg.replace(/^\[\w+\]\s*/, ''), color: 'error' })
  }
}

const copyPath = () => navigator.clipboard.writeText(props.session.meta.cwd)

const actions: Record<string, () => unknown> = {
  p: () => store.setPinned(id.value, !pinned.value),
  u: () => store.setUnread(id.value, !unread.value),
  r: () => emit('rename'),
  f: async () => {
    if (remote.value) return navigateTo({ path: '/', query: { new: '1', fork: id.value } })
    await store.fork(id.value)
    if (route.path !== '/') await navigateTo('/')
  },
  a: () => store.archive(id.value)
}

// Deleting cannot be undone: the first press asks, the second deletes.
function remove() {
  if (!confirmDelete.value) {
    confirmDelete.value = true
    return
  }
  run(() => store.deleteSession(id.value))
}

function onKey(e: KeyboardEvent) {
  if (e.metaKey || e.ctrlKey || e.altKey) return
  if (e.key.toLowerCase() === 'd') {
    e.preventDefault()
    remove()
    return
  }
  const action = actions[e.key.toLowerCase()]
  if (!action) return
  e.preventDefault()
  run(action)
}
watch(open, (v) => {
  confirmDelete.value = false
  if (v) document.addEventListener('keydown', onKey)
  else document.removeEventListener('keydown', onKey)
})
onBeforeUnmount(() => document.removeEventListener('keydown', onKey))
</script>

<template>
  <UPopover
    v-model:open="open"
    :content="{ side: 'bottom', align: 'start', sideOffset: 4 }"
  >
    <button
      type="button"
      :aria-label="`More for ${session.meta.title}`"
      class="flex size-5 shrink-0 items-center justify-center text-muted hover:bg-hover hover:text-white"
      :class="open ? 'flex bg-hover text-white' : 'hidden group-hover/row:flex'"
      @click.stop.prevent
    >
      <HIcon
        name="more"
        :size="13"
      />
    </button>
    <template #content>
      <div
        role="menu"
        :aria-label="session.meta.title"
        class="w-[220px] border border-accented bg-hover py-1 text-[12.5px]"
      >
        <PaneMenuItem
          v-if="desktop && !remote"
          icon="folder"
          @click="run(() => revealInFolder(session.meta.cwd))"
        >
          Show in Finder
        </PaneMenuItem>
        <template v-if="!remote">
          <PaneMenuItem
            icon="copy"
            @click="run(copyPath)"
          >
            Copy path
          </PaneMenuItem>
          <div class="my-1 h-px bg-line" />
        </template>
        <PaneMenuItem
          icon="pin"
          value="P"
          @click="run(actions.p!)"
        >
          {{ pinned ? 'Unpin' : 'Pin' }}
        </PaneMenuItem>
        <PaneMenuItem
          icon="dot"
          value="U"
          @click="run(actions.u!)"
        >
          {{ unread ? 'Mark as read' : 'Mark as unread' }}
        </PaneMenuItem>
        <PaneMenuItem
          icon="edit"
          value="R"
          @click="run(actions.r!)"
        >
          Rename
        </PaneMenuItem>
        <PaneMenuItem
          icon="branch"
          value="F"
          @click="run(actions.f!)"
        >
          {{ remote ? 'Continue here…' : 'Fork' }}
        </PaneMenuItem>
        <div class="my-1 h-px bg-line" />
        <PaneMenuItem
          icon="archive"
          value="A"
          @click="run(actions.a!)"
        >
          Archive
        </PaneMenuItem>
        <PaneMenuItem
          icon="trash"
          value="D"
          danger
          @click="remove"
        >
          {{ confirmDelete ? 'Delete for good?' : 'Delete' }}
        </PaneMenuItem>
      </div>
    </template>
  </UPopover>
</template>
