<script setup lang="ts">
// The sign-in part of a tracker or knowledge source form, driven by what the provider's capabilities say: the address, the methods
// it offers with their own hints, and its option fields. Nothing here knows a provider; secrets are write-only.
import { AUTH_HELP, AUTH_LABELS, TrackerAuthMethod, takesLogin, takesSecret, type SignInForm, type SignInSpec } from '~/lib/issues/types'

const props = withDefaults(defineProps<{ spec: SignInSpec, address?: boolean }>(), { address: true })
const form = defineModel<SignInForm>({ required: true })

const authOptions = computed(() => props.spec.authMethods.map(m => ({ value: String(m), label: AUTH_LABELS[m] ?? '' })))
const hint = computed(() => props.spec.authHints.find(h => h.method === form.value.auth))
const help = computed(() => hint.value?.help || AUTH_HELP[form.value.auth] || '')
const secretLabel = computed(() => hint.value?.secretLabel || (form.value.auth === TrackerAuthMethod.PASSWORD ? tt('Password') : tt('Token')))
const loginLabel = computed(() => hint.value?.loginLabel || (form.value.auth === TrackerAuthMethod.EMAIL_TOKEN ? tt('Account email') : tt('Login')))
const choices = (values: string[], required: boolean) => [...(required ? [] : [{ value: '', label: tt('Default') }]), ...values.map(v => ({ value: v, label: v }))]
const setOption = (key: string, value: string) => {
  form.value.options = { ...form.value.options, [key]: value }
}
const inputClass = 'border border-line bg-transparent px-2 py-1 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <div class="flex flex-col gap-2">
    <input
      v-if="address"
      v-model="form.baseUrl"
      :placeholder="spec.defaultBaseUrl || spec.baseUrlPlaceholder || tt('https://example.com')"
      :aria-label="tt('Address')"
      :class="[inputClass, 'min-w-[240px]']"
    >
    <div
      v-if="spec.authMethods.length"
      class="flex flex-wrap items-center gap-2 text-[12.5px] text-toned"
    >
      <span>{{ tt('Sign in with') }}</span>
      <div class="w-[190px] shrink-0">
        <HSelect
          :model-value="String(form.auth)"
          :options="authOptions"
          :aria-label="tt('Sign in with')"
          @update:model-value="form.auth = Number($event)"
        />
      </div>
      <input
        v-if="takesLogin(form.auth)"
        v-model="form.login"
        :placeholder="loginLabel"
        :aria-label="loginLabel"
        autocomplete="off"
        :class="[inputClass, 'w-[200px]']"
      >
      <input
        v-if="takesSecret(form.auth)"
        v-model="form.secret"
        type="password"
        autocomplete="new-password"
        :placeholder="form.id ? tt('Leave empty to keep the stored one') : secretLabel"
        :aria-label="secretLabel"
        :class="[inputClass, 'min-w-[200px] flex-1']"
      >
      <template v-if="form.auth === TrackerAuthMethod.OAUTH">
        <input
          v-model="form.oauthClientId"
          :placeholder="tt('OAuth client id')"
          :aria-label="tt('OAuth client id')"
          :class="[inputClass, 'w-[190px]']"
        >
        <input
          v-model="form.oauthClientSecret"
          type="password"
          autocomplete="new-password"
          :placeholder="tt('Client secret, for a confidential client')"
          :aria-label="tt('Client secret')"
          :class="[inputClass, 'w-[240px]']"
        >
      </template>
    </div>
    <p
      v-if="spec.authMethods.length && help"
      class="m-0 text-[11.5px] text-muted"
    >
      {{ help }}
    </p>
    <div
      v-for="f in spec.optionFields"
      :key="f.key"
      class="flex flex-wrap items-center gap-2 text-[12.5px] text-toned"
    >
      <span class="w-[160px] shrink-0">{{ f.label }}<span v-if="f.required"> *</span></span>
      <div
        v-if="f.allowedValues.length"
        class="w-[200px] shrink-0"
      >
        <HSelect
          :model-value="form.options[f.key] ?? f.defaultValue"
          :options="choices(f.allowedValues, f.required)"
          :aria-label="f.label"
          @update:model-value="setOption(f.key, String($event))"
        />
      </div>
      <input
        v-else
        :value="form.options[f.key] ?? ''"
        :placeholder="f.placeholder || f.defaultValue"
        :aria-label="f.label"
        :class="[inputClass, 'min-w-[200px] flex-1']"
        @input="setOption(f.key, ($event.target as HTMLInputElement).value)"
      >
      <span
        v-if="f.help"
        class="w-full text-[11.5px] text-muted"
      >{{ f.help }}</span>
    </div>
  </div>
</template>
