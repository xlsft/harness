<script setup lang="ts">
// A runner's image, runtime, limits and timeouts (docs/design/cloud-sessions.md). Empty fields keep the server's defaults,
// shown as placeholders; the server checks every value.
import type { RunnerConfigForm } from '~/lib/hosts/types'

const form = defineModel<RunnerConfigForm>({ required: true })
const FIELD = 'h-8 min-w-0 border border-line bg-transparent px-2 text-[12.5px] text-strong outline-none focus:border-accent'
</script>

<template>
  <div class="flex flex-col gap-2">
    <div class="grid grid-cols-[repeat(auto-fill,minmax(150px,1fr))] gap-2">
      <label class="col-span-2 flex flex-col gap-1 text-muted">
        {{ tt('Image') }}
        <input
          v-model="form.image"
          :class="FIELD"
          placeholder="harness-agent"
        >
      </label>
      <label class="flex flex-col gap-1 text-muted">
        {{ tt('Runtime') }}
        <select
          v-model="form.runtime"
          :class="FIELD"
        >
          <option value="">{{ tt('The engine\'s default (runc)') }}</option>
          <option value="runsc">{{ tt('gVisor (runsc), recommended') }}</option>
        </select>
      </label>
      <label class="flex flex-col gap-1 text-muted">
        {{ tt('CPUs') }}
        <input
          v-model="form.cpus"
          type="number"
          min="0.1"
          step="0.5"
          :class="FIELD"
          placeholder="2"
        >
      </label>
      <label class="flex flex-col gap-1 text-muted">
        {{ tt('Memory, GiB') }}
        <input
          v-model="form.memoryGiB"
          type="number"
          min="0.125"
          :class="FIELD"
          placeholder="4"
        >
      </label>
      <label class="flex flex-col gap-1 text-muted">
        {{ tt('Disk, GiB') }}
        <input
          v-model="form.diskGiB"
          type="number"
          min="1"
          :class="FIELD"
          placeholder="20"
        >
      </label>
      <label class="flex flex-col gap-1 text-muted">
        {{ tt('Processes') }}
        <input
          v-model="form.pids"
          type="number"
          min="64"
          :class="FIELD"
          placeholder="1024"
        >
      </label>
      <label class="flex flex-col gap-1 text-muted">
        {{ tt('Removed when idle for, minutes') }}
        <input
          v-model="form.idleMinutes"
          type="number"
          min="1"
          :class="FIELD"
          placeholder="60"
        >
      </label>
      <label class="flex flex-col gap-1 text-muted">
        {{ tt('Longest life of a container, hours') }}
        <input
          v-model="form.lifetimeHours"
          type="number"
          min="0.2"
          :class="FIELD"
          placeholder="24"
        >
      </label>
      <label class="flex flex-col gap-1 text-muted">
        {{ tt('Caches, GiB each') }}
        <input
          v-model="form.cacheGiB"
          type="number"
          min="0.125"
          :class="FIELD"
          placeholder="10"
        >
      </label>
      <label class="flex flex-col gap-1 text-muted">
        {{ tt('Caches shared') }}
        <select
          v-model="form.cacheScope"
          :class="FIELD"
        >
          <option value="user">{{ tt('By one user\'s sessions') }}</option>
          <option value="runner">{{ tt('By everyone on this runner') }}</option>
        </select>
      </label>
    </div>
    <label class="flex flex-col gap-1 text-muted">
      {{ tt('Other images a session may ask for, one per line') }}
      <textarea
        v-model="form.allowedImages"
        rows="2"
        class="border border-line bg-transparent p-2 text-[12.5px] text-strong outline-none focus:border-accent"
      />
    </label>
    <span class="flex items-center gap-1.5 text-muted">
      <HToggle
        v-model="form.storageOptSize"
        :label="tt('The engine enforces the disk size (overlay2 on xfs with pquota, btrfs, zfs)')"
      />
      {{ tt('The engine enforces the disk size (overlay2 on xfs with pquota, btrfs, zfs)') }}
    </span>
    <p
      v-if="form.cacheScope === 'runner'"
      class="m-0 text-warn"
    >
      {{ tt('Shared caches let one user\'s agent put code in another\'s: share them only between people who trust each other.') }}
    </p>
  </div>
</template>
