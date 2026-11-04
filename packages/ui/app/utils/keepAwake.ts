import type { SessionMeta } from '~/lib/session/types'

// Who gets the "Keep computer awake" switch; the existing relay state says all the client knows about it, the rest is
// this frontend's own (the desktop app, whether it took over a remote session). A session of another machine needs to
// be driven through the relay: the switch then keeps that machine awake. A local session shows it in the desktop app,
// and elsewhere only when the session is on the relay.
export function keepAwakeAvailable(
  meta: Pick<SessionMeta, 'remote' | 'remoteControllable' | 'relayEnabled'>,
  viewer: { desktop: boolean, controlling: boolean }
): boolean {
  if (meta.remote) return meta.remoteControllable && viewer.controlling
  return viewer.desktop || meta.relayEnabled
}
