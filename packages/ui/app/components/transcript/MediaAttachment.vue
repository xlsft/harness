<script setup lang="ts">
// Attachments (Media.dc.html): file chips, a single image, a gallery, a video player and a voice message.
import type { Attachment } from '~/lib/session/types'
import type { IconName } from '~/utils/icons'

const props = withDefaults(defineProps<{
  variant?: 'file' | 'image' | 'gallery' | 'video' | 'audio'
  files?: Attachment[]
  side?: 'left' | 'right'
  caption?: string
  meta?: string
  transcript?: string
}>(), { variant: 'file', files: () => [], side: 'left', caption: '', meta: '', transcript: '' })

function fileLook(f: Attachment): { icon: IconName, cls: string, type: string } {
  if (/zip|tar|gz/.test(f.mediaType) || /\.(zip|tgz|gz)$/.test(f.name)) return { icon: 'archive', cls: 'text-warn', type: 'Archive' }
  if (/json/.test(f.mediaType) || f.name.endsWith('.json')) return { icon: 'code', cls: 'text-cyan', type: 'JSON' }
  if (f.kind === 'image') return { icon: 'image', cls: 'text-strong', type: 'Image' }
  if (f.name.endsWith('.md')) return { icon: 'file', cls: 'text-strong', type: 'Markdown' }
  return { icon: 'file', cls: 'text-strong', type: f.mediaType.split('/').pop()?.toUpperCase() ?? 'File' }
}

function size(n: number) {
  if (n >= 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1).replace(/\.0$/, '')} MB`
  return `${Math.max(1, Math.round(n / 1024))} KB`
}

const playing = ref(false)
const wave = [6, 10, 16, 22, 14, 8, 12, 20, 26, 18, 10, 6, 9, 15, 24, 28, 20, 12, 8, 14, 22, 16, 10, 7, 12, 18, 24, 20, 14, 9, 6, 11, 17, 23, 19, 13, 8, 6, 10, 15, 12, 8, 6, 5]
const align = computed(() => props.side === 'right' ? 'justify-end' : 'justify-start')
</script>

<template>
  <div
    v-if="variant === 'file'"
    class="flex flex-wrap gap-2"
    :class="align"
  >
    <a
      v-for="f in files"
      :key="f.name"
      href="#"
      class="flex min-w-[200px] items-center gap-2.5 border border-default bg-surface px-2.5 py-[7px] text-toned"
    >
      <span class="flex size-[30px] items-center justify-center bg-raised"><HIcon
        :name="fileLook(f).icon"
        :size="16"
        :class="fileLook(f).cls"
      /></span>
      <span class="flex flex-col"><span class="text-[12.5px] text-strong">{{ f.name }}</span><span class="text-[11px] text-muted">{{ fileLook(f).type }} · {{ size(f.size) }}</span></span>
    </a>
  </div>

  <figure
    v-else-if="variant === 'image'"
    class="m-0 flex w-[440px] max-w-full flex-col gap-[5px]"
  >
    <div
      role="img"
      :aria-label="caption"
      class="flex aspect-video flex-col items-center justify-center gap-2 border border-default bg-surface bg-[repeating-linear-gradient(135deg,var(--color-hover)_0_1px,transparent_1px_14px)] text-[11px] text-muted"
    >
      <HIcon
        name="image"
        :size="26"
        class="text-line-strong"
      />
      {{ tt('[screenshot 2880×1620]') }}
    </div>
    <figcaption class="flex gap-2 text-[11px] text-muted">
      {{ caption }}<span class="flex-1" />{{ meta }}
    </figcaption>
  </figure>

  <div
    v-else-if="variant === 'gallery'"
    class="grid w-[440px] max-w-full grid-cols-4 gap-1"
  >
    <div
      v-for="(f, i) in files.slice(0, 3)"
      :key="i"
      role="img"
      :aria-label="tt('Screenshot {0}', [i + 1])"
      class="flex aspect-square items-end border border-default bg-surface bg-[repeating-linear-gradient(135deg,var(--color-hover)_0_1px,transparent_1px_10px)] p-[5px] text-[10px] text-muted"
    >
      {{ f.name }}
    </div>
    <a
      v-if="files.length > 3"
      href="#"
      class="flex aspect-square items-center justify-center border border-default bg-hover text-[16px] text-strong"
    >+{{ files.length - 3 }}</a>
  </div>

  <figure
    v-else-if="variant === 'video'"
    class="m-0 flex w-[440px] max-w-full flex-col gap-[5px]"
  >
    <div class="relative flex aspect-video items-center justify-center border border-default bg-surface">
      <span class="absolute top-2 left-2.5 text-[11px] text-muted">{{ tt('[screen recording]') }}</span>
      <button
        type="button"
        :aria-label="tt('Play')"
        class="flex size-[46px] items-center justify-center bg-accent text-on-accent"
        @click="playing = !playing"
      >
        <HIcon
          name="play"
          :size="20"
        />
      </button>
      <div class="absolute inset-x-0 bottom-0 flex items-center gap-2.5 px-2.5 py-[7px] text-[11px] text-toned">
        0:12<span class="flex h-[3px] flex-1 bg-line"><span class="w-[11%] bg-accent" /></span>1:48
        <HIcon
          name="expand"
          :size="14"
        />
      </div>
    </div>
    <figcaption class="flex gap-2 text-[11px] text-muted">
      {{ caption }}<span class="flex-1" />{{ meta }}
    </figcaption>
  </figure>

  <div
    v-else
    class="w-[440px] max-w-full border border-default bg-surface"
  >
    <div class="flex items-center gap-2.5 px-2.5 py-2">
      <button
        type="button"
        :aria-label="tt('Play voice message')"
        class="flex size-8 shrink-0 items-center justify-center bg-accent text-on-accent"
      >
        <HIcon name="play" />
      </button>
      <div class="flex h-7 min-w-0 flex-1 items-center gap-0.5 overflow-hidden">
        <span
          v-for="(h, i) in wave"
          :key="i"
          class="w-[3px] shrink-0"
          :class="i < 14 ? 'bg-accent' : 'bg-line-strong'"
          :style="{ height: `${h}px` }"
        />
      </div>
      <span class="text-[11px] text-muted">{{ meta }}</span>
    </div>
    <div class="border-t border-default px-2.5 py-[7px] text-[12px] leading-[1.55] text-muted">
      <span class="text-toned">{{ tt('Transcript:') }}</span> “{{ transcript }}”
    </div>
  </div>
</template>
