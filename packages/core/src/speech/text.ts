/** Text and audio helpers of the speech service. */

/**
 * Splits text into pieces of at most `max` characters, preferring paragraph, then sentence, then word boundaries,
 * so each piece is one TTS request. Whitespace-only pieces are dropped.
 */
export function splitForSpeech(text: string, max: number): string[] {
  const out: string[] = [];
  let rest = text.trim();
  while (rest.length > max) {
    const window = rest.slice(0, max + 1);
    let cut = lastBoundary(window, /\n\s*\n/g, max);
    if (cut < max / 3) cut = Math.max(cut, lastBoundary(window, /[.!?…。！？](?=\s|$)["')\]]*/g, max));
    if (cut < max / 3) cut = Math.max(cut, lastBoundary(window, /[,;:](?=\s)/g, max));
    if (cut < max / 3) cut = Math.max(cut, window.lastIndexOf(" ", max));
    if (cut <= 0) cut = max;
    const piece = rest.slice(0, cut).trim();
    if (piece) out.push(piece);
    rest = rest.slice(cut).trim();
  }
  if (rest) out.push(rest);
  return out;
}

/** End index of the last match of re that ends at or before max (0 when none). */
function lastBoundary(s: string, re: RegExp, max: number): number {
  let best = 0;
  for (const m of s.matchAll(re)) {
    const end = m.index + m[0].length;
    if (end <= max) best = end;
  }
  return best;
}

/** A WAV header for 16-bit little-endian PCM. */
export function wavHeader(dataBytes: number, sampleRate: number, channels = 1): Uint8Array {
  const b = Buffer.alloc(44);
  b.write("RIFF", 0, "ascii");
  b.writeUInt32LE(36 + dataBytes, 4);
  b.write("WAVE", 8, "ascii");
  b.write("fmt ", 12, "ascii");
  b.writeUInt32LE(16, 16);
  b.writeUInt16LE(1, 20);
  b.writeUInt16LE(channels, 22);
  b.writeUInt32LE(sampleRate, 24);
  b.writeUInt32LE(sampleRate * channels * 2, 28);
  b.writeUInt16LE(channels * 2, 32);
  b.writeUInt16LE(16, 34);
  b.write("data", 36, "ascii");
  b.writeUInt32LE(dataBytes, 40);
  return new Uint8Array(b);
}

/** The base type of a MIME type, lower case ("audio/webm;codecs=opus" -> "audio/webm"). */
export function baseMime(mime: string): string {
  return mime.split(";")[0]!.trim().toLowerCase();
}

/** A file name with an extension the upload APIs recognise. */
export function fileNameFor(mime: string): string {
  const ext: Record<string, string> = {
    "audio/webm": "webm",
    "audio/ogg": "ogg",
    "audio/wav": "wav",
    "audio/x-wav": "wav",
    "audio/wave": "wav",
    "audio/mpeg": "mp3",
    "audio/mp3": "mp3",
    "audio/mp4": "m4a",
    "audio/m4a": "m4a",
    "audio/x-m4a": "m4a",
    "audio/flac": "flac",
    "audio/aac": "aac",
  };
  return `audio.${ext[baseMime(mime)] ?? "bin"}`;
}

/** OpenRouter's input_audio.format for a MIME type. */
export function audioFormatOf(mime: string): string {
  const b = baseMime(mime);
  if (b.includes("wav") || b === "audio/wave") return "wav";
  if (b === "audio/mpeg" || b === "audio/mp3") return "mp3";
  return b.replace(/^audio\//, "");
}
