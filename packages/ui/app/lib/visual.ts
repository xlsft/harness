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

// Variable in the visual → app token it reads, with the dark theme's value when the token is missing. The
// Visual tool description (core prompts/tools/Visual) lists the same names; keep them in step.
const VARS: [string, string, string][] = [
  ['--bg', '--color-ground', '#000000'],
  ['--surface', '--color-surface', '#0a0a0a'],
  ['--hover', '--color-hover', '#131313'],
  ['--raised', '--color-raised', '#1c1c1c'],
  ['--line', '--color-line', '#2b2b2b'],
  ['--line-strong', '--color-line-strong', '#454545'],
  ['--text', '--ui-text', '#d4d4d4'],
  ['--text-strong', '--ui-text-highlighted', '#ffffff'],
  ['--muted', '--ui-text-muted', '#8f8f8f'],
  ['--dimmed', '--ui-text-dimmed', '#737373'],
  ['--accent', '--color-accent', '#2b7fff'],
  ['--accent-hover', '--color-accent-hover', '#8ec5ff'],
  ['--on-accent', '--color-on-accent', '#ffffff'],
  ['--ok', '--color-ok', '#00bc7d'],
  ['--warn', '--color-warn', '#fe9a00'],
  ['--error', '--color-bad', '#ff2056'],
  ['--cyan', '--color-cyan', '#00b8db'],
  ['--violet', '--color-violet', '#8e51ff'],
  ['--pink', '--color-pink', '#f6339a'],
  ['--orange', '--color-orange', '#fe6e00'],
  ['--chart-1', '--color-chart-1', '#2b7fff'],
  ['--chart-2', '--color-chart-2', '#f54900'],
  ['--chart-3', '--color-chart-3', '#8e51ff'],
  ['--chart-4', '--color-chart-4', '#0092b8'],
  ['--chart-5', '--color-chart-5', '#f6339a'],
  ['--radius', '--radius-control', '0px'],
  ['--radius-box', '--radius-box', '0px'],
  ['--font', '--font-sans', 'ui-monospace, monospace'],
  ['--mono', '--font-mono', 'ui-monospace, monospace']
]

// Fixed parts of the app's look: its type scale and spacing, and a chart palette.
const FIXED = '--border:1px;--text-xs:11px;--text-sm:12px;--text-md:13px;--text-lg:15px;--text-xl:18px;'
  + '--leading:1.6;--space:4px;--control-h:28px;'

// Base styles in the app's look, at zero specificity (:where) so any rule of the visual wins.
const BASE = `:where(html,body){margin:0;background:transparent;color:var(--text);font:var(--text-md)/var(--leading) var(--font)}
:where(body){padding:calc(var(--space)*3)}
:where(*,*::before,*::after){box-sizing:border-box;border-radius:var(--radius)}
:where(h1,h2,h3,h4){margin:0 0 calc(var(--space)*2);color:var(--text-strong);font-weight:600;line-height:1.3}
:where(h1){font-size:var(--text-xl)}:where(h2){font-size:var(--text-lg)}:where(h3,h4){font-size:var(--text-md)}
:where(p){margin:0 0 calc(var(--space)*2)}
:where(a){color:var(--accent);text-decoration:none}:where(a:hover){color:var(--accent-hover)}
:where(code,kbd,pre){font-family:var(--mono);font-size:var(--text-sm)}
:where(code){border:var(--border) solid var(--line);background:var(--raised);padding:0 4px}
:where(pre){margin:0;border:var(--border) solid var(--line);background:var(--surface);padding:calc(var(--space)*2.5) calc(var(--space)*3);overflow:auto}
:where(hr){border:0;border-top:var(--border) solid var(--line);margin:calc(var(--space)*3) 0}
:where(button){min-height:var(--control-h);padding:0 calc(var(--space)*2.5);border:var(--border) solid var(--line);background:transparent;color:var(--text);font:inherit;font-size:var(--text-sm);cursor:pointer}
:where(button:hover){border-color:var(--line-strong);color:var(--text-strong)}
:where(button:disabled){opacity:.5;cursor:default}
:where(button.primary,button[type=submit]){background:var(--accent);border-color:var(--accent);color:var(--on-accent)}
:where(input:not([type=checkbox],[type=radio],[type=range]),select,textarea){min-height:var(--control-h);padding:0 calc(var(--space)*2.5);border:var(--border) solid var(--line-strong);background:var(--bg);color:var(--text-strong);font:inherit;font-size:var(--text-sm);outline:none}
:where(textarea){height:auto;padding:calc(var(--space)*1.5) calc(var(--space)*2.5)}
:where(input,select,textarea):focus{border-color:var(--accent)}
:where(input[type=checkbox],input[type=radio],input[type=range]){accent-color:var(--accent)}
:where(table){border-collapse:collapse;width:100%;font-size:var(--text-sm)}
:where(th,td){border-bottom:var(--border) solid var(--line);padding:calc(var(--space)*1.5) calc(var(--space)*2.5);text-align:left}
:where(th){color:var(--muted);font-weight:400}
::selection{background:color-mix(in srgb,var(--accent) 35%,transparent)}
::-webkit-scrollbar{width:8px;height:8px}::-webkit-scrollbar-thumb{background:var(--line-strong)}::-webkit-scrollbar-track{background:transparent}`

const scheme = () => import.meta.client && document.documentElement.classList.contains('light') ? 'light' : 'dark'

function themeVars(): string {
  const style = import.meta.client ? getComputedStyle(document.documentElement) : null
  return VARS.map(([name, token, fallback]) => `${name}:${style?.getPropertyValue(token).trim() || fallback}`).join(';') + ';' + FIXED
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
    + `<style>:root{${themeVars()};color-scheme:${scheme()}}${BASE}</style>`
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
