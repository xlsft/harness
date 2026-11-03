<script setup lang="ts">
// Environment picker of the new-session screen. Today a session runs on the connected client ("This
// computer"); remote-control hosts (RelayService) and SSH hosts are listed here once the UI talks to core.
const emit = defineEmits<{ close: [] }>()
const query = ref('')
const ROW = 'flex cursor-pointer items-center gap-2 border-l-2 px-2.5 py-[5px]'
</script>

<template>
  <div
    role="menu"
    aria-label="Environment"
    class="w-[280px] max-w-[calc(100vw-32px)] border border-accented bg-hover text-[12.5px]"
  >
    <label class="flex items-center gap-2 border-b border-default px-2.5 py-1.5 text-muted">
      <HIcon
        name="search"
        :size="13"
      />
      <input
        v-model="query"
        placeholder="Search environments"
        class="min-w-0 flex-1 bg-transparent text-strong outline-none placeholder:text-muted"
      >
    </label>
    <div class="px-2.5 pt-[5px] pb-0.5 text-[11px] text-muted">
      Local
    </div>
    <div
      role="menuitemradio"
      aria-checked="true"
      :class="[ROW, 'border-accent bg-raised text-strong']"
      @click="emit('close')"
    >
      <HIcon
        name="monitor"
        :size="13"
      />
      <span class="flex-1">This computer</span>
      <HIcon
        name="check"
        :size="13"
        class="text-accent"
      />
    </div>
    <div class="my-1 h-px bg-line" />
    <div
      role="menuitem"
      aria-disabled="true"
      :class="[ROW, 'border-transparent text-muted']"
    >
      <HIcon
        name="relay"
        :size="13"
      />
      <span class="flex-1">Remote control</span>
      <span class="text-[11px]">soon</span>
    </div>
    <div
      role="menuitem"
      aria-disabled="true"
      :class="[ROW, 'border-transparent text-muted']"
    >
      <HIcon
        name="terminal"
        :size="13"
      />
      <span class="flex-1">SSH</span>
      <span class="text-[11px]">soon</span>
    </div>
  </div>
</template>
