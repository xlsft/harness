<script setup lang="ts">
// What a source's audit partners said about a skill, word for word. No audit isn't a pass, so it says that too.
import type { SkillAudit } from '~/gen/harness/skills/v1/search_pb'
import { auditTone, shortDate, TONE_CLASS } from '~/lib/extensions/view'

defineProps<{ audits: readonly SkillAudit[] }>()
</script>

<template>
  <div
    class="flex flex-col gap-1 text-[12px]"
    data-audits
  >
    <p
      v-if="!audits.length"
      class="m-0 text-warn"
    >
      {{ tt('The source has no audit of this skill. That isn\'t a pass.') }}
    </p>
    <div
      v-for="a in audits"
      :key="a.provider"
      class="flex flex-wrap items-baseline gap-x-2"
    >
      <span
        class="w-10 shrink-0"
        :class="TONE_CLASS[auditTone(a.status)]"
      >{{ a.status }}</span>
      <span class="text-toned">{{ a.provider }}</span>
      <span
        v-if="a.summary"
        class="min-w-0 text-muted"
      >{{ a.summary }}</span>
      <span
        v-if="a.auditedAt"
        class="text-muted"
      >{{ shortDate(a.auditedAt) }}</span>
    </div>
  </div>
</template>
