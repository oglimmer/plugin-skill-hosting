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
  agents?: Agent[]
}

// Agent is a plugin-level subagent definition. It is published as
// agents/<name>.md at the plugin root, and Claude Code exposes it as the agent
// type "<plugin>:<name>". `body` is the subagent's system prompt.
export interface Agent {
  id: string
  pluginId: string
  name: string
  description: string
  body: string
  // `model` frontmatter: an alias (opus, sonnet, haiku, inherit) or a model id.
  // '' means not set.
  model: string
  // `tools` frontmatter: a comma-separated allowlist. '' means inherit all.
  tools: string
  // Any further frontmatter keys, as raw YAML.
  extraFrontmatter: string
  createdAt: string
  updatedAt: string
  createdBy?: string
  createdByName?: string
  updatedBy?: string
  updatedByName?: string
  // Lock state, same meaning as on Skill: a locked agent is withdrawn from git,
  // the external mirror and MCP, but stays visible here flagged as locked.
  locked: boolean
  lockedAt?: string
  lockedBy?: string
  lockedByName?: string
  lockSource?: 'admin' | 'audit'
  lockReason?: string
  // Audit gate state, same meaning as on Skill.
  auditPending?: boolean
  auditPendingAt?: string
  auditPendingError?: string
}

// AgentInput is the create/update payload. On update, a field left out keeps
// its stored value; '' clears it.
export interface AgentInput {
  name?: string
  description: string
  body: string
  model?: string
  tools?: string
  extraFrontmatter?: string
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
  // Lock state. A locked skill is withdrawn from git, the external mirror, and
  // MCP, but stays visible here flagged as locked. `lockSource` is 'admin' or
  // 'audit'; `lockedByName` is the admin who locked it (absent for audit locks).
  locked: boolean
  lockedAt?: string
  lockedBy?: string
  lockedByName?: string
  lockSource?: 'admin' | 'audit'
  lockReason?: string
  // Audit gate. `auditPending` means the current content has not passed the
  // security check yet, so it is saved but not published: git still serves the
  // last published version. A non-empty `auditPendingError` means the check
  // itself failed and the change is held until a retry passes or an admin
  // approves it.
  auditPending?: boolean
  auditPendingAt?: string
  auditPendingError?: string
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

// AuditResult is the latest stored security-audit verdict for one skill or one
// agent. `kind` says which: a skill result carries skillId/skillName, an agent
// result carries agentId/agentName.
export interface AuditResult {
  kind: 'skill' | 'agent'
  skillId?: string
  pluginName: string
  skillName?: string
  agentId?: string
  agentName?: string
  auditedAt: string
  model: string
  riskScore: number
  riskLevel: RiskLevel
  categories: string[]
  summary: string
  findings: AuditFinding[]
  error?: string
}

// AuditPendingItem is one change that is saved but not published yet because
// the audit gate has not released it. `error` is set when its check failed.
export interface AuditPendingItem {
  kind: 'skill' | 'agent'
  pluginName: string
  name: string
  since: string
  error?: string
}

export interface AuditResultsResponse {
  enabled: boolean
  threshold: number
  running: boolean
  results: AuditResult[]
  // gate: changes are published only after they pass the security check.
  gate?: boolean
  pending?: AuditPendingItem[]
}

export interface ExternalSyncStatus {
  remoteUrl: string
  branch: string
  inSync: boolean
  missing: string[]
  outOfDate: string[]
  extra: string[]
}

export interface ReconcileReport {
  pushed: string[]
  removed: string[]
  errors?: Record<string, string>
}
