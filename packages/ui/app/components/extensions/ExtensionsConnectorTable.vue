<script setup lang="ts">
// Yours > Connectors: one table of every server, with who manages it as a badge. The client has folded what plugins share into one row.
// Reading this list connects to nothing: "connects in sessions" says so.
import type { RowMenuItem } from '~/components/RowMenu.vue'
import type { ExtensionConnector } from '~/lib/extensions/types'
import { connectorStatus, providedBy, TONE_CLASS } from '~/lib/extensions/view'

const props = defineProps<{ rows: ExtensionConnector[], cwd: string, act: (f: () => Promise<unknown>, done?: string) => Promise<unknown> }>()
const emit = defineEmits<{ open: [ExtensionConnector], add: [] }>()
const store = useSessionStore()
const mcp = () => store.transport!.mcp()

function menu(c: ExtensionConnector): (RowMenuItem | null)[] {
  const items: (RowMenuItem | null)[] = [{ label: tt('Open'), run: () => emit('open', c) }]
  const e = c.entry
  const s = e?.server
  if (e && s) {
    if (e.canEdit) items.push({ label: s.enabled ? tt('Turn off') : tt('Turn on'), run: () => props.act(() => mcp().update({ ...s, enabled: !s.enabled }, { set: {}, remove: [] }, s.version)) })
    if (e.canRemove) {
      items.push(null, { label: tt('Remove'), run: () => {
        if (window.confirm(tt('Remove the connector {0}?', [s.name]))) return props.act(() => mcp().remove(s.id))
      } })
    }
  }
  return items
}
</script>

<template>
  <table
    v-if="rows.length"
    :aria-label="tt('Connectors')"
    class="w-full border-collapse text-[12.5px]"
  >
    <thead>
      <tr class="border-b border-default text-left text-[12px] font-normal text-muted">
        <th class="py-1.5 pr-3 font-normal">
          {{ tt('Connector') }}
        </th>
        <th class="w-[220px] px-3 py-1.5 font-normal max-md:hidden">
          {{ tt('Type') }}
        </th>
        <th class="w-[170px] px-3 py-1.5 font-normal max-md:w-[118px] max-md:px-2">
          {{ tt('Status') }}
        </th>
        <th class="w-8 p-0">
          <span class="sr-only">{{ tt('Actions') }}</span>
        </th>
      </tr>
    </thead>
    <tbody>
      <tr
        v-for="c in rows"
        :key="c.id"
        class="cursor-pointer border-b border-default hover:bg-hover"
        data-connector-row
        @click="emit('open', c)"
      >
        <td class="max-w-0 py-2 pr-3">
          <button
            type="button"
            class="flex w-full cursor-pointer items-center gap-2.5 text-left"
            @click.stop="emit('open', c)"
          >
            <span class="flex size-7 shrink-0 items-center justify-center border border-default bg-raised text-muted">
              <HIcon
                :name="c.remote ? 'globe' : 'term'"
                :size="14"
              />
            </span>
            <span class="flex min-w-0 flex-col">
              <span class="truncate text-strong">{{ c.name }}</span>
              <span
                v-if="c.providers.length"
                class="truncate text-[12px] text-muted"
              >{{ providedBy(c) }}</span>
              <span class="hidden truncate text-[11px] text-muted max-md:block">{{ c.remote ? tt('Remote') : tt('Local') }}<template v-if="c.badges.length"> · {{ c.badges.join(', ') }}</template></span>
            </span>
          </button>
        </td>
        <td class="px-3 py-2 max-md:hidden">
          <span class="flex flex-wrap items-center gap-1.5 text-toned">
            {{ c.remote ? tt('Remote') : tt('Local') }}
            <span
              v-for="b in c.badges"
              :key="b"
              class="border border-default px-1 text-[10px] leading-[14px] text-muted"
            >{{ b }}</span>
          </span>
        </td>
        <td
          class="px-3 py-2 text-[12px] max-md:px-2"
          :class="TONE_CLASS[connectorStatus(c.status).tone]"
          :title="c.statusDetail"
        >
          <span class="inline-flex items-center gap-1">
            <HIcon
              v-if="connectorStatus(c.status).tone === 'ok'"
              name="check"
              :size="13"
            />
            {{ connectorStatus(c.status).label }}
          </span>
        </td>
        <td
          class="p-0"
          @click.stop
        >
          <RowMenu
            :items="menu(c)"
            :label="tt('Actions for {0}', [c.name])"
            align="end"
          >
            <button
              type="button"
              :aria-label="tt('Actions for {0}', [c.name])"
              class="flex size-7 cursor-pointer items-center justify-center text-muted hover:text-strong"
            >
              <HIcon
                name="more"
                :size="15"
              />
            </button>
          </RowMenu>
        </td>
      </tr>
    </tbody>
  </table>
</template>
