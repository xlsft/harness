// The colour of a person's letter avatar: a hue from a hash of who they are (username, else email or id), with fixed
// saturation and lightness so white text reads on every hue. The same person gets the same colour everywhere.

// FNV-1a: small, stable, and spreads similar names apart.
function hash(s: string): number {
  let h = 0x811C9DC5
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  return h >>> 0
}

export function avatarColor(seed: string): string {
  return `hsl(${hash(seed.trim().toLowerCase()) % 360} 52% 42%)`
}
