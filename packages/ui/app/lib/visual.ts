// Visual tool documents: the model's HTML is shown in an iframe sandboxed with scripts only (null origin, no
// access to the app). This adds a CSP (CDN scripts, styles and fonts; no fetch), the app's colours as CSS
// variables, harness.send() for talking back and height reports so the frame fits its content.

const CDNS = 'https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://unpkg.com'
const CSP = [
  'default-src \'none\'',
  `script-src 'unsafe-inline' 'unsafe-eval' ${CDNS}`,
  `style-src 'unsafe-inline' ${CDNS} https://fonts.googleapis.com`,
  `font-src data: ${CDNS} https://fonts.gstatic.com`,
  'img-src data: blob:',
  'media-src data: blob:',
  'connect-src \'none\'',
  'form-action \'none\''
].join('; ')

// Variable in the visual → app token it reads, with the dark theme's value when the token is missing.
const VARS: [string, string, string][] = [
  ['--bg', '--color-ground', '#000000'],
  ['--surface', '--color-surface', '#0a0a0a'],
  ['--raised', '--color-raised', '#1c1c1c'],
  ['--line', '--color-line', '#2b2b2b'],
  ['--muted', '--ui-text-muted', '#8f8f8f'],
  ['--text', '--ui-text', '#d4d4d4'],
  ['--accent', '--color-accent', '#2b7fff'],
  ['--ok', '--color-ok', '#00bc7d'],
  ['--warn', '--color-warn', '#fe9a00'],
  ['--error', '--color-bad', '#ff2056'],
  ['--font', '--font-sans', 'ui-monospace, monospace'],
  ['--mono', '--font-mono', 'ui-monospace, monospace']
]

function themeVars(): string {
  const style = import.meta.client ? getComputedStyle(document.documentElement) : null
  return VARS.map(([name, token, fallback]) => `${name}:${style?.getPropertyValue(token).trim() || fallback}`).join(';')
}

// Messages from the frame carry the nonce; the script that knows it removes itself, so the visual's own code
// cannot read it from the DOM. harness.send works only right after a click or a key press.
function bridge(nonce: string): string {
  return `(function(){var n=${JSON.stringify(nonce)},s=document.currentScript,last=0;
function post(m){m.harnessVisual=n;parent.postMessage(m,'*')}
function size(){if(document.body)post({height:Math.ceil(document.body.scrollHeight)})}
window.harness=Object.freeze({send:function(t){var u=navigator.userActivation;if(u&&!u.isActive)return false;var now=Date.now();if(now-last<1000)return false;last=now;post({send:String(t).slice(0,2000)});return true}});
document.addEventListener('DOMContentLoaded',function(){new ResizeObserver(size).observe(document.body);size()});
addEventListener('load',size);if(s)s.remove()})()`
}

export function visualDocument(html: string, nonce: string): string {
  const head = `<meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="${CSP}">`
    + `<style>:root{${themeVars()};color-scheme:dark}html,body{margin:0;background:transparent;color:var(--text);font:13px/1.6 var(--font)}</style>`
    + `<script>${bridge(nonce)}</script>`
  // Our head goes first so the visual's own styles win; a document without <head> gets one.
  const h = html.match(/<head(\s[^>]*)?>/i)
  if (h) return html.slice(0, h.index! + h[0].length) + head + html.slice(h.index! + h[0].length)
  const root = html.match(/<html(\s[^>]*)?>/i)
  if (root) return html.slice(0, root.index! + root[0].length) + `<head>${head}</head>` + html.slice(root.index! + root[0].length)
  const doctype = html.match(/^\s*<!doctype[^>]*>/i)
  return doctype
    ? doctype[0] + `<head>${head}</head>` + html.slice(doctype[0].length)
    : `<!doctype html><head>${head}</head>` + html
}

export interface VisualMessage { height?: number, send?: string }

// Prefix of a message sent from inside a visual: "[from visual cpu-load] Use 80%".
export const FROM_VISUAL = /^\[from visual ([a-z0-9-]+)\] /
