import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { contentSecurityPolicy, renderPage } from '../../src/panel/html.ts'

const HTML = `<!DOCTYPE html><html><head><script type="importmap">{"imports":{"#entry":"/_nuxt/entry.js"}}</script><script>theme()</script><link rel="stylesheet" href="/_nuxt/a.css"><script type="module" src="/_nuxt/entry.js" crossorigin></script></head><body><script>window.__NUXT__={};window.__NUXT__.config={app:{baseURL:"/",buildAssetsDir:"/_nuxt/",cdnURL:""}}</script></body></html>`

const page = (over: Record<string, string> = {}) => renderPage({
  html: HTML, assets: 'https://file.vscode-cdn.net/ui', cspSource: 'https://*.vscode-cdn.net', nonce: 'NONCE',
  url: 'http://127.0.0.1:7801', token: 'tok', folder: '/repo', kind: 'dark', ...over
})

describe('the panel page', () => {
  it('loads every asset from the webview', () => {
    const html = page()
    assert.ok(html.includes('"https://file.vscode-cdn.net/ui/_nuxt/entry.js"'))
    assert.ok(html.includes('href="https://file.vscode-cdn.net/ui/_nuxt/a.css"'))
    assert.ok(html.includes('cdnURL:"https://file.vscode-cdn.net/ui/"'))
    assert.ok(!/"\/_nuxt\/entry/.test(html))
  })
  it('gives every script the nonce', () => {
    const html = page()
    const scripts = html.match(/<script\b[^>]*>/g) ?? []
    assert.ok(scripts.length >= 5)
    for (const tag of scripts) assert.ok(tag.includes('nonce="NONCE"'), tag)
  })
  it('hands over the endpoint before anything else runs', () => {
    const html = page()
    assert.ok(html.indexOf('__HARNESS_IDE__') < html.indexOf('importmap'))
    assert.ok(html.includes('"url":"http://127.0.0.1:7801"') && html.includes('"token":"tok"') && html.includes('"folder":"/repo"'))
  })
  it('reports what the policy blocks', () => {
    assert.ok(page().includes('securitypolicyviolation'))
  })
  it('cannot be broken out of by a folder name', () => {
    const html = page({ folder: '/repo</script><script>alert(1)</script>' })
    assert.ok(!html.includes('</script><script>alert(1)'))
    assert.ok(html.includes('\\u003c/script>'))
  })
  it('allows the one client and nothing else on the network', () => {
    const csp = contentSecurityPolicy('https://x.cdn', 'N', 'http://127.0.0.1:7801')
    assert.ok(csp.includes(`default-src 'none'`))
    assert.ok(csp.includes('connect-src http://127.0.0.1:7801 ws://127.0.0.1:7801 https://x.cdn'))
    assert.ok(csp.includes(`script-src 'nonce-N' https://x.cdn`))
    assert.ok(!csp.includes('unsafe-eval'))
    assert.ok(!csp.includes('*'))
  })
})
