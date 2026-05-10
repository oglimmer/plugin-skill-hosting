export interface User {
  id: string
  email: string
  username: string
  apiToken?: string
}

export interface Plugin {
  id: string
  ownerId: string
  ownerName?: string
  name: string
  description: string
  version: string
  authorName: string
  authorEmail: string
  homepage: string
  license: string
  createdAt: string
  updatedAt: string
  deletedAt?: string
  deletedBy?: string
  deletedByName?: string
  skills?: Skill[]
}

export interface Skill {
  id: string
  pluginId: string
  name: string
  description: string
  body: string
  createdAt: string
  updatedAt: string
  createdBy?: string
  createdByName?: string
  updatedBy?: string
  updatedByName?: string
  deletedAt?: string
  deletedBy?: string
  deletedByName?: string
}

export interface SkillVersion {
  id: string
  skillId: string
  version: number
  action: 'create' | 'update' | 'delete' | 'restore' | 'revert'
  name: string
  description: string
  body: string
  editedBy?: string
  editedByName?: string
  editedAt: string
}

export interface SkillFileSummary {
  path: string
  isBinary: boolean
  sizeBytes: number
  updatedAt: string
}

export interface SkillFile extends SkillFileSummary {
  content: string // raw text when !isBinary, base64 when isBinary
}

function token(): string | null {
  return localStorage.getItem('token')
}

async function request<T>(path: string, opts: RequestInit = {}): Promise<T> {
  const headers = new Headers(opts.headers)
  headers.set('Content-Type', 'application/json')
  const t = token()
  if (t) headers.set('Authorization', `Bearer ${t}`)
  const res = await fetch(path, { ...opts, headers })
  if (!res.ok) {
    let msg = res.statusText
    try {
      const data = await res.json()
      if (data && data.error) msg = data.error
    } catch {}
    throw new Error(msg)
  }
  if (res.status === 204) return undefined as T
  return res.json() as Promise<T>
}

export type AuthMode = 'password' | 'oidc'

export interface AuthConfig {
  mode: AuthMode
  marketplaceName: string
  defaultLicense: string
}

export const api = {
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
  regenerateToken: () =>
    request<{ apiToken: string }>('/api/me/token/regenerate', { method: 'POST' }),
  listPlugins: () => request<Plugin[]>('/api/plugins'),
  getPlugin: (name: string) => request<Plugin>(`/api/plugins/${name}`),
  createPlugin: (data: Partial<Plugin>) =>
    request<Plugin>('/api/plugins', { method: 'POST', body: JSON.stringify(data) }),
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
  updateSkill: (pluginName: string, skillName: string, data: Partial<Skill>) =>
    request<void>(`/api/plugins/${pluginName}/skills/${skillName}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
  deleteSkill: (pluginName: string, skillName: string) =>
    request<void>(`/api/plugins/${pluginName}/skills/${skillName}`, {
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
    name: string
    description: string
    body: string
    files?: SkillFileSummary[]
  }) =>
    request<ValidationReport>(`/api/skills/validate`, {
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

export type FindingSeverity = 'problem' | 'warning' | 'info'

export interface Finding {
  severity: FindingSeverity
  title: string
  detail: string
}

export interface ValidationReport {
  summary: string
  findings: Finding[]
  suggestedDescription?: string
}
