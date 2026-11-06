<script setup lang="ts">
// What the organization imposes on this user, as the client merged it. Read only: there is nothing to change here.
import type { ManagedPolicyView } from '~/lib/transport/types'

defineProps<{ policy: ManagedPolicyView }>()
</script>

<template>
  <dl
    v-if="policy.active"
    aria-label="Set by your administrator"
    class="m-0 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 border border-default px-3 py-3 text-[12px]"
  >
    <template v-if="policy.denyRules.length">
      <dt class="text-muted">
        Denied
      </dt><dd class="m-0 font-mono text-toned">
        {{ policy.denyRules.join(', ') }}
      </dd>
    </template>
    <template v-if="policy.askRules.length">
      <dt class="text-muted">
        Always asks
      </dt><dd class="m-0 font-mono text-toned">
        {{ policy.askRules.join(', ') }}
      </dd>
    </template>
    <template v-if="policy.allowRules.length">
      <dt class="text-muted">
        Allowed
      </dt><dd class="m-0 font-mono text-toned">
        {{ policy.allowRules.join(', ') }}
      </dd>
    </template>
    <template v-if="policy.managedRulesOnly">
      <dt class="text-muted">
        Your rules
      </dt><dd class="m-0 text-toned">
        Ignored: only these rules count.
      </dd>
    </template>
    <template v-if="policy.disabledModes.length">
      <dt class="text-muted">
        Modes off
      </dt><dd class="m-0 text-toned">
        {{ policy.disabledModes.join(', ') }}
      </dd>
    </template>
    <template v-if="policy.sandboxRequired || policy.sandboxOutsideRefused">
      <dt class="text-muted">
        Sandbox
      </dt><dd class="m-0 text-toned">
        {{ [policy.sandboxRequired ? 'required' : '', policy.sandboxOutsideRefused ? 'no running outside it' : ''].filter(Boolean).join(', ') }}
      </dd>
    </template>
    <template v-if="policy.restrictProviders">
      <dt class="text-muted">
        Providers
      </dt><dd class="m-0 text-toned">
        {{ policy.allowedProviders.join(', ') || 'none' }}
      </dd>
    </template>
    <template v-if="policy.personalProvidersDisabled">
      <dt class="text-muted">
        Personal providers
      </dt><dd class="m-0 text-toned">
        Not allowed.
      </dd>
    </template>
    <template v-if="policy.restrictSkills || policy.blockedSkills.length || policy.externalSkillSearchDisabled">
      <dt class="text-muted">
        Skills
      </dt><dd class="m-0 text-toned">
        <template v-if="policy.restrictSkills">
          Only {{ policy.allowedSkills.join(', ') || 'none' }}.
        </template>
        <template v-if="policy.blockedSkills.length">
          Blocked: {{ policy.blockedSkills.join(', ') }}.
        </template>
        <template v-if="policy.externalSkillSearchDisabled">
          External skill search is off.
        </template>
      </dd>
    </template>
    <template v-if="policy.localModelsDisabled">
      <dt class="text-muted">
        Local models
      </dt><dd class="m-0 text-toned">
        Not allowed.
      </dd>
    </template>
    <template v-if="policy.restrictMcpServers">
      <dt class="text-muted">
        MCP servers
      </dt><dd class="m-0 text-toned">
        {{ policy.allowedMcpServers.join(', ') || 'none' }}
      </dd>
    </template>
  </dl>
  <p
    v-else
    class="m-0 text-[12px] text-muted"
  >
    Your administrator has set nothing here.
  </p>
</template>
