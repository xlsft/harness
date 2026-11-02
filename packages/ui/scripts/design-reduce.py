# Replaces the inline SVGs of the design artboards with short ids (I0, I1, …) for reading, and lists them in _icons.json.
import re, sys, os, json
src = sys.argv[1]
out = sys.argv[2]
os.makedirs(out, exist_ok=True)
icons = {}
order = []
svg_re = re.compile(r'<svg([^>]*)>(.*?)</svg>', re.S)


def norm(inner):
    return re.sub(r'\s+', ' ', inner.strip())


def repl(m):
    attrs, inner = m.group(1), m.group(2)
    vb = re.search(r'viewBox="([^"]*)"', attrs)
    vb = vb.group(1) if vb else ''
    key = vb + '|' + norm(inner)
    if key not in icons:
        icons[key] = 'I%d' % len(icons)
        order.append(key)
    a = re.sub(r'\s*viewBox="[^"]*"', '', attrs)
    a = re.sub(r'\s*aria-hidden="true"', '', a)
    a = re.sub(r'\s*class="ic"', '', a)
    return '<svg %s%s/>' % (icons[key], a)


for f in sorted(os.listdir(src)):
    if not f.endswith('.html'):
        continue
    t = open(os.path.join(src, f)).read()
    t = svg_re.sub(repl, t)
    open(os.path.join(out, f), 'w').write(t)
with open(os.path.join(out, '_icons.json'), 'w') as fp:
    json.dump([{'id': icons[k], 'vb': k.split('|', 1)[0], 'inner': k.split('|', 1)[1]} for k in order], fp, indent=0)
print(len(icons))
