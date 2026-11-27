// The host side of a plugin pane (docs/design/plugin-ui.md). The client built the document: the plugin's page with its own files
// inlined, a CSP first in its head, then the marker this replaces with the app's theme variables and the bridge of the message
// API. The frame is sandboxed with scripts only (an opaque origin, no access to the app, its cookies or storage), and every call
// it makes goes to the client, which checks the plugin declared the capability and this device approved it.

import { BASE, scheme, themeVars } from '~/lib/visual'

export const HOST_MARKER = '<!--harness-host-->'

// Version of the message API: the bridge and the client speak the same.
export const PLUGIN_UI_VERSION = 1

// window.harness inside the frame: call(method, params) → Promise, on(event, fn) → unsubscribe, version. Messages to the host
// carry the nonce; the script that knows it removes itself, and keeps what it needs from the window before the page's own
// code runs. `active` is the frame's user activation as the frame tells it: a speed bump, not a proof.
function bridge(nonce: string): string {
  return `(function(){var n=${JSON.stringify(nonce)},s=document.currentScript,P=parent,W=window,seq=0,pending={},handlers={},
vars=s&&s.previousElementSibling,A=navigator.userActivation;
function post(m){m.harnessPlugin=n;m.v=${PLUGIN_UI_VERSION};P.postMessage(m,'*')}
W.addEventListener('message',function(e){if(e.source!==P)return;var d=e.data;if(!d||d.harnessHost!==${PLUGIN_UI_VERSION})return;
if(d.id&&pending[d.id]){var p=pending[d.id];delete pending[d.id];if(d.error){var x=new Error(String(d.error.message||d.error.code));x.code=d.error.code;p[1](x)}else p[0](d.result);return}
if(d.event==='theme-vars'&&vars&&typeof d.css==='string'){vars.textContent=d.css;return}
if(d.event){(handlers[d.event]||[]).slice().forEach(function(f){try{f(d.data)}catch(err){setTimeout(function(){throw err})}})}});
W.harness=Object.freeze({version:${PLUGIN_UI_VERSION},
call:function(method,params){var id=++seq;return new Promise(function(res,rej){pending[id]=[res,rej];post({id:id,method:String(method),params:params===undefined?{}:params,active:!!(A&&A.isActive)})})},
on:function(ev,f){ev=String(ev);(handlers[ev]=handlers[ev]||[]).push(f);if(ev==='session')post({subscribe:'session'});return function(){handlers[ev]=(handlers[ev]||[]).filter(function(g){return g!==f})}}});
if(s)s.remove()})()`
}

// The style element with the theme: kept current from outside, so the pane follows the theme without reloading.
export function themeStyle(): string {
  return `:root{${themeVars()};color-scheme:${scheme()}}${BASE}`
}

export function pluginPaneDocument(html: string, nonce: string): string {
  const i = html.indexOf(HOST_MARKER)
  if (i < 0) throw new Error('the pane document has no place for the host')
  const host = `<style>${themeStyle()}</style><script>${bridge(nonce)}</script>`
  return html.slice(0, i) + host + html.slice(i + HOST_MARKER.length)
}

// A message from the frame.
export interface PaneRequest {
  harnessPlugin: string
  v: number
  id?: number
  method?: string
  params?: unknown
  active?: boolean
  subscribe?: string
}

// Prefix of a message a plugin sends: "[from plugin cost-meter] ..." (server/pluginui.go).
export const FROM_PLUGIN = /^\[from plugin ([A-Za-z0-9._-]+)\] /
