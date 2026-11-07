<script setup lang="ts">
// What a plugin holds, in the names it would go by. This is what the user approves, so what runs programs says exactly what it runs.
import { capabilityLabel, type PluginContentsVM } from '~/lib/plugins/types'

defineProps<{ contents: PluginContentsVM }>()
</script>

<template>
  <dl
    class="m-0 grid grid-cols-[110px_1fr] gap-x-3 gap-y-1.5 text-[12px] max-sm:grid-cols-1"
    data-contents
  >
    <template v-if="contents.skills.length">
      <dt class="text-muted">
        {{ tt('Skills') }}
      </dt><dd class="m-0 text-toned">
        {{ contents.skills.join(', ') }}
      </dd>
    </template>
    <template v-if="contents.commands.length">
      <dt class="text-muted">
        {{ tt('Commands') }}
      </dt><dd class="m-0 text-toned">
        {{ contents.commands.join(', ') }}
      </dd>
    </template>
    <template v-if="contents.agents.length">
      <dt class="text-muted">
        {{ tt('Agents') }}
      </dt><dd class="m-0 text-toned">
        {{ contents.agents.join(', ') }}
      </dd>
    </template>
    <template v-if="contents.outputStyles.length">
      <dt class="text-muted">
        {{ tt('Output styles') }}
      </dt><dd class="m-0 text-toned">
        {{ contents.outputStyles.join(', ') }}
      </dd>
    </template>
    <template v-if="contents.allowedTools.length">
      <dt class="text-muted">
        {{ tt('Tools without asking') }}
      </dt><dd class="m-0 text-warn">
        {{ contents.allowedTools.join(', ') }}
      </dd>
    </template>
    <template v-if="contents.hooks.length">
      <dt class="text-muted">
        {{ tt('Hooks') }}
      </dt><dd class="m-0 flex flex-col gap-1">
        <span
          v-for="(h, i) in contents.hooks"
          :key="i"
          class="break-all text-warn"
        >{{ h.event }}<template v-if="h.matcher"> ({{ h.matcher }})</template><template v-if="h.fromSkill">{{ tt(', while {0} runs', [h.fromSkill]) }}</template>: <code>{{ h.command }}</code></span>
      </dd>
    </template>
    <template v-if="contents.mcpServers.length">
      <dt class="text-muted">
        {{ tt('Connectors') }}
      </dt><dd class="m-0 flex flex-col gap-1">
        <span
          v-for="m in contents.mcpServers"
          :key="m.name"
          class="break-all text-warn"
        >{{ m.name }} ({{ m.transport }}): <code>{{ m.command }}</code></span>
      </dd>
    </template>
    <template v-if="contents.panes.length">
      <dt class="text-muted">
        {{ tt('Panes') }}
      </dt><dd class="m-0 text-toned">
        {{ contents.panes.map(p => `${p.title} (${p.page})`).join(', ') }}
      </dd>
    </template>
    <template v-if="contents.statusItems.length">
      <dt class="text-muted">
        {{ tt('Status bar') }}
      </dt><dd class="m-0 flex flex-col gap-1 text-toned">
        <span
          v-for="it in contents.statusItems"
          :key="it.id"
          class="break-words"
        >{{ it.text }}: <template v-if="it.pane">{{ tt('opens {0}', [contents.panes.find(p => p.id === it.pane)?.title ?? it.pane]) }}</template><template v-else>{{ tt('sends “{0}”', [it.prompt]) }}</template></span>
      </dd>
    </template>
    <template v-if="contents.uiCapabilities.length || contents.uiNetwork.length">
      <dt class="text-muted">
        {{ tt('Its panes may ask to') }}
      </dt><dd class="m-0 flex flex-col gap-1 text-toned">
        <span
          v-for="c in contents.uiCapabilities"
          :key="c"
        >{{ capabilityLabel(c) }}</span>
        <span
          v-if="contents.uiNetwork.length"
          class="break-all text-warn"
        >{{ capabilityLabel('network', contents.uiNetwork) }}</span>
      </dd>
    </template>
    <template v-if="contents.skipped.length">
      <dt class="text-muted">
        {{ tt('Not acted on') }}
      </dt><dd class="m-0 text-muted">
        {{ contents.skipped.join(', ') }}
      </dd>
    </template>
  </dl>
</template>
