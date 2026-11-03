// Text picked in the transcript, sent on: "Reply" quotes it in the composer, "Start a side chat" opens a new side
// chat with it as the quote. The composer and the side chat pane pick the request up and clear it.
export function useQuoteRequests() {
  const reply = useState<string>('quote-reply', () => '')
  const sideChat = useState<string>('quote-side-chat', () => '')
  const { openPane } = usePanes()
  return {
    reply,
    sideChat,
    replyWith: (text: string) => (reply.value = text),
    startSideChat: (text: string) => {
      sideChat.value = text
      openPane('sidechat')
    }
  }
}

// One line for a quote: whitespace collapsed, long picks cut with an ellipsis.
export function quoteLine(text: string, max = 280) {
  const one = text.replace(/\s+/g, ' ').trim()
  return one.length > max ? `${one.slice(0, max - 1)}…` : one
}
