<script setup lang="ts">
// Settings > Computer use: the sites the agent may use in the user's own Chrome without asking, on this device. They come from
// the session's question ("Always on this device"); here they can only be taken back. Device-local, never synced.
const store = useSessionStore()
const available = ref(false)
const sites = ref<string[]>([])
const error = ref('')

const message = (e: unknown) => e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)

onMounted(async () => {
  try {
    const r = await store.transport?.browser().realChrome()
    available.value = r?.available ?? false
    sites.value = r?.allowedSites ?? []
  } catch (e) {
    error.value = message(e)
  }
})

async function revoke(site: string) {
  try {
    sites.value = await store.transport?.browser().revokeSite(site) ?? []
  } catch (e) {
    error.value = message(e)
  }
}
</script>

<template>
  <section
    aria-label="Your Chrome"
    class="flex flex-col"
  >
    <h2 class="set-h">
      Your Chrome
    </h2>
    <p class="text-[12px] text-muted">
      A session's browser can use your own Chrome, with your sign-ins, from the Browser pane's menu.
      {{ available ? 'Chrome is ready to connect.' : 'Turn on remote debugging at chrome://inspect/#remote-debugging first.' }}
      Every site asks once; these never ask again on this device.
    </p>
    <ul
      v-if="sites.length"
      class="mt-2 flex flex-col"
    >
      <li
        v-for="s in sites"
        :key="s"
        class="flex items-center gap-2 border-b border-line py-1.5 text-[12.5px] text-strong"
      >
        <span class="min-w-0 flex-1 truncate">{{ s }}</span>
        <HBtn
          tone="danger"
          @click="revoke(s)"
        >
          Revoke
        </HBtn>
      </li>
    </ul>
    <p
      v-else
      class="mt-2 text-[12px] text-muted"
    >
      No site is always allowed.
    </p>
    <p
      v-if="error"
      role="alert"
      class="mt-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>
  </section>
</template>
