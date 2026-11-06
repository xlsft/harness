<script setup lang="ts">
// Links (Link.dc.html): OG card with image, compact rows without an image, inline link with hover preview.
interface LinkPreview {
  url: string
  site: string
  title: string
  description?: string
  favicon?: string
}

const props = withDefaults(defineProps<{
  variant?: 'og' | 'compact' | 'inline'
  links: LinkPreview[]
}>(), { variant: 'compact' })

const hover = ref(false)
const first = computed(() => props.links[0]!)
</script>

<template>
  <a
    v-if="variant === 'og'"
    :href="first.url"
    class="flex w-[480px] max-w-full flex-col border border-default bg-surface text-toned"
  >
    <div
      role="img"
      :aria-label="tt('OG image of {0}', [first.title])"
      class="flex aspect-[1.91] flex-col gap-2.5 border-b border-default bg-default px-7 py-[26px]"
    >
      <slot name="image">
        <span class="text-[12px] text-muted">{{ tt('xlsft /') }}</span>
        <span class="font-display text-[30px] leading-[1.1] font-bold text-strong">{{ tt('proxy.xlsft.ru') }}</span>
        <span class="text-[12px] leading-normal text-muted">{{ tt('Nuxt 4 · Nuxt UI · Nitro · Drizzle') }}</span>
        <span class="flex-1" />
        <div class="flex h-1.5"><span class="w-[74%] bg-accent" /><span class="w-[20%] bg-ok" /><span class="w-[6%] bg-line-strong" /></div>
        <div class="flex gap-4 text-[11px] text-muted"><span>{{ tt('TypeScript 74%') }}</span><span>{{ tt('Vue 20%') }}</span><span>{{ tt('Other 6%') }}</span></div>
      </slot>
    </div>
    <div class="flex flex-col gap-[3px] px-3 py-2.5">
      <span class="flex items-center gap-[7px] text-[11px] text-muted">
        <span class="flex size-3.5 items-center justify-center bg-white text-[9px] font-bold text-black">{{ first.favicon ?? first.site[0]!.toUpperCase() }}</span>{{ first.site }}
      </span>
      <span class="text-[13px] text-strong">{{ first.title }}</span>
      <span
        v-if="first.description"
        class="text-[12px] text-muted"
      >{{ first.description }}</span>
    </div>
  </a>

  <div
    v-else-if="variant === 'compact'"
    class="flex w-[480px] max-w-full flex-col gap-1.5"
  >
    <a
      v-for="l in links"
      :key="l.url"
      :href="l.url"
      class="flex items-center gap-2.5 border border-default bg-surface px-2.5 py-2 text-toned hover:bg-hover"
    >
      <span class="flex size-7 shrink-0 items-center justify-center bg-raised text-[12px] text-strong">{{ l.favicon ?? l.title[0] }}</span>
      <span class="flex min-w-0 flex-1 flex-col"><span class="truncate text-[12.5px] text-strong">{{ l.title }}</span><span class="text-[11px] text-muted">{{ l.site }}</span></span>
      <HIcon
        name="external"
        :size="14"
        class="text-muted"
      />
    </a>
  </div>

  <span
    v-else
    class="relative inline-block"
  >
    <a
      :href="first.url"
      class="text-accent underline underline-offset-[3px]"
      :class="hover ? 'bg-accent/15' : ''"
      @mouseenter="hover = true"
      @mouseleave="hover = false"
      @focus="hover = true"
      @blur="hover = false"
    ><slot>{{ first.title }}</slot></a>
    <span
      v-if="hover"
      class="absolute top-full left-6 z-10 mt-1.5 flex w-80 max-w-[80vw] flex-col gap-[3px] border border-accented bg-hover px-2.5 py-2 text-[12px] leading-normal"
    >
      <span class="text-[11px] text-muted">{{ first.site }}</span>
      <span class="text-strong">{{ first.title }}</span>
      <span
        v-if="first.description"
        class="text-muted"
      >{{ first.description }}</span>
    </span>
  </span>
</template>
