import { describe, expect, it } from 'vitest'
import { isDevVersion, isNewer, latestReleaseTag, serverUpgradeCommands } from '~/lib/releases'

describe('release versions', () => {
  it('order by semver, a pre-release before its final version', () => {
    expect(isNewer('v0.2.0', '0.1.0')).toBe(true)
    expect(isNewer('v0.1.0', '0.1.0')).toBe(false)
    expect(isNewer('v0.1.0', '0.2.0')).toBe(false)
    expect(isNewer('v0.10.0', '0.9.0')).toBe(true)
    expect(isNewer('v0.2.0', '0.2.0-rc.1')).toBe(true)
    expect(isNewer('v0.2.0-rc.1', '0.2.0')).toBe(false)
    expect(isNewer('v0.2.0-rc.10', '0.2.0-rc.9')).toBe(true)
  })

  it('compare nothing that is not a version', () => {
    expect(isNewer('nonsense', '0.1.0')).toBe(false)
    expect(isNewer('v0.1.0', '')).toBe(false)
  })

  it('know a development build', () => {
    expect(isDevVersion('0.0.0-dev')).toBe(true)
    expect(isDevVersion('0.1.0')).toBe(false)
  })
})

describe('latest release', () => {
  const answer = (status: number, body: unknown) => (async () => new Response(JSON.stringify(body), { status })) as typeof fetch

  it('reads the tag', async () => {
    await expect(latestReleaseTag(answer(200, { tag_name: 'v0.2.0' }))).resolves.toBe('v0.2.0')
  })

  it('fails on anything else', async () => {
    await expect(latestReleaseTag(answer(404, {}))).rejects.toThrow('404')
    await expect(latestReleaseTag(answer(200, { tag_name: 'nightly' }))).rejects.toThrow('without a version')
  })
})

describe('server upgrade commands', () => {
  it('name the image version without the v, and never update by themselves', () => {
    const commands = serverUpgradeCommands('v0.2.0')
    expect(commands).toContain('docker pull ghcr.io/xlsft/harness:0.2.0')
    expect(commands).toContain('docker stop harness')
    expect(commands).toContain('pg_dump')
  })
})
