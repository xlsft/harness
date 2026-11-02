# Extracts the design's inline SVG icons into app/utils/icons.ts.
# Usage (from packages/ui): python3 scripts/design-reduce.py ../../docs/design/ui /tmp/red && python3 scripts/design-icons.py /tmp/red ../../docs/design/ui app/utils/icons.ts
import re, sys, os, json
red, src, out = sys.argv[1], sys.argv[2], sys.argv[3]
icons = json.load(open(os.path.join(red, '_icons.json')))

# Per-file renames for dict names that clash with a different path elsewhere.
RENAME = {
    ('HeaderMenu.dc.html', 'globe'): 'globe-stand',
    ('HeaderMenu.dc.html', 'copy'): 'copy',
    ('HeaderMenu.dc.html', 'chat'): 'chat-outline',
    ('ArtifactsPage.dc.html', 'globe'): 'globe-share',
    ('ArtifactsPage.dc.html', 'list'): 'list',
    ('ArtifactsPage.dc.html', 'more'): 'more',
    ('Browser.dc.html', 'rightArrow'): 'arrow-right',
    ('Browser.dc.html', 'left'): 'arrow-left',
    ('Composer.dc.html', 'up'): 'arrow-up',
    ('ArtifactPane.dc.html', 'ext'): 'external',
    ('ArtifactPane.dc.html', 'del'): 'trash',
}
INLINE = {
    'I0': 'agent', 'I1': 'check', 'I48': 'edit', 'I49': 'thinking', 'I50': 'down', 'I51': 'question',
    'I52': 'hook', 'I53': 'warn', 'I54': 'interrupt', 'I55': 'clipboard', 'I56': 'retry', 'I57': 'branch',
    'I64': 'external', 'I65': 'sidebar', 'I67': 'plus', 'I68': 'copy', 'I69': 'clock', 'I70': 'sliders',
    'I71': 'more', 'I72': 'settings', 'I73': 'search', 'I74': 'tasks', 'I75': 'terminal', 'I76': 'diff',
    'I77': 'globe', 'I78': 'folder', 'I79': 'file', 'I80': 'term', 'I81': 'checkbox-checked',
    'I82': 'loading', 'I83': 'checkbox', 'I85': 'monitor', 'I86': 'folder', 'I87': 'relay',
    'I88': 'pull-request', 'I89': 'term', 'I90': 'info', 'I91': 'alert', 'I92': 'right', 'I93': 'archive',
    'I94': 'code', 'I95': 'image', 'I96': 'play', 'I97': 'expand', 'I98': 'arrow-up-right',
    'I99': 'session', 'I100': 'commit', 'I104': 'background', 'I105': 'search', 'I106': 'web-search',
    'I107': 'box', 'I108': 'x', 'I109': 'shield',
}

result = {}


def add(name, d, vb='0 0 24 24'):
    d = ' '.join(d.split())
    if name in result and result[name]['d'] != d:
        # keep the first one; report the clash
        if norm(result[name]['d']) != norm(d):
            print('clash', name, file=sys.stderr)
        return
    result[name] = {'d': d, 'vb': vb}


def norm(d):
    return re.sub(r'(\d)0+\b', r'\1', d)


for f in sorted(os.listdir(src)):
    if not f.endswith('.html'):
        continue
    t = open(os.path.join(src, f)).read()
    for m in re.finditer(r"^\s*([a-zA-Z]+):\s*'([mMlLhHvVcCsSqQtTaAzZ0-9.\-\s,]+)'", t, re.M):
        name, d = m.group(1), m.group(2)
        name = RENAME.get((f, name), name)
        name = re.sub(r'([A-Z])', lambda x: '-' + x.group(1).lower(), name)
        add(name, d)

for i in icons:
    if i['id'] in INLINE:
        ds = re.findall(r'd="([^"]+)"', i['inner'])
        add(INLINE[i['id']], ' '.join(ds), i['vb'])

# aliases kept short and stable
ALIAS_DROP = {'x'}
lines = ['// Generated from the inline SVG paths in docs/design/ui/*.dc.html (all 24×24, fill = currentColor).',
         '// Regenerate with scripts/design-icons.py (see its header); do not edit paths by hand.',
         '/* eslint-disable */',
         'export const ICONS = {']
for name in sorted(result):
    if name in ('finished-count', 'arrow-up', 'chat-outline'):
        continue
    lines.append("  '%s': '%s'," % (name, result[name]['d']))
lines.append('} as const')
lines.append('')
lines.append('export type IconName = keyof typeof ICONS')
lines.append('')
open(out, 'w').write('\n'.join(lines))
print(len(result), sorted(result))
