export type UserStatus = 'approved' | 'pending' | 'rejected'

export interface User {
  id: string
  email: string
  username: string
  apiToken?: string
  status: UserStatus
}

export interface UserSummary {
  id: string
  username: string
  email: string
  status: UserStatus
  createdAt: string
  approvedBy?: string
  approvedByName?: string
  approvedAt?: string
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

export type AuthMode = 'password' | 'oidc'

export interface AuthConfig {
  mode: AuthMode
  marketplaceName: string
  defaultLicense: string
  userApprovalRequired: boolean
}

export interface BackendBuildInfo {
  name: string
  version: string
  gitCommit: string
  buildTime: string
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
