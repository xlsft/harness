<script setup lang="ts">
// Settings > Managed policy: the layer an administrator sets and clients obey, above every user, project and local setting.
// Admins edit it, auditors read it, and everyone else sees what applies to them as the client merged it (UserPreferences).
import { emptyManagedPolicy, lines, type ManagedPolicy, type ManagedPolicyState } from '~/lib/enterprise/types'
import type { ManagedPolicyView } from '~/lib/transport/types'

const store = useSessionStore()
const access = useAccess()
const canRead = computed(() => access.can('policy.read'))
const writable = computed(() => access.can('policy.write'))

const state = ref<ManagedPolicyState | null>(null)
const policy = ref<ManagedPolicy>(emptyManagedPolicy())
const allow = ref('')
const ask = ref('')
const deny = ref('')
const providers = ref('')
const servers = ref('')
const pluginAllowed = ref('')
const pluginBlocked = ref('')
const pluginMarkets = ref('')
const pluginsOn = ref('')
const pluginsOff = ref('')
const skillsAllowed = ref('')
const skillsBlocked = ref('')
const view = ref<ManagedPolicyView | null>(null)
const error = ref('')
const saved = ref(false)

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

function show(s: ManagedPolicyState) {
  state.value = s
  policy.value = { ...s.policy }
  allow.value = s.policy.allowRules.join('\n')
  ask.value = s.policy.askRules.join('\n')
  deny.value = s.policy.denyRules.join('\n')
  providers.value = s.policy.allowedProviders.join('\n')
  servers.value = s.policy.allowedMcpServers.join('\n')
  pluginAllowed.value = s.policy.allowedPluginSources.join('\n')
  pluginBlocked.value = s.policy.blockedPluginSources.join('\n')
  pluginMarkets.value = s.policy.distributedMarketplaces.join('\n')
  pluginsOn.value = s.policy.distributedPlugins.join('\n')
  pluginsOff.value = s.policy.blockedPlugins.join('\n')
  skillsAllowed.value = s.policy.allowedSkills.join('\n')
  skillsBlocked.value = s.policy.blockedSkills.join('\n')
}

onMounted(async () => {
  await access.load()
  try {
    if (canRead.value) show(await store.transport!.enterprise().getManagedPolicy())
    else view.value = (await store.transport!.getUserPreferences()).managedPolicy ?? null
  } catch (e) {
    error.value = message(e)
  }
})

async function save() {
  saved.value = false
  try {
    show(await store.transport!.enterprise().setManagedPolicy({
      ...policy.value,
      allowRules: lines(allow.value),
      askRules: lines(ask.value),
      denyRules: lines(deny.value),
      allowedProviders: lines(providers.value),
      allowedMcpServers: lines(servers.value),
      allowedPluginSources: lines(pluginAllowed.value),
      blockedPluginSources: lines(pluginBlocked.value),
      distributedMarketplaces: lines(pluginMarkets.value),
      distributedPlugins: lines(pluginsOn.value),
      blockedPlugins: lines(pluginsOff.value),
      allowedSkills: lines(skillsAllowed.value),
      blockedSkills: lines(skillsBlocked.value)
    }))
    error.value = ''
    saved.value = true
  } catch (e) {
    error.value = message(e)
  }
}

function addProvider(id: string) {
  if (!lines(providers.value).includes(id)) providers.value = [...lines(providers.value), id].join('\n')
}

function addServer(id: string) {
  if (!lines(servers.value).includes(id)) servers.value = [...lines(servers.value), id].join('\n')
}

const area = 'min-h-[72px] w-full resize-y border border-accented bg-default px-2 py-1.5 font-mono text-[12px] text-strong outline-none focus:border-accent disabled:opacity-60'
const label = 'flex flex-col gap-1 text-[11.5px] text-muted'
</script>

<template>
  <div class="flex flex-col">
    <h2 class="set-h">
      {{ tt('Managed policy') }}
    </h2>
    <p class="set-sub">
      {{ tt('Set by an administrator for everyone on this server. It comes first: clients obey it and nothing in user, project or local settings can override it. The server itself refuses providers and MCP servers it does not allow.') }}
    </p>
    <p
      v-if="error"
      role="alert"
      class="m-0 mb-3 border border-bad px-3 py-2 text-[12px] text-bad"
    >
      {{ error }}
    </p>

    <form
      v-if="state"
      class="flex flex-col gap-3"
      :aria-label="tt('Managed policy')"
      @submit.prevent="save"
    >
      <h3 class="m-0 mt-2 text-[12.5px] font-semibold text-strong">
        {{ tt('Permission rules') }}
      </h3>
      <p class="m-0 text-[12px] text-muted">
        {{ tt('One per line, in the settings syntax:') }} <code>Bash(rm:*)</code>, <code>Read</code>, <code>WebFetch(domain:example.com)</code>{{ tt('. Deny beats ask beats allow.') }}
      </p>
      <div class="grid grid-cols-3 gap-3 max-md:grid-cols-1">
        <label :class="label">{{ tt('Deny') }}
          <textarea
            v-model="deny"
            :disabled="!writable"
            spellcheck="false"
            :class="area"
          />
        </label>
        <label :class="label">{{ tt('Ask') }}
          <textarea
            v-model="ask"
            :disabled="!writable"
            spellcheck="false"
            :class="area"
          />
        </label>
        <label :class="label">{{ tt('Allow') }}
          <textarea
            v-model="allow"
            :disabled="!writable"
            spellcheck="false"
            :class="area"
          />
        </label>
      </div>
      <div class="set-row">
        <div class="set-label">
          <span>{{ tt('Only these rules') }}</span>
          <span class="set-desc">{{ tt('Rules in user, project and local settings are ignored.') }}</span>
        </div>
        <HToggle
          v-model="policy.managedRulesOnly"
          :disabled="!writable"
          :label="tt('Only managed permission rules')"
        />
      </div>

      <h3 class="m-0 mt-2 text-[12.5px] font-semibold text-strong">
        {{ tt('Sandbox') }}
      </h3>
      <div class="set-row">
        <div class="set-label">
          <span>{{ tt('Require the sandbox') }}</span>
          <span class="set-desc">{{ tt('Nobody can turn it off. On a platform without a sandbox yet, commands are refused.') }}</span>
        </div>
        <HToggle
          v-model="policy.sandboxRequired"
          :disabled="!writable"
          :label="tt('Require the sandbox')"
        />
      </div>
      <div class="set-row">
        <div class="set-label">
          <span>{{ tt('Never outside it') }}</span>
          <span class="set-desc">{{ tt('The way to run one command outside the sandbox is taken away.') }}</span>
        </div>
        <HToggle
          v-model="policy.sandboxOutsideRefused"
          :disabled="!writable"
          :label="tt('Refuse running outside the sandbox')"
        />
      </div>

      <h3 class="m-0 mt-2 text-[12.5px] font-semibold text-strong">
        {{ tt('Providers') }}
      </h3>
      <div class="set-row">
        <div class="set-label">
          <span>{{ tt('Only allowed providers') }}</span>
          <span class="set-desc">{{ tt('Provider or preset ids, one per line. On clients,') }} <code>local</code> {{ tt('allows local models and') }} <code>acp</code> {{ tt('or') }} <code>acp:&lt;id&gt;</code> {{ tt('external agents. An empty list allows none.') }}</span>
        </div>
        <HToggle
          v-model="policy.restrictProviders"
          :disabled="!writable"
          :label="tt('Only allowed providers')"
        />
      </div>
      <template v-if="policy.restrictProviders">
        <textarea
          v-model="providers"
          :aria-label="tt('Allowed providers')"
          :disabled="!writable"
          spellcheck="false"
          :class="area"
        />
        <div
          v-if="writable && state.providers.length"
          class="flex flex-wrap gap-1"
        >
          <HBtn
            v-for="c in state.providers"
            :key="c.id"
            size="xs"
            @click="addProvider(c.id)"
          >
            + {{ c.label }}
          </HBtn>
        </div>
      </template>
      <div class="set-row">
        <div class="set-label">
          <span>{{ tt('No personal providers') }}</span>
          <span class="set-desc">{{ tt('People cannot add or use their own.') }}</span>
        </div>
        <HToggle
          v-model="policy.personalProvidersDisabled"
          :disabled="!writable"
          :label="tt('No personal providers')"
        />
      </div>

      <h3 class="m-0 mt-2 text-[12.5px] font-semibold text-strong">
        {{ tt('Always-on hosts') }}
      </h3>
      <div class="set-row">
        <div class="set-label">
          <span>{{ tt('Only hosts an admin registered') }}</span>
          <span class="set-desc">{{ tt('Nobody registers a host of their own. Hosts need a sandbox driver when the sandbox is required.') }}</span>
        </div>
        <HToggle
          v-model="policy.personalHostsDisabled"
          :disabled="!writable"
          :label="tt('Only hosts an admin registered')"
        />
      </div>

      <h3 class="m-0 mt-2 text-[12.5px] font-semibold text-strong">
        {{ tt('MCP servers') }}
      </h3>
      <div class="set-row">
        <div class="set-label">
          <span>{{ tt('Only allowed servers') }}</span>
          <span class="set-desc">{{ tt('Names, one per line, at any scope, a repository\'s') }} <code>.mcp.json</code> {{ tt('included. An empty list allows none.') }}</span>
        </div>
        <HToggle
          v-model="policy.restrictMcpServers"
          :disabled="!writable"
          :label="tt('Only allowed MCP servers')"
        />
      </div>
      <template v-if="policy.restrictMcpServers">
        <textarea
          v-model="servers"
          :aria-label="tt('Allowed MCP servers')"
          :disabled="!writable"
          spellcheck="false"
          :class="area"
        />
        <div
          v-if="writable && state.mcpServers.length"
          class="flex flex-wrap gap-1"
        >
          <HBtn
            v-for="c in state.mcpServers"
            :key="c.id"
            size="xs"
            @click="addServer(c.id)"
          >
            + {{ c.label }}
          </HBtn>
        </div>
      </template>
      <div class="set-row">
        <div class="set-label">
          <span>{{ tt('Plugins only from allowed sources') }}</span>
          <span class="set-desc">{{ tt('One per line:') }} <code>owner/repo</code>, <code>owner/*</code>{{ tt(', a git or') }} <code>marketplace.json</code> {{ tt('URL, an absolute path,') }} <code>host:regexp</code> {{ tt('or') }} <code>path:regexp</code>{{ tt('. An empty list allows none. Clients check before a download and again when a plugin would load.') }}</span>
        </div>
        <HToggle
          v-model="policy.restrictPluginSources"
          :disabled="!writable"
          :label="tt('Plugins only from allowed sources')"
        />
      </div>
      <textarea
        v-if="policy.restrictPluginSources"
        v-model="pluginAllowed"
        :aria-label="tt('Allowed plugin sources')"
        :disabled="!writable"
        spellcheck="false"
        :class="area"
      />
      <div class="set-label mt-2">
        <span>{{ tt('Blocked plugin sources') }}</span>
        <span class="set-desc">{{ tt('The same forms, checked first.') }}</span>
      </div>
      <textarea
        v-model="pluginBlocked"
        :aria-label="tt('Blocked plugin sources')"
        :disabled="!writable"
        spellcheck="false"
        :class="area"
      />
      <div class="set-label mt-2">
        <span>{{ tt('Distributed marketplaces') }}</span>
        <span class="set-desc"><code>name=source</code>{{ tt(', one per line: registered on every device.') }}</span>
      </div>
      <textarea
        v-model="pluginMarkets"
        :aria-label="tt('Distributed marketplaces')"
        :disabled="!writable"
        spellcheck="false"
        :class="area"
      />
      <div class="set-label mt-2">
        <span>{{ tt('Distributed plugins') }}</span>
        <span class="set-desc"><code>name@marketplace</code>{{ tt(', one per line: fetched on every device and kept on. Each device still approves what runs on it.') }}</span>
      </div>
      <textarea
        v-model="pluginsOn"
        :aria-label="tt('Distributed plugins')"
        :disabled="!writable"
        spellcheck="false"
        :class="area"
      />
      <div class="set-label mt-2">
        <span>{{ tt('Blocked plugins') }}</span>
        <span class="set-desc"><code>name@marketplace</code>{{ tt(', one per line.') }}</span>
      </div>
      <textarea
        v-model="pluginsOff"
        :aria-label="tt('Blocked plugins')"
        :disabled="!writable"
        spellcheck="false"
        :class="area"
      />

      <div class="flex items-center justify-between gap-4">
        <div class="set-label">
          <span>{{ tt('Only these skills') }}</span>
          <span class="set-desc">{{ tt('One name per line;') }} <code>kit:*</code> {{ tt('names all of a plugin. An empty list allows none. Built-in commands aren\'t skills. Clients enforce it and show it as set by you.') }}</span>
        </div>
        <HToggle
          v-model="policy.restrictSkills"
          :disabled="!writable"
          :label="tt('Only these skills')"
        />
      </div>
      <textarea
        v-if="policy.restrictSkills"
        v-model="skillsAllowed"
        :aria-label="tt('Allowed skills')"
        :disabled="!writable"
        spellcheck="false"
        :class="area"
      />
      <div class="set-label mt-2">
        <span>{{ tt('Blocked skills') }}</span>
        <span class="set-desc">{{ tt('The same forms, checked first.') }}</span>
      </div>
      <textarea
        v-model="skillsBlocked"
        :aria-label="tt('Blocked skills')"
        :disabled="!writable"
        spellcheck="false"
        :class="area"
      />
      <div class="flex items-center justify-between gap-4">
        <div class="set-label">
          <span>{{ tt('Turn off external skill search') }}</span>
          <span class="set-desc">{{ tt('No search of public skill directories, on the server or on clients. Skills already installed from it stay until you block them.') }}</span>
        </div>
        <HToggle
          v-model="policy.externalSkillSearchDisabled"
          :disabled="!writable"
          :label="tt('Turn off external skill search')"
        />
      </div>

      <div
        v-if="writable"
        class="flex items-center justify-end gap-3"
      >
        <span
          v-if="saved"
          role="status"
          class="text-[12px] text-ok"
        >{{ tt('Saved. Clients pick it up from their sync feed; sessions already open apply rules from their next start.') }}</span>
        <HBtn
          size="md"
          variant="primary"
          type="submit"
        >
          {{ tt('Save') }}
        </HBtn>
      </div>
    </form>

    <SettingsManagedNotice
      v-else-if="view"
      :policy="view"
    />
    <p
      v-else-if="!error"
      class="m-0 py-4 text-[12.5px] text-muted"
    >
      {{ tt('Loading…') }}
    </p>
  </div>
</template>
