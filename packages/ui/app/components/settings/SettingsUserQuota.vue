<script setup lang="ts">
// A user's quota in Settings > Users: what is set for them, what applies and where it comes from, and the spend against it.
// These are the limits the organization sets. A user's own providers and their limits are not here: those stay theirs.
import type { QuotaLimits, UserQuotaStatus } from '~/lib/enterprise/types'

const props = defineProps<{ userId: string, name: string, readonly?: boolean }>()
const emit = defineEmits<{ error: [string] }>()
const store = useSessionStore()

const status = ref<UserQuotaStatus | null>(null)

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function load() {
  try {
    status.value = await store.transport!.enterprise().getUserQuota(props.userId)
  } catch (e) {
    emit('error', message(e))
  }
}
watch(() => props.userId, load, { immediate: true })

async function save(l: QuotaLimits) {
  try {
    await store.transport!.enterprise().setUserQuota(props.userId, l)
    await load()
  } catch (e) {
    emit('error', message(e))
  }
}

const usd = (n: number | undefined) => n === undefined ? 'no limit' : `$${n.toFixed(2)}`
function from(source: string) {
  if (source === 'user') return 'set here'
  if (source === 'role') return 'from the role'
  if (source.startsWith('group:')) return `from group ${source.slice(6)}`
  return ''
}
</script>

<template>
  <div class="flex flex-col gap-1.5">
    <span class="text-[11.5px] text-muted">Spend limits</span>
    <template v-if="status">
      <SettingsQuotaForm
        :limits="status.own"
        :name="name"
        :readonly="readonly"
        @save="save"
      />
      <span class="text-[11.5px] text-dimmed">
        Today {{ usd(status.dailySpentUsd) }} of {{ usd(status.effective.dailyUsd) }}<template v-if="from(status.dailySource)"> ({{ from(status.dailySource) }})</template>.
        This month {{ usd(status.monthlySpentUsd) }} of {{ usd(status.effective.monthlyUsd) }}<template v-if="from(status.monthlySource)"> ({{ from(status.monthlySource) }})</template>.
        Personal providers do not count.
      </span>
    </template>
    <span
      v-else
      class="text-[11.5px] text-dimmed"
    >Loading…</span>
  </div>
</template>
