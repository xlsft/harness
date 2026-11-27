import dark from '../themes-dark.json'
import light from '../themes-light.json'

const themes = { dark, light } as const
type Kind = keyof typeof themes

// Inline head script: applies stored / system theme before paint (no flash).
const THEME_SCRIPT = `(function(){try{var d=${JSON.stringify({ dark: dark.tokens, light: light.tokens })};
var k=localStorage.getItem('harness-docs-theme');
if(k!=='dark'&&k!=='light')k=matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light';
var r=document.documentElement,t=d[k];
for(var n in t)r.style.setProperty('--color-'+n,t[n]);
r.classList.remove('light','dark');r.classList.add(k);r.dataset.theme=k;
}catch(e){}})()`

// Shared across the app (not per-component state).
const kind = ref<Kind>('dark')

export function useAppearance() {
  function apply(k: Kind) {
    kind.value = k
    const r = document.documentElement
    for (const [token, value] of Object.entries(themes[k].tokens)) r.style.setProperty(`--color-${token}`, value)
    r.classList.remove('light', 'dark')
    r.classList.add(k)
    r.dataset.theme = k
    try { localStorage.setItem('harness-docs-theme', k) } catch {
      // localStorage unavailable (private mode, etc).
    }
  }

  function toggle() { apply(kind.value === 'dark' ? 'light' : 'dark') }

  onMounted(() => {
    const stored = (() => { try { return localStorage.getItem('harness-docs-theme') } catch { return null } })()
    const k: Kind = stored === 'light' || stored === 'dark'
      ? stored
      : matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
    apply(k)
  })

  useHead({ script: [{ innerHTML: THEME_SCRIPT, tagPriority: 'critical' }] })
  return { kind, toggle, apply }
}
