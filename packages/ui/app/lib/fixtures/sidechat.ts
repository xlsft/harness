// Demo data for the Side chat pane (docs/design/ui/SideChat.dc.html). Side chats are not part of
// the agent contract yet: the pane keeps threads locally and streams canned answers.

export interface SideMessage {
  id: string
  role: 'user' | 'assistant'
  // Inline `code` is written with backticks.
  text: string
  // User: the main-chat selection the question was asked about.
  quote?: string
  // Assistant: a numbered list between `text` and `outro`.
  points?: string[]
  outro?: string
  // Assistant: "model · tokens" line with the actions; shown once the answer is complete.
  meta?: string
  streaming?: boolean
}

export interface SideChat {
  id: string
  title: string
  messages: SideMessage[]
}

export const SIDE_MODEL = { name: 'glm-4.6', provider: 'openrouter' }

export const SIDE_QUOTE = '…I\'m taking ConnectRPC because it stays gRPC-compatible and works over HTTP/1.1 and from the browser.'

export const SIDE_EMPTY = {
  title: 'No messages yet',
  body: 'A side chat sees this session but never changes it: no files, no tools, nothing added to the main thread.'
}

export function connectThread(): SideMessage[] {
  return [
    { id: 'q1', role: 'user', quote: SIDE_QUOTE, text: 'Why ConnectRPC and not plain gRPC here?' },
    {
      id: 'a1',
      role: 'assistant',
      text: 'Three reasons specific to harness:',
      points: [
        'Nitro serves fetch handlers over HTTP/1.1, and plain gRPC needs HTTP/2 end to end.',
        'The web and mobile UI can call Connect directly, no gRPC-Web proxy.',
        'It\'s still wire-compatible, so the Go client can speak gRPC if it ever needs to.'
      ],
      outro: 'The cost is bidi streaming: browsers can\'t do it, so the relay sticks to server streams.',
      meta: 'glm-4.6 · 1.8k tok'
    },
    { id: 'q2', role: 'user', text: 'And it runs inside Nitro without an adapter?' },
    { id: 'a2', role: 'assistant', text: 'Yes, through the fetch adapter: `createConnectRouter` plus a catch-all route.' }
  ]
}

export function sideChats(): SideChat[] {
  return [
    { id: 'connect', title: 'ConnectRPC vs gRPC', messages: connectThread() },
    { id: 'blank', title: 'Side chat', messages: [] }
  ]
}

// The streaming state of the design continues the last answer with this text.
export const STREAM_CONTINUATION = ' The only catch is body size limits on large uploads: Nitro caps request bodies, so raise the limit for the upload route.'

// Canned answers for questions typed in the demo, used in turn.
export const CANNED_ANSWERS = [
  'Short version: the side chat reads the session transcript and the open files, but it has no tools, so nothing it says changes the main thread until you press `Send to main chat`.',
  'It depends on where the cost sits. For harness the server streams are cheap, so keeping one `Gateway/Stream` call per turn is simpler than batching.',
  'Yes. Ask it in the main chat if you want the agent to act on it; this thread stays read-only.'
]
