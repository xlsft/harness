<script setup lang="ts">
// One server in the detail of Extensions > Connectors: its state and who manages it, its tools with their permission rules, prompts,
// resources and the error if connecting failed. Approval and sign-in live here.
import { ApprovalState, canSignIn, GetOAuthFlowResponse_State, McpServerSource, McpServerState, needsDecision, stateView, TRANSPORT_LABELS, transportName, type InspectMcpServerResponse, type McpApi, type McpRule, type McpServerEntry } from '~/lib/mcp/types'
import { AuthState } from '~/gen/harness/mcp/v1/mcp_pb'
import { isDesktop, openExternal } from '~/lib/desktop'

const props = defineProps<{
  entry: McpServerEntry
  folder: string
  api: McpApi
  inspection?: InspectMcpServerResponse
  inspecting: boolean
}>()
const emit = defineEmits<{ changed: [], edit: [], failed: [message: string] }>()

const busy = ref(false)
const server = computed(() => props.entry.server!)
const state = computed(() => stateView(props.entry))
const local = computed(() => props.entry.source === McpServerSource.DEVICE)
const connected = computed(() => server.value.auth?.state === AuthState.CONNECTED)
const signInAvailable = computed(() => canSignIn(props.entry))
const toolCount = computed(() => props.inspection?.tools.length ?? 0)
const failed = computed(() => props.inspection?.error ?? '')

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

async function run(fn: () => Promise<unknown>) {
  busy.value = true
  try {
    await fn()
    emit('changed')
  } catch (e) {
    emit('failed', message(e))
  } finally {
    busy.value = false
  }
}

async function toggle(enabled: boolean) {
  await run(() => props.api.update({ ...server.value, enabled }, { set: {}, remove: [] }, server.value.version))
}

async function remove() {
  await run(() => props.api.remove(server.value.id))
}

async function approve(approved: boolean) {
  await run(() => props.api.setApproval(server.value.id, props.folder, approved))
}

// OAuth: the flow runs on the server. The user signs in in a browser; this polls until the flow ends.
const signingIn = ref(false)
let cancelled = false
onBeforeUnmount(() => {
  cancelled = true
})
async function signIn() {
  signingIn.value = true
  try {
    const flow = await props.api.startOAuth(server.value.id)
    if (isDesktop()) await openExternal(flow.authorizationUrl)
    else window.open(flow.authorizationUrl, '_blank', 'noopener')
    for (;;) {
      await new Promise(r => setTimeout(r, 2000))
      if (cancelled) return
      const f = await props.api.oauthFlow(flow.flowId)
      if (f.state === GetOAuthFlowResponse_State.COMPLETED) break
      if (f.state !== GetOAuthFlowResponse_State.PENDING) throw new Error(f.errorMessage || 'The sign-in did not finish')
    }
    emit('changed')
  } catch (e) {
    emit('failed', message(e))
  } finally {
    signingIn.value = false
  }
}

async function signOut() {
  await run(() => props.api.revokeAuth(server.value.id))
}

const RULES: { value: string, label: string }[] = [
  { value: 'none', label: tt('Default') },
  { value: 'allow', label: tt('Allow') },
  { value: 'ask', label: tt('Ask') },
  { value: 'deny', label: tt('Deny') }
]
async function setRule(tool: string, value: string) {
  await run(() => props.api.setToolRule(server.value.name, tool, (value === 'none' ? '' : value) as McpRule, local.value))
}
</script>

<template>
  <div class="flex flex-col border border-default">
    <div class="flex flex-wrap items-center gap-3 px-3 py-2.5">
      <div class="flex min-w-0 flex-1 flex-col gap-0.5">
        <span class="flex items-center gap-2 text-[12.5px] text-strong">
          {{ server.name }}
          <span
            class="text-[11px]"
            :class="{ 'text-ok': state.tone === 'ok', 'text-bad': state.tone === 'bad', 'text-muted': state.tone === 'muted' }"
          >{{ state.label }}</span>
          <span
            v-if="inspecting"
            class="text-[11px] text-muted"
          >{{ tt('connecting…') }}</span>
          <span
            v-else-if="inspection?.connected"
            class="text-[11px] text-muted"
          >{{ tt('{0} tool{1}', [toolCount, toolCount === 1 ? '' : 's']) }}</span>
          <span
            v-else-if="failed"
            class="text-[11px] text-bad"
          >{{ tt('cannot connect') }}</span>
        </span>
        <span class="truncate text-[11.5px] text-muted">{{ entry.managedBy }} · {{ TRANSPORT_LABELS[transportName(server.transport)] }} · {{ entry.launchSummary }}</span>
        <span
          v-if="entry.stateDetail"
          class="text-[11.5px] text-muted"
        >{{ entry.stateDetail }}</span>
      </div>

      <template v-if="needsDecision(entry)">
        <HBtn
          size="sm"
          variant="primary"
          :disabled="busy"
          @click="approve(true)"
        >
          {{ tt('Allow') }}
        </HBtn>
        <HBtn
          v-if="entry.state !== McpServerState.DENIED"
          size="sm"
          :disabled="busy"
          @click="approve(false)"
        >
          {{ tt('Deny') }}
        </HBtn>
      </template>
      <HBtn
        v-if="signInAvailable && !connected && entry.state === McpServerState.NEEDS_AUTH"
        size="sm"
        variant="primary"
        :disabled="signingIn"
        @click="signIn"
      >
        {{ signingIn ? tt('Waiting for sign-in…') : tt('Sign in') }}
      </HBtn>
      <HToggle
        v-if="entry.canEdit"
        :model-value="server.enabled"
        :label="tt('Use {0}', [server.name])"
        :disabled="busy"
        @update:model-value="toggle"
      />
    </div>

    <div
      class="flex flex-col gap-3 border-t border-default px-3 py-3 text-[12px]"
    >
      <p
        v-if="failed"
        role="alert"
        class="m-0 text-bad"
      >
        {{ failed }}
      </p>
      <p
        v-if="inspection?.serverName"
        class="m-0 text-muted"
      >
        {{ tt('Reports itself as {0} {1}.', [inspection.serverName, inspection.serverVersion]) }}
        <template v-if="inspection.instructionsChars > 0n">
          {{ tt('Gives the agent {0} characters of instructions, within a size limit.', [inspection.instructionsChars]) }}
        </template>
      </p>
      <p
        v-if="server.auth?.account"
        class="m-0 text-muted"
      >
        {{ tt('Signed in as {0}.', [server.auth.account]) }}
      </p>
      <p
        v-if="server.secrets.length"
        class="m-0 text-muted"
      >
        {{ tt('Secrets: {0}.', [server.secrets.map(s => s.hasValue ? s.name : `${s.name} (not set)`).join(', ')]) }}
      </p>

      <div
        v-if="inspection?.tools.length"
        class="flex flex-col"
      >
        <div class="mb-1 flex items-center gap-2 text-strong">
          <span class="flex-1">{{ tt('Tools') }}</span>
          <span class="text-[11px] text-muted">{{ tt('Whole server') }}</span>
          <div class="w-[110px] shrink-0">
            <HSelect
              :model-value="'none'"
              :options="RULES"
              :aria-label="tt('Rule for every tool of {0}', [server.name])"
              @update:model-value="(v: string) => setRule('', v)"
            />
          </div>
        </div>
        <div
          v-for="t in inspection.tools"
          :key="t.name"
          class="flex items-center gap-2 border-t border-default py-1.5"
        >
          <span class="flex min-w-0 flex-1 flex-col">
            <span class="truncate text-strong">{{ t.title || t.name }}</span>
            <span class="truncate text-[11.5px] text-muted">{{ t.description }}</span>
          </span>
          <div class="w-[110px] shrink-0">
            <HSelect
              :model-value="t.rule || 'none'"
              :options="RULES"
              :aria-label="tt('Rule for {0}', [t.name])"
              @update:model-value="(v: string) => setRule(t.name, v)"
            />
          </div>
        </div>
      </div>

      <div
        v-if="inspection?.prompts.length"
        class="flex flex-col gap-0.5"
      >
        <span class="text-strong">{{ tt('Prompts') }}</span>
        <span
          v-for="p in inspection.prompts"
          :key="p.name"
          class="text-muted"
        >{{ tt('/mcp__{0}__{1}', [server.name, p.name]) }}<template v-if="p.description"> · {{ p.description }}</template></span>
      </div>
      <div
        v-if="inspection?.resources.length"
        class="flex flex-col gap-0.5"
      >
        <span class="text-strong">{{ tt('Resources') }}</span>
        <span
          v-for="r in inspection.resources"
          :key="r.uri || r.uriTemplate"
          class="truncate text-muted"
        >{{ r.uri || r.uriTemplate }}<template v-if="r.description"> · {{ r.description }}</template></span>
      </div>
      <p
        v-if="server.deniedTools.length || server.allowedTools.length"
        class="m-0 text-muted"
      >
        <template v-if="server.allowedTools.length">
          {{ tt('Only these tools exist: {0}.', [server.allowedTools.join(', ')]) }}
        </template>
        <template v-if="server.deniedTools.length">
          {{ tt('Hidden by whoever manages it: {0}.', [server.deniedTools.join(', ')]) }}
        </template>
      </p>

      <div class="flex flex-wrap items-center gap-2">
        <HBtn
          v-if="entry.canEdit"
          size="sm"
          @click="emit('edit')"
        >
          {{ tt('Edit') }}
        </HBtn>
        <HBtn
          v-if="signInAvailable && connected"
          size="sm"
          :disabled="signingIn"
          @click="signIn"
        >
          {{ signingIn ? tt('Waiting for sign-in…') : tt('Reconnect') }}
        </HBtn>
        <HBtn
          v-if="signInAvailable && connected"
          size="sm"
          :disabled="busy"
          @click="signOut"
        >
          {{ tt('Sign out') }}
        </HBtn>
        <HBtn
          v-if="entry.approval === ApprovalState.APPROVED && entry.source === McpServerSource.PROJECT"
          size="sm"
          :disabled="busy"
          @click="approve(false)"
        >
          {{ tt('Stop allowing') }}
        </HBtn>
        <HBtn
          v-if="entry.canRemove"
          size="sm"
          tone="danger"
          :disabled="busy"
          @click="remove"
        >
          {{ tt('Remove') }}
        </HBtn>
      </div>
    </div>
  </div>
</template>
