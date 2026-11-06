// Releases of harness on GitHub: the newest stable version, what "newer" means, and what an admin runs to upgrade a server.
// The client decides all of this; the server never updates itself and isn't asked.

export const RELEASES_URL = 'https://github.com/xlsft/harness/releases'
export const SERVER_IMAGE = 'ghcr.io/xlsft/harness'

const LATEST_API = 'https://api.github.com/repos/xlsft/harness/releases/latest'

interface Version {
  core: [number, number, number]
  pre: string
}

function parse(v: string): Version | null {
  const m = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$/.exec(v.trim())
  return m ? { core: [Number(m[1]), Number(m[2]), Number(m[3])], pre: m[4] ?? '' } : null
}

function comparePre(a: string, b: string) {
  const as = a.split('.')
  const bs = b.split('.')
  for (let i = 0; i < Math.min(as.length, bs.length); i++) {
    const an = /^\d+$/.test(as[i]!) ? Number(as[i]) : null
    const bn = /^\d+$/.test(bs[i]!) ? Number(bs[i]) : null
    if (an !== null && bn !== null) {
      if (an !== bn) return an - bn
    } else if (an !== null) {
      return -1
    } else if (bn !== null) {
      return 1
    } else if (as[i] !== bs[i]) {
      return as[i]! < bs[i]! ? -1 : 1
    }
  }
  return as.length - bs.length
}

/** Whether release `tag` is newer than `current`. A pre-release sorts before its final version. */
export function isNewer(tag: string, current: string) {
  const a = parse(tag)
  const b = parse(current)
  if (!a || !b) return false
  for (let i = 0; i < 3; i++) {
    if (a.core[i] !== b.core[i]) return a.core[i]! > b.core[i]!
  }
  if (!a.pre || !b.pre) return !a.pre && !!b.pre
  return comparePre(a.pre, b.pre) > 0
}

/** A build not made by the release workflow reports 0.0.0-dev: no comparison means anything for it. */
export const isDevVersion = (v: string) => v.endsWith('-dev')

/** The newest stable release's tag. Never a pre-release or a draft: GitHub's "latest" is neither. */
export async function latestReleaseTag(fetcher: typeof fetch = fetch): Promise<string> {
  const res = await fetcher(LATEST_API, { headers: { Accept: 'application/vnd.github+json' } })
  if (!res.ok) throw new Error(`GitHub answered ${res.status}`)
  const body = await res.json() as { tag_name?: string }
  if (!body.tag_name || !parse(body.tag_name)) throw new Error('GitHub answered without a version')
  return body.tag_name
}

/** What an admin runs to move a server to `tag`: pull, then replace the container on the same volume and environment. */
export function serverUpgradeCommands(tag: string) {
  const image = `${SERVER_IMAGE}:${tag.replace(/^v/, '')}`
  return [
    `docker pull ${image}`,
    'docker exec harness pg_dump -U harness -Fc harness > harness-$(date +%F).dump',
    'docker stop harness && docker rm harness',
    `docker run -d --name harness ... ${image}   # the same -p, -v and -e as before`
  ].join('\n')
}
