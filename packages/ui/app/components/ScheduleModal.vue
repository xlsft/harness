<script setup lang="ts">
// Creating or editing a scheduled task in a dialog. The common schedules are plain choices (hourly, daily, weekdays,
// weekly at a time) that become a cron line; anything else is typed as one. The client checks what is saved.
import {
  cronOfSimple,
  FREQUENCY_OPTIONS,
  simpleOfCron,
  TRIGGER_OPTIONS,
  WEEKDAY_OPTIONS,
  type Frequency,
  type ScheduleDraft
} from '~/lib/schedule/types'

interface Option { value: string, label: string }

const props = defineProps<{
  draft: ScheduleDraft
  folders: Option[]
  modes: Option[]
  hosts: Option[]
  // What the client said about the last save.
  error?: string
  // Saves the draft; true closes the dialog.
  save: (d: ScheduleDraft) => Promise<boolean>
}>()
const emit = defineEmits<{ close: [] }>()

// False plays the closing animation; the parent hears close once it has finished.
const shown = ref(true)
const d = reactive<ScheduleDraft>({ ...props.draft })
const saving = ref(false)
const advanced = ref(false)

const simple = simpleOfCron(d.cron)
const frequency = ref<Frequency>(simple.frequency)
const time = ref(simple.time)
const minute = ref(simple.time.slice(3))
const weekday = ref(String(simple.weekday))

watch([frequency, time, minute, weekday], () => {
  if (frequency.value === 'custom') return
  const t = frequency.value === 'hourly' ? `00:${minute.value.padStart(2, '0')}` : time.value
  d.cron = cronOfSimple({ frequency: frequency.value, time: t, weekday: Number(weekday.value) }, d.cron)
})

const title = computed(() => (d.id ? tt('Edit scheduled task') : tt('New scheduled task')))
const canSave = computed(() => d.prompt.trim().length > 0 && !saving.value)

async function submit() {
  if (!canSave.value) return
  saving.value = true
  try {
    if (await props.save({ ...d })) shown.value = false
  } finally {
    saving.value = false
  }
}

const field = 'h-9 w-full border border-accented bg-default px-3 text-[13px] text-strong outline-none focus:border-accent'
const area = 'w-full resize-y border border-accented bg-default px-3 py-2.5 text-[13px] leading-[1.6] text-strong outline-none placeholder:text-dimmed focus:border-accent'
</script>

<template>
  <Teleport to="body">
    <Transition
      name="h-modal"
      appear
      @after-leave="emit('close')"
    >
      <div
        v-if="shown"
        class="fixed inset-0 z-40 flex items-center justify-center bg-ground/60 p-6"
        @pointerdown.self="shown = false"
        @keydown.esc="shown = false"
      >
        <form
          role="dialog"
          aria-modal="true"
          :aria-label="title"
          class="flex max-h-full w-[min(560px,100%)] flex-col overflow-hidden rounded-panel border border-line-strong bg-default text-[12.5px]"
          @submit.prevent="submit"
        >
          <div class="flex h-12 shrink-0 items-center gap-2 pr-2 pl-5">
            <h2 class="m-0 flex-1 text-[14px] font-semibold text-strong">
              {{ title }}
            </h2>
            <HIconBtn
              icon="close"
              :label="tt('Close')"
              @click="shown = false"
            />
          </div>

          <div class="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-5 pb-4">
            <label class="flex flex-col gap-1.5 text-strong">
              {{ tt('Name') }}
              <input
                v-model="d.name"
                :class="field"
                :placeholder="tt('Optional')"
              >
            </label>

            <label class="flex flex-col gap-1.5 text-strong">
              {{ tt('Instructions') }}
              <textarea
                v-model="d.prompt"
                rows="7"
                :class="area"
                :placeholder="tt('What to do on each run. A slash command works too.')"
              />
            </label>

            <div class="flex flex-col gap-1.5">
              <span class="text-strong">{{ tt('Trigger') }}</span>
              <div class="flex items-center gap-2">
                <div class="min-w-0 flex-1">
                  <HSelect
                    v-model="d.trigger"
                    :options="TRIGGER_OPTIONS"
                    :aria-label="tt('Trigger')"
                  />
                </div>
                <template v-if="d.trigger === 'cron'">
                  <div class="min-w-0 flex-1">
                    <HSelect
                      v-model="frequency"
                      :options="FREQUENCY_OPTIONS"
                      :aria-label="tt('How often')"
                    />
                  </div>
                  <div
                    v-if="frequency === 'weekly'"
                    class="min-w-0 flex-1"
                  >
                    <HSelect
                      v-model="weekday"
                      :options="WEEKDAY_OPTIONS"
                      :aria-label="tt('Day')"
                    />
                  </div>
                  <label
                    v-if="frequency === 'hourly'"
                    class="flex shrink-0 items-center gap-1.5 text-muted"
                  >
                    {{ tt('at minute') }}
                    <input
                      v-model="minute"
                      inputmode="numeric"
                      maxlength="2"
                      :class="[field, '!w-16']"
                      :aria-label="tt('Minute')"
                    >
                  </label>
                  <input
                    v-else-if="frequency !== 'custom'"
                    v-model="time"
                    type="time"
                    :class="[field, '!w-[120px] shrink-0']"
                    :aria-label="tt('Time')"
                  >
                  <input
                    v-else
                    v-model="d.cron"
                    :class="[field, 'min-w-0 flex-1 font-mono']"
                    :aria-label="tt('Minute, hour, day, month, weekday')"
                    placeholder="0 9 * * 1-5"
                  >
                </template>
                <input
                  v-else-if="d.trigger === 'once'"
                  v-model="d.at"
                  type="datetime-local"
                  :class="field"
                  :aria-label="tt('At')"
                >
                <label
                  v-else-if="d.trigger === 'interval'"
                  class="flex items-center gap-1.5 text-muted"
                >
                  <input
                    v-model="d.everyMinutes"
                    inputmode="decimal"
                    :class="[field, '!w-20']"
                    :aria-label="tt('Minutes between runs')"
                  >
                  {{ tt('minutes') }}
                </label>
              </div>
              <textarea
                v-if="d.trigger === 'file'"
                v-model="d.paths"
                rows="2"
                :class="[area, 'font-mono']"
                :aria-label="tt('Files to watch in the folder, one pattern per line')"
                placeholder="src/**/*.go"
              />
              <p
                v-if="d.trigger === 'file'"
                class="m-0 text-[11.5px] text-muted"
              >
                {{ tt('Files to watch in the folder, one pattern per line.') }}
              </p>
              <p
                v-if="d.trigger === 'webhook'"
                class="m-0 text-[11.5px] text-muted"
              >
                {{ tt('The address to post to appears in the list once the task has reached the server. What is posted reaches the run as data, never as instructions.') }}
              </p>
            </div>

            <div class="flex items-center justify-between gap-3 border-t border-line pt-4">
              <span class="flex flex-col gap-0.5">
                <span class="text-strong">{{ tt('Folder') }}</span>
                <span class="text-[11.5px] text-muted">{{ tt('Where each run works. Without one it only talks.') }}</span>
              </span>
              <div class="w-[240px] shrink-0">
                <HSelect
                  v-model="d.cwd"
                  :options="folders"
                  :aria-label="tt('Folder')"
                />
              </div>
            </div>

            <div class="flex flex-col gap-3 border-t border-line pt-4">
              <button
                type="button"
                class="flex items-center text-left text-strong"
                :aria-expanded="advanced"
                @click="advanced = !advanced"
              >
                <span class="flex-1">{{ tt('Advanced settings') }}</span>
                <HIcon
                  name="down"
                  :size="12"
                  class="text-muted transition-transform duration-150"
                  :class="advanced && 'rotate-180'"
                />
              </button>
              <template v-if="advanced">
                <div class="flex items-center justify-between gap-3">
                  <span class="text-toned">{{ tt('Permissions') }}</span>
                  <div class="w-[240px] shrink-0">
                    <HSelect
                      v-model="d.permissionMode"
                      :options="modes"
                      :aria-label="tt('Permissions')"
                    />
                  </div>
                </div>
                <label class="flex items-center justify-between gap-3 text-toned">
                  {{ tt('Run once when back if a time was missed') }}
                  <HToggle
                    v-model="d.catchUp"
                    :label="tt('Run once when back if a time was missed')"
                  />
                </label>
                <label class="flex items-center justify-between gap-3 text-toned">
                  {{ tt('Each run on its own copy of the repository') }}
                  <HToggle
                    v-model="d.worktree"
                    :label="tt('Each run on its own copy of the repository')"
                  />
                </label>
                <div
                  v-if="hosts.length > 1"
                  class="flex flex-col gap-2"
                >
                  <div class="flex items-center justify-between gap-3">
                    <span class="text-toned">{{ tt('While its machine is away') }}</span>
                    <div class="w-[240px] shrink-0">
                      <HSelect
                        v-model="d.awayHostId"
                        :options="hosts"
                        :aria-label="tt('While its machine is away')"
                      />
                    </div>
                  </div>
                  <input
                    v-if="d.awayHostId"
                    v-model="d.repositoryKey"
                    :class="field"
                    :placeholder="tt('host/owner/repository (default: the folder\'s remote)')"
                  >
                </div>
              </template>
            </div>
          </div>

          <p
            v-if="error"
            role="alert"
            class="m-0 shrink-0 px-5 pb-3 text-bad"
          >
            {{ error }}
          </p>
          <div class="flex shrink-0 justify-end gap-2 border-t border-line px-5 py-3">
            <HBtn
              size="md"
              @click="shown = false"
            >
              {{ tt('Cancel') }}
            </HBtn>
            <HBtn
              variant="primary"
              size="md"
              type="submit"
              :disabled="!canSave"
            >
              {{ d.id ? tt('Save') : tt('Create') }}
            </HBtn>
          </div>
        </form>
      </div>
    </Transition>
  </Teleport>
</template>
