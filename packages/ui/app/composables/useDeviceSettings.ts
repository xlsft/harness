// Keeps the appearance of this browser and the client's device settings in step: the client holds the theme mode (the
// model can change it with SettingsSet), the picker tells it about the user's choice. Called once, from app.vue.
import type { AppearanceMode } from '~/themes'

export function useDeviceSettings() {
  const store = useSessionStore()
  const { appearance, setMode } = useAppearance()

  // A client with no choice yet learns this browser's.
  watch(() => store.deviceSettingsLoaded, (loaded) => {
    if (loaded && !store.deviceSettings.theme) store.quietly(store.updateDeviceSettings({ theme: appearance.value.mode }))
  }, { immediate: true })
  watch(() => store.deviceSettings.theme, (theme) => {
    if (theme && theme !== appearance.value.mode) setMode(theme as AppearanceMode)
  }, { immediate: true })
  watch(() => appearance.value.mode, (mode) => {
    if (store.deviceSettingsLoaded && mode !== store.deviceSettings.theme) store.quietly(store.updateDeviceSettings({ theme: mode }))
  })
}
