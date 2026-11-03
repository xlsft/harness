<script setup lang="ts">
// External link of the settings screens: the system browser on the desktop app, a new tab on the web.
import { isDesktop, openExternal } from '~/lib/desktop'

const props = defineProps<{ href: string }>()

function open(e: MouseEvent) {
  if (!isDesktop()) return
  e.preventDefault()
  void openExternal(props.href)
}
</script>

<template>
  <a
    :href="href"
    target="_blank"
    rel="noopener noreferrer"
    class="link inline-flex items-center gap-0.5 text-[11.5px]"
    @click="open"
  ><slot /><HIcon
    name="arrow-up-right"
    :size="11"
  /></a>
</template>
