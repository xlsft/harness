// Markdown to what a voice should say: code blocks are named rather than read, links keep their text, and
// emphasis, headings, list markers and table pipes go.
export function speakable(md: string): string {
  return md
    .replace(/```[^\n]*\n[\s\S]*?(```|$)/g, ' (code block) ')
    .replace(/`([^`]+)`/g, '$1')
    .replace(/!\[[^\]]*\]\([^)]*\)/g, '')
    .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
    .replace(/^\s{0,3}#{1,6}\s+/gm, '')
    .replace(/^\s*>\s?/gm, '')
    .replace(/^\s*([-*+]|\d+\.)\s+/gm, '')
    .replace(/^\s*\|?[\s:-]+\|[\s|:-]*$/gm, '')
    .replace(/\|/g, ', ')
    .replace(/(\*\*|__|\*|_|~~)(?=\S)([\s\S]*?\S)\1/g, '$2')
    .replace(/<[^>]+>/g, '')
    .replace(/[ \t]+/g, ' ')
    .replace(/\n{2,}/g, '\n')
    .trim()
}

// A BCP 47 tag for the browser voice from the script the text is mostly in.
export function guessLang(text: string): string | undefined {
  const cyr = (text.match(/[Ѐ-ӿ]/g) ?? []).length
  const lat = (text.match(/[A-Za-z]/g) ?? []).length
  if (cyr > lat) return 'ru-RU'
  return undefined
}
