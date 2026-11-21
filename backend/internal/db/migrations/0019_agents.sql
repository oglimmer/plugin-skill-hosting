-- Plugin agents (subagents)
--
-- A plugin can ship subagent definitions next to its skills. Claude Code loads
-- them from agents/<name>.md at the plugin root and exposes each one as the
-- agent type "<plugin>:<name>". An agent is a single markdown file: YAML
-- frontmatter (name, description, optional model / tools / further keys) and a
-- body that becomes the subagent's system prompt.
--
-- model and tools are stored as their own columns because they are the two
-- keys almost every agent sets; any other frontmatter key is kept verbatim in
-- extra_frontmatter, the same round-trip approach skills use.
--
-- Agents are hard-deleted and carry no version history: they have no
-- supporting files and are small enough to re-create.
--
-- An agent's body is a system prompt, so it can carry the same harmful
-- instructions as a skill. Agents therefore get the same lock (see 0018) and
-- the same security-audit result log (see 0012) as skills.

CREATE TABLE IF NOT EXISTS agents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plugin_id UUID NOT NULL REFERENCES plugins(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    tools TEXT NOT NULL DEFAULT '',
    extra_frontmatter TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_by UUID REFERENCES users(id) ON DELETE SET NULL,
    -- Lock state, same meaning as on skills: a locked agent is withdrawn from
    -- git, the external mirror and MCP, but stays visible in the web UI.
    -- locked_at NULL means "not locked".
    locked_at TIMESTAMPTZ,
    -- Admin who applied the lock. NULL when the audit applied it automatically.
    locked_by UUID REFERENCES users(id) ON DELETE SET NULL,
    -- 'admin' (manual) or 'audit' (automatic, score at/above the threshold).
    lock_source TEXT CHECK (lock_source IS NULL OR lock_source IN ('admin', 'audit')),
    lock_reason TEXT NOT NULL DEFAULT '',
    -- Set when an admin unlocks an agent the audit had locked, so a later audit
    -- does not lock it again. Cleared when the agent's content is edited.
    audit_lock_suppressed BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE (plugin_id, name)
);

CREATE INDEX IF NOT EXISTS agents_plugin_idx ON agents(plugin_id);

-- Security-audit results for agents. Same shape and meaning as
-- skill_audit_results: one row per agent per audit run, latest row wins.
CREATE TABLE IF NOT EXISTS agent_audit_results (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id    UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    audited_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    model       TEXT NOT NULL DEFAULT '',
    risk_score  INT NOT NULL DEFAULT 0,
    risk_level  TEXT NOT NULL DEFAULT 'low',
    categories  JSONB NOT NULL DEFAULT '[]'::jsonb,
    summary     TEXT NOT NULL DEFAULT '',
    findings    JSONB NOT NULL DEFAULT '[]'::jsonb,
    error       TEXT NOT NULL DEFAULT '',
    alerted     BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX IF NOT EXISTS idx_agent_audit_results_agent
    ON agent_audit_results (agent_id, audited_at DESC);
