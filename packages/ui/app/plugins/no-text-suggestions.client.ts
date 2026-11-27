// No native typing help in any field: the system's autocorrect bubbles, inline predictions, spelling underlines and
// auto-capitalisation get in the way of names, paths, commands and code, which is most of what is typed here. Set on
// every input, textarea and editable element as it appears, so no component has to remember it. A field that wants one
// of them back sets the attribute itself; only missing attributes are filled in.
const ATTRS: Record<string, string> = {
  autocorrect: 'off',
  autocapitalize: 'off',
  spellcheck: 'false',
  writingsuggestions: 'false'
}
const FIELDS = 'input, textarea, [contenteditable]:not([contenteditable="false"])'

function quiet(el: Element) {
  for (const [k, v] of Object.entries(ATTRS)) {
    if (!el.hasAttribute(k)) el.setAttribute(k, v)
  }
}

function sweep(root: ParentNode) {
  if (root instanceof Element && root.matches(FIELDS)) quiet(root)
  root.querySelectorAll(FIELDS).forEach(quiet)
}

export default defineNuxtPlugin(() => {
  sweep(document)
  new MutationObserver((records) => {
    for (const r of records) {
      if (r.type === 'attributes') {
        if (r.target instanceof Element && r.target.matches(FIELDS)) quiet(r.target)
        continue
      }
      r.addedNodes.forEach((n) => {
        if (n instanceof Element) sweep(n)
      })
    }
  }).observe(document.documentElement, { subtree: true, childList: true, attributes: true, attributeFilter: ['contenteditable'] })
})
