// The always-on host dialogs (docs/design/hosts.md): the session whose move is being confirmed, and the best-of-N group
// being compared. Empty: closed. The session menu, /move and the composer open them; the dialogs live in app.vue.

export const useMoveSession = () => useState<string>('move-session', () => '')

// A group to compare, or 'new' for the dialog that starts one.
export const useAttemptsView = () => useState<string>('attempts-view', () => '')
