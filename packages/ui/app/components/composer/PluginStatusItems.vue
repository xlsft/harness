<script setup lang="ts">
// Status-bar items of plugins (docs/design/plugin-ui.md): a plugin's text and icon in the composer footer. A click opens its
// pane, or sends its fixed prompt to the session; sending needs session.prompt approved on this device, asked for here.
import type { SessionState } from '~/lib/session/types'
import { capabilityLabel, type PluginStatusItemVM } from '~/lib/plugins/types'

const props = defineProps<{ session: SessionState }>()
const store = useSessionStore()
const { lists, version, load, show } = usePluginPane()

const sid = computed(() => props.session.meta.id)
const list = computed(() => lists.value[sid.value])
watch(() => [sid.value, props.session.meta.cwd, version.value], () => load(sid.value), { immediate: true })

const toast = useToast()
const asking = ref<PluginStatusItemVM | null>(null)
const problem = ref('')
const key = (it: PluginStatusItemVM) => `${it.pluginId}/${it.id}`

async function send(it: PluginStatusItemVM, approved = false) {
  problem.value = ''
  try {
    const api = store.transport!.plugins()
    if (approved) await api.setUiGrant(it.pluginId, 'session.prompt', true, list.value?.contentHashes[it.pluginId] ?? '')
    const r = await api.callUi(it.pluginId, sid.value, 'session.prompt', JSON.stringify({ text: it.prompt }))
    asking.value = r.needsApproval && !approved ? it : null
  } catch (e) {
    problem.value = e instanceof Error ? e.message : String(e)
    if (!asking.value) toast.add({ title: problem.value, color: 'error' })
  }
}

function click(it: PluginStatusItemVM) {
  if (it.pane) {
    const pane = list.value?.panes.find(p => p.pluginId === it.pluginId && p.id === it.pane)
    show(sid.value, { pluginId: it.pluginId, paneId: it.pane, title: pane?.title ?? it.text })
    return
  }
  void send(it)
}
</script>

<template>
  <span
    v-if="list?.statusItems.length"
    class="flex shrink-0 items-center"
    data-plugin-status
  >
    <UPopover
      v-for="it in list.statusItems"
      :key="key(it)"
      :open="asking !== null && key(asking) === key(it)"
      :content="{ side: 'top', align: 'start', sideOffset: 6 }"
      @update:open="v => { if (!v) asking = null }"
    >
      <button
        type="button"
        :title="it.tooltip || (it.prompt ? tt('Sends: {0}', [it.prompt]) : it.text)"
        :aria-label="`${list.pluginNames[it.pluginId] ?? it.pluginId}: ${it.text}`"
        class="flex shrink-0 items-center gap-[5px] px-2 py-1 text-[11.5px] whitespace-nowrap text-muted hover:bg-hover hover:text-toned"
        @click="click(it)"
      >
        <HIcon
          :name="pluginIcon(it.icon, 'box')"
          :size="13"
        />{{ it.text }}
      </button>
      <template #content>
        <div
          role="dialog"
          :aria-label="tt('Allow {0}?', [list.pluginNames[it.pluginId] ?? it.pluginId])"
          class="flex w-[320px] max-w-[calc(100vw-32px)] flex-col gap-2 border border-accented bg-hover px-[13px] py-3 text-[12.5px]"
        >
          <p class="m-0 text-toned">
            {{ tt('{0} asks to:', [list.pluginNames[it.pluginId] ?? it.pluginId]) }} <span class="text-strong">{{ capabilityLabel('session.prompt') }}</span>
          </p>
          <p class="m-0 text-[11.5px] break-words text-muted">
            {{ tt('This click sends: {0}', [it.prompt]) }}
          </p>
          <p
            v-if="problem"
            class="m-0 text-[11.5px] text-bad"
          >
            {{ problem }}
          </p>
          <div class="flex gap-1.5">
            <HBtn
              variant="primary"
              size="sm"
              @click="send(it, true)"
            >
              {{ tt('Allow and send') }}
            </HBtn>
            <HBtn
              size="sm"
              @click="asking = null"
            >
              {{ tt('Cancel') }}
            </HBtn>
          </div>
        </div>
      </template>
    </UPopover>
  </span>
</template>
