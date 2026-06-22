import type { AuthConfig, Plugin, Skill, User } from '../types'

// Minimal valid fixtures with sensible defaults; pass overrides for the fields
// a given test cares about. Keeping these here means a shape change in types.ts
// surfaces as one compile error per builder, not scattered across every test.

export function makeUser(overrides: Partial<User> = {}): User {
  return {
    id: 'u1',
    email: 'alice@example.com',
    username: 'alice',
    status: 'approved',
    isAdmin: false,
    theme: 'light',
    ...overrides,
  }
}

export function makePlugin(name = 'demo', overrides: Partial<Plugin> = {}): Plugin {
  return {
    id: `p-${name}`,
    ownerId: 'u1',
    ownerName: 'alice',
    name,
    description: `desc for ${name}`,
    version: '0.1.0',
    authorName: '',
    authorEmail: '',
    homepage: '',
    license: 'MIT',
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

export function makeSkill(name = 'a-skill', overrides: Partial<Skill> = {}): Skill {
  return {
    id: `s-${name}`,
    pluginId: 'p-demo',
    name,
    description: `desc for ${name}`,
    body: '# body',
    extraFrontmatter: '',
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    locked: false,
    ...overrides,
  }
}

export function makeAuthConfig(overrides: Partial<AuthConfig> = {}): AuthConfig {
  return {
    mode: 'password',
    marketplaceName: 'test-market',
    defaultLicense: 'MIT',
    userApprovalRequired: false,
    enterpriseMode: false,
    ...overrides,
  }
}

// makeJwt builds a structurally-valid (unsigned) JWT whose exp is `secondsFromNow`
// in the future (or past, if negative). api.ts only base64-decodes the payload to
// read `exp`, so the signature is irrelevant here.
export function makeJwt(secondsFromNow = 3600): string {
  const header = btoa(JSON.stringify({ alg: 'none', typ: 'JWT' }))
  const exp = Math.floor(Date.now() / 1000) + secondsFromNow
  const payload = btoa(JSON.stringify({ sub: 'u1', exp }))
  return `${header}.${payload}.sig`
}
