// The colour of a person's letter avatar: one of twelve hues 30° apart, picked by a hash of who they are (username, else
// email or id), with fixed saturation and lightness so white text reads on each. The same person gets the same colour
// everywhere, and the hues are far enough apart to tell people apart at a glance.

// FNV-1a with MurmurHash3's finaliser: plain FNV-1a barely moves the low bits of short strings, so "xlsft" and
// "eldanto" landed on nearly the same hue.
function hash(s: string): number {
  let h = 0x811C9DC5
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  h ^= h >>> 16
  h = Math.imul(h, 0x85EBCA6B)
  h ^= h >>> 13
  h = Math.imul(h, 0xC2B2AE35)
  h ^= h >>> 16
  return h >>> 0
}

const HUES = 12

export function avatarColor(seed: string): string {
  return `hsl(${(hash(seed.trim().toLowerCase()) % HUES) * (360 / HUES)} 52% 42%)`
}
