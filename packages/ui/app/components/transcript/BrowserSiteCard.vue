<script setup lang="ts">
// The session's browser runs in the user's own Chrome and the agent wants a site it has no grant for. Pages stay out of reach
// until this is answered; once resolved it collapses to one muted line.
import type { BrowserSiteItem } from '~/lib/session/types'
import type { BrowserSiteAnswer } from '~/lib/browser/types'

const props = defineProps<{ item: BrowserSiteItem, sessionId: string }>()
const store = useSessionStore()

const sending = ref(false)
const error = ref('')

async function answer(a: BrowserSiteAnswer) {
  if (props.item.state !== 'pending' || sending.value || !store.transport) return
  sending.value = true
  error.value = ''
  try {
    await store.transport.browser().respondToSite(props.sessionId, props.item.id, a)
  } catch (e) {
    error.value = e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
  } finally {
    sending.value = false
  }
}

const summary = computed(() => {
  if (props.item.state === 'cancelled') return `${props.item.site} was not answered`
  return props.item.answer === 'deny' ? `${props.item.site} refused in your Chrome` : `${props.item.site} allowed in your Chrome`
})
</script>

<template>
  <div
    v-if="item.state === 'pending'"
    class="border border-accent bg-surface"
    role="group"
    :aria-label="`Allow ${item.site} in your Chrome?`"
  >
    <div class="flex items-center gap-[9px] border-b border-default px-3 py-2 text-[12.5px]">
      <HIcon
        name="globe"
        class="text-accent"
      />
      <span class="text-strong">Let the agent use {{ item.site }} in your Chrome?</span>
      <span class="flex-1" />
      <span class="text-[11px] text-muted">waiting</span>
    </div>
    <div class="flex flex-col gap-2.5 px-3 py-2.5 text-[12.5px]">
      <span class="break-all text-muted">{{ item.url }}</span>
      <span class="text-toned">It would act there with your sign-ins, as you. Other sites still ask.</span>
      <div class="flex flex-wrap gap-2">
        <HBtn
          variant="primary"
          :disabled="sending"
          @click="answer('session')"
        >
          Allow for this session
        </HBtn>
        <HBtn
          :disabled="sending"
          @click="answer('always')"
        >
          Always on this device
        </HBtn>
        <HBtn
          tone="danger"
          :disabled="sending"
          @click="answer('deny')"
        >
          Refuse
        </HBtn>
      </div>
      <span
        v-if="error"
        role="alert"
        class="text-bad"
      >{{ error }}</span>
    </div>
  </div>
  <div
    v-else
    class="text-[12px] text-muted"
  >
    {{ summary }}
  </div>
</template>
