// A shared clock for elapsed timers ("running · 41s"), ticking once a second.
const now = ref(Date.now())
let timer: ReturnType<typeof setInterval> | null = null

export function useNow() {
  if (!timer && import.meta.client) {
    timer = setInterval(() => {
      now.value = Date.now()
    }, 1000)
  }
  return now
}
