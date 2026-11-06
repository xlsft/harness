<script setup lang="ts">
// A file in the composer: a square with the image filling it, or a type icon and the extension. The name is in the
// tooltip; a click opens an image, the X in the corner takes the file off. Outlined, not bordered, so the theme's
// radius for bordered blocks stays off it and the outline follows the image's corners.
import { fileExtension } from '~/lib/session/attachments'
import type { ComposerFile } from '~/utils/composer'
import type { IconName } from '~/utils/icons'

// `covered`: the preview is over it, and the tooltip of the click that opened it must not stay on top.
const props = defineProps<{ file: ComposerFile, url?: string, covered?: boolean }>()
defineEmits<{ open: [], remove: [] }>()

// An image the browser cannot decode falls back to the icon, and opens nothing.
const broken = ref(false)
watch(() => props.url, () => {
  broken.value = false
})
const shown = computed(() => !!props.file.image && !!props.url && !broken.value)
const extension = computed(() => fileExtension(props.file.name).slice(0, 5).toUpperCase() || 'FILE')
const icon = computed<IconName>(() => {
  const ext = fileExtension(props.file.name)
  if (props.file.image) return 'image'
  if (/^(zip|tar|gz|tgz|rar|7z)$/.test(ext)) return 'archive'
  if (/^(json|ya?ml|toml|xml|html?|css|[cm]?[jt]sx?|vue|go|rs|py|rb|sh|java|kt|swift|c|h|cpp)$/.test(ext)) return 'code'
  if (/^(md|mdx)$/.test(ext)) return 'md'
  return 'file'
})
const tooltip = computed(() => props.file.error ? `${props.file.name}: ${props.file.error}` : props.file.name)
const uploading = computed(() => props.file.progress !== undefined && props.file.progress < 100)
const frame = computed(() => [
  'size-14 overflow-hidden rounded-chip bg-raised outline-1 -outline-offset-1',
  props.file.error ? 'outline-bad' : 'outline-line-strong'
])
</script>

<template>
  <div class="relative size-14 shrink-0">
    <UTooltip
      :text="tooltip"
      :disabled="covered"
      :delay-duration="300"
      :content="{ side: 'top', sideOffset: 4 }"
    >
      <button
        v-if="shown"
        type="button"
        :aria-label="tt('Open {0}', [file.name])"
        class="block"
        :class="frame"
        @click="$emit('open')"
      >
        <img
          :src="url"
          :alt="file.name"
          class="size-full object-cover"
          @error="broken = true"
        >
      </button>
      <div
        v-else
        tabindex="0"
        role="img"
        :aria-label="tooltip"
        class="flex flex-col items-center justify-center gap-0.5"
        :class="[frame, file.error ? 'text-bad' : 'text-muted']"
      >
        <HIcon
          :name="icon"
          :size="18"
        />
        <span class="max-w-full truncate px-0.5 text-[10px] leading-none">{{ extension }}</span>
      </div>
    </UTooltip>
    <span
      v-if="uploading"
      class="absolute inset-x-0.5 bottom-0.5 flex h-0.5 bg-line"
    ><span
      class="bg-accent"
      :style="{ width: `${file.progress}%` }"
    /></span>
    <button
      type="button"
      :aria-label="tt('Remove {0}', [file.name])"
      class="absolute top-0.5 right-0.5 flex size-4 items-center justify-center rounded-chip bg-black/70 text-white hover:bg-black"
      @click="$emit('remove')"
    >
      <HIcon
        name="close"
        :size="10"
      />
    </button>
  </div>
</template>
