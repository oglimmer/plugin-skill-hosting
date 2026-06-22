import type {
  AuthConfig,
  BackendBuildInfo,
  Plugin,
  Skill,
  SkillFile,
  SkillFileSummary,
  SkillVersion,
  User,
  UserSummary,
  ValidationReport,
  Finding,
  FindingFix,
  AuditResultsResponse,
  ExternalSyncStatus,
  ReconcileReport,
} from './types'

function token(): string | null {
  return localStorage.getItem('token')
}

// isJwtExpired decodes a JWT's payload and reports whether its `exp` claim is
// already in the past. Anything it can't confidently read as expired — a
// malformed token, a missing/non-numeric exp — returns false so the server
// stays the source of truth in ambiguous cases. This lets us treat a session
// as logged-out client-side before wasting a round-trip on a doomed request.
export function isJwtExpired(tok: string): boolean {
  const parts = tok.split('.')
  if (parts.length !== 3) return false
  try {
    const json = atob(parts[1].replace(/-/g, '+').replace(/_/g, '/'))
    const claims = JSON.parse(json) as { exp?: number }
    if (typeof claims.exp !== 'number') return false
    return claims.exp * 1000 <= Date.now()
  } catch {
    return false
  }
}

// ApiError carries the HTTP status alongside the message so callers can react
// to *what kind* of failure occurred — e.g. show a 404 error page for a
// missing plugin vs a 500 page for a server fault. Plain `errMsg(e)` still
// works on it since it extends Error.
export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

// SLUG_RE mirrors the backend slug rule (app.go slugRe): a lowercase slug of
// 3–64 chars that starts and ends alphanumeric. Plain regex literal — no `u`/`v`
// flag — so it parses everywhere; the HTML `pattern` attribute is just a hint.
const SLUG_RE = /^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$/

// slugError returns a human-readable validation message for an invalid slug, or
// '' when the value is acceptable. Callers use it to give in-UI feedback before
// hitting the API, instead of relying on the browser's native pattern bubble.
export function slugError(value: string): string {
  if (SLUG_RE.test(value)) return ''
  return 'name must be a lowercase slug — 3–64 characters, letters/digits/hyphens, starting and ending with a letter or digit'
}

export function errMsg(e: unknown, fallback = 'something went wrong'): string {
  if (e instanceof Error) return e.message || fallback
  if (typeof e === 'string') return e
  return fallback
}

// errStatus pulls the HTTP status out of a caught error, or undefined if the
// failure didn't come from the API (e.g. a network error).
export function errStatus(e: unknown): number | undefined {
  return e instanceof ApiError ? e.status : undefined
}

async function request<T>(path: string, opts: RequestInit = {}): Promise<T> {
  const headers = new Headers(opts.headers)
  headers.set('Content-Type', 'application/json')
  const t = token()
  if (t) {
    // Bail before the network call once the token is past its exp — the server
    // would only answer 401 anyway, and short-circuiting lets callers/guards
    // react to an expired session uniformly.
    if (isJwtExpired(t)) throw new ApiError(401, 'session expired')
    headers.set('Authorization', `Bearer ${t}`)
  }
  const res = await fetch(path, { ...opts, headers })
  if (!res.ok) {
    let msg = res.statusText
    try {
      const data = await res.json()
      if (data && data.error) msg = data.error
    } catch {}
    throw new ApiError(res.status, msg)
  }
  if (res.status === 204) return undefined as T
  return res.json() as Promise<T>
}

export const api = {
  version: () => request<BackendBuildInfo>('/api/version'),
  authConfig: () => request<AuthConfig>('/api/auth/config'),
  register: (email: string, username: string, password: string) =>
    request<{ token: string; user: User }>('/api/auth/register', {
      method: 'POST',
      body: JSON.stringify({ email, username, password }),
    }),
  login: (email: string, password: string) =>
    request<{ token: string; user: User }>('/api/auth/login', {
      method: 'POST',
      body: JSON.stringify({ email, password }),
    }),
  me: () => request<User>('/api/me'),
  setTheme: (theme: string) =>
    request<{ theme: string }>('/api/me/theme', {
      method: 'PUT',
      body: JSON.stringify({ theme }),
    }),
  regenerateToken: () =>
    request<{ apiToken: string }>('/api/me/token/regenerate', { method: 'POST' }),
  revokeSessions: () =>
    request<void>('/api/me/sessions/revoke', { method: 'POST' }),
  listUsers: () => request<UserSummary[]>('/api/users'),
  approveUser: (id: string) =>
    request<void>(`/api/users/${id}/approve`, { method: 'POST' }),
  rejectUser: (id: string) =>
    request<void>(`/api/users/${id}/reject`, { method: 'POST' }),
  deleteUser: (id: string) =>
    request<void>(`/api/users/${id}`, { method: 'DELETE' }),
  promoteUser: (id: string) =>
    request<void>(`/api/users/${id}/promote`, { method: 'POST' }),
  demoteUser: (id: string) =>
    request<void>(`/api/users/${id}/demote`, { method: 'POST' }),
  listAuditResults: () => request<AuditResultsResponse>('/api/audit/results'),
  runAudit: () => request<{ status: string }>('/api/audit/run', { method: 'POST' }),
  externalGitStatus: () => request<ExternalSyncStatus>('/api/external-git/status'),
  externalGitReconcile: () =>
    request<ReconcileReport>('/api/external-git/reconcile', { method: 'POST' }),
  listPlugins: () => request<Plugin[]>('/api/plugins'),
  getPlugin: (name: string) => request<Plugin>(`/api/plugins/${name}`),
  createPlugin: (data: Partial<Plugin>) =>
    request<Plugin>('/api/plugins', { method: 'POST', body: JSON.stringify(data) }),
  updatePlugin: (name: string, data: Partial<Plugin>) =>
    request<Plugin>(`/api/plugins/${name}`, { method: 'PUT', body: JSON.stringify(data) }),
  deletePlugin: (name: string) =>
    request<void>(`/api/plugins/${name}`, { method: 'DELETE' }),
  listDeletedPlugins: () => request<Plugin[]>('/api/me/deleted-plugins'),
  restorePlugin: (name: string) =>
    request<Plugin>(`/api/plugins/${name}/restore`, { method: 'POST' }),
  createSkill: (pluginName: string, data: Partial<Skill>) =>
    request<Skill>(`/api/plugins/${pluginName}/skills`, {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  importSkill: async (pluginName: string, zip: File): Promise<Skill> => {
    const form = new FormData()
    form.append('file', zip)
    const headers = new Headers()
    const t = token()
    if (t) {
      if (isJwtExpired(t)) throw new ApiError(401, 'session expired')
      headers.set('Authorization', `Bearer ${t}`)
    }
    const res = await fetch(`/api/plugins/${pluginName}/skills/import`, {
      method: 'POST',
      headers,
      body: form,
    })
    if (!res.ok) {
      let msg = res.statusText
      try {
        const data = await res.json()
        if (data && data.error) msg = data.error
      } catch {}
      throw new ApiError(res.status, msg)
    }
    return res.json() as Promise<Skill>
  },
  updateSkill: (pluginName: string, skillName: string, data: Partial<Skill>) =>
    request<void>(`/api/plugins/${pluginName}/skills/${skillName}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
  deleteSkill: (pluginName: string, skillName: string) =>
    request<void>(`/api/plugins/${pluginName}/skills/${skillName}`, {
      method: 'DELETE',
    }),
  moveSkill: (pluginName: string, skillName: string, targetPlugin: string) =>
    request<Skill>(`/api/plugins/${pluginName}/skills/${skillName}/move`, {
      method: 'POST',
      body: JSON.stringify({ targetPlugin }),
    }),
  // Admin-only. Locking withdraws the skill from git, the external mirror, and
  // MCP; unlocking restores it. The server returns the updated skill.
  lockSkill: (pluginName: string, skillName: string, reason: string) =>
    request<Skill>(`/api/plugins/${pluginName}/skills/${skillName}/lock`, {
      method: 'POST',
      body: JSON.stringify({ reason }),
    }),
  unlockSkill: (pluginName: string, skillName: string) =>
    request<Skill>(`/api/plugins/${pluginName}/skills/${skillName}/lock`, {
      method: 'DELETE',
    }),
  listDeletedSkills: (pluginName: string) =>
    request<Skill[]>(`/api/plugins/${pluginName}/deleted-skills`),
  restoreSkill: (pluginName: string, skillName: string) =>
    request<Skill>(`/api/plugins/${pluginName}/skills/${skillName}/restore`, {
      method: 'POST',
    }),
  skillVersions: (pluginName: string, skillName: string) =>
    request<SkillVersion[]>(`/api/plugins/${pluginName}/skills/${skillName}/versions`),
  revertSkill: (pluginName: string, skillName: string, version: number) =>
    request<Skill>(`/api/plugins/${pluginName}/skills/${skillName}/revert/${version}`, {
      method: 'POST',
    }),
  validateSkill: (data: {
    pluginName?: string
    skillName?: string
    name: string
    description: string
    body: string
    files?: SkillFileSummary[]
  }) =>
    request<ValidationReport>(`/api/skills/validate`, {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  fixFinding: (data: {
    pluginName?: string
    skillName?: string
    name: string
    description: string
    body: string
    extraFrontmatter?: string
    files?: SkillFileSummary[]
    finding: Finding
  }) =>
    request<FindingFix>(`/api/skills/finding-fix`, {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  listSkillFiles: (pluginName: string, skillName: string) =>
    request<SkillFileSummary[]>(
      `/api/plugins/${pluginName}/skills/${skillName}/files`,
    ),
  getSkillFile: (pluginName: string, skillName: string, path: string) =>
    request<SkillFile>(
      `/api/plugins/${pluginName}/skills/${skillName}/files/${path}`,
    ),
  putSkillFile: (
    pluginName: string,
    skillName: string,
    path: string,
    data: { content: string; isBinary: boolean },
  ) =>
    request<SkillFile>(
      `/api/plugins/${pluginName}/skills/${skillName}/files/${path}`,
      { method: 'PUT', body: JSON.stringify(data) },
    ),
  deleteSkillFile: (pluginName: string, skillName: string, path: string) =>
    request<void>(
      `/api/plugins/${pluginName}/skills/${skillName}/files/${path}`,
      { method: 'DELETE' },
    ),
}
