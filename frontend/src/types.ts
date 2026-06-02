export type UserStatus = 'approved' | 'pending' | 'rejected'

export interface User {
  id: string
  email: string
  username: string
  apiToken?: string
  status: UserStatus
  isAdmin: boolean
  // UI theme preference (see theme.ts). Server-persisted; may be absent on
  // legacy cached sessions, so treat it as optional client-side.
  theme?: string
}

export interface UserSummary {
  id: string
  username: string
  email: string
  status: UserStatus
  isAdmin: boolean
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
  extraFrontmatter: string
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
  extraFrontmatter: string
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
  // When true the deployment is configured for enterprise team rollout, so the
  // connect UI leads with managed-settings guidance and tucks the per-user
  // personal-token setup behind an "expert mode" toggle.
  enterpriseMode: boolean
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

// FindingFix is a minimal patch produced by the per-finding fix endpoint.
// Only the fields the model decided to change are present — missing keys mean
// "do not touch this field". An empty string IS a value (e.g. clearing
// extraFrontmatter), so we distinguish missing from empty.
export interface FindingFix {
  name?: string
  description?: string
  body?: string
  extraFrontmatter?: string
  note?: string
}

export type RiskLevel = 'low' | 'medium' | 'high' | 'critical'
export type AuditSeverity = 'critical' | 'high' | 'medium' | 'low'

export interface AuditFinding {
  category: string
  severity: AuditSeverity
  detail: string
}

// AuditResult is the latest stored security-audit verdict for one skill.
export interface AuditResult {
  skillId: string
  pluginName: string
  skillName: string
  auditedAt: string
  model: string
  riskScore: number
  riskLevel: RiskLevel
  categories: string[]
  summary: string
  findings: AuditFinding[]
  error?: string
}

export interface AuditResultsResponse {
  enabled: boolean
  threshold: number
  running: boolean
  results: AuditResult[]
}
