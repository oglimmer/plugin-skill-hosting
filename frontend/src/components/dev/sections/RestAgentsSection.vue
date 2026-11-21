<script setup lang="ts">
import Endpoint from '../Endpoint.vue'
</script>

<template>
  <div class="dev-subsection">
    <header class="section-head">
      <h2>Agent endpoints</h2>
      <p class="section-lede">
        An agent is a subagent that ships with a plugin. It is published as
        <code>agents/{agent}.md</code> at the plugin root, and Claude Code exposes it
        as the agent type <code>{plugin}:{agent}</code>. Every agent write bumps the
        parent plugin's semver and rewrites its git repo.
      </p>
    </header>

    <aside class="callout">
      <h4>How agents differ from skills</h4>
      <ul class="dev-list">
        <li><strong>Create / delete</strong> — bumps the plugin <strong>major</strong>.</li>
        <li><strong>Update</strong> — bumps the <strong>minor</strong> when the body size changes by more than 30%, otherwise the <strong>patch</strong>.</li>
        <li><strong>No history</strong> — agents have no versions, no soft-delete and no restore. A delete is permanent.</li>
        <li><strong>Same audit and lock as skills</strong> — the <a href="#audit">security audit</a> checks agents too, and an admin or the audit can lock one.</li>
      </ul>
    </aside>

    <Endpoint
      method="GET"
      path="/api/plugins/{name}/agents"
      summary="List a plugin's agents, ordered by name."
    >
      <template #response>
        <p>
          <code>200 OK</code> with an array of agent objects. The same array is
          included as <code>agents</code> in <code>GET /api/plugins/{name}</code>.
        </p>
      </template>
    </Endpoint>

    <Endpoint
      method="GET"
      path="/api/plugins/{name}/agents/{agent}"
      summary="Read one agent."
    >
      <template #response>
<pre>{
  "id":               "…",
  "pluginId":         "…",
  "name":             "worker",
  "description":      "Implementation worker for one task brief.",
  "body":             "You are a worker. …",
  "model":            "opus",          // "" when not set
  "tools":            "Read, Edit",    // "" when not set
  "extraFrontmatter": "",
  "createdAt":        "…",
  "updatedAt":        "…",
  "createdByName":    "alice",
  "updatedByName":    "alice"
}</pre>
      </template>
      <template #errors>
        <ul class="dev-list">
          <li><code>404</code> — plugin or agent not found</li>
        </ul>
      </template>
    </Endpoint>

    <Endpoint
      method="POST"
      path="/api/plugins/{name}/agents"
      summary="Create a new agent inside a plugin."
    >
      <template #request>
<pre>{
  "name":             "worker",                  // slug, required
  "description":      "When to use this agent.", // required
  "body":             "You are a worker. …",     // required: the system prompt, no YAML frontmatter
  "model":            "opus",                    // optional: opus | sonnet | haiku | inherit | a model id
  "tools":            "Read, Edit, Bash",        // optional: comma-separated allowlist
  "extraFrontmatter": "color: blue"              // optional: further keys as raw YAML
}</pre>
        <p>
          <code>extraFrontmatter</code> must not repeat <code>name</code>,
          <code>description</code>, <code>model</code> or <code>tools</code>, and must
          not contain a <code>---</code> line.
        </p>
      </template>
      <template #response>
        <p><code>200 OK</code> with the created agent object.</p>
      </template>
      <template #errors>
        <ul class="dev-list">
          <li><code>400</code> — invalid name, empty description or body, or invalid model / tools / extraFrontmatter</li>
          <li><code>404</code> — plugin not found</li>
          <li><code>409</code> — agent with that name already exists</li>
        </ul>
      </template>
    </Endpoint>

    <Endpoint
      method="PUT"
      path="/api/plugins/{name}/agents/{agent}"
      summary="Replace an agent's content. The name cannot change."
    >
      <template #request>
<pre>{
  "description":      "...",   // required
  "body":             "...",   // required
  "model":            "...",   // optional: omit to keep, "" to clear
  "tools":            "...",   // optional: omit to keep, "" to clear
  "extraFrontmatter": "..."    // optional: omit to keep, "" to clear
}</pre>
      </template>
      <template #response>
        <p><code>200 OK</code> with the updated agent object.</p>
      </template>
      <template #errors>
        <ul class="dev-list">
          <li><code>400</code> — validation failed (same rules as create)</li>
          <li><code>404</code> — plugin or agent not found</li>
        </ul>
      </template>
    </Endpoint>

    <Endpoint
      method="DELETE"
      path="/api/plugins/{name}/agents/{agent}"
      summary="Delete an agent permanently. Bumps the plugin major."
    >
      <template #response>
        <p><code>204 No Content</code></p>
      </template>
      <template #errors>
        <ul class="dev-list">
          <li><code>403</code> — caller is not the plugin owner, an admin, or the agent's creator; or the agent is locked and the caller is not an admin</li>
          <li><code>404</code> — plugin or agent not found</li>
        </ul>
      </template>
    </Endpoint>

    <Endpoint
      method="POST"
      path="/api/plugins/{name}/agents/{agent}/audit-approve"
      summary="Publish an agent's pending or held change without a passed security check. Admin-only."
    >
      <template #response>
        <p>
          <code>200 OK</code> with the updated agent object
          (<code>auditPending: false</code>). See the
          <a href="#audit">publish gate</a>.
        </p>
      </template>
      <template #errors>
        <ul class="dev-list">
          <li><code>403</code> — caller is not an admin</li>
          <li><code>404</code> — plugin or agent not found</li>
          <li><code>409</code> — the agent has no pending change</li>
        </ul>
      </template>
    </Endpoint>

    <aside class="callout">
      <h4>Locking</h4>
      <p>
        A locked agent is withdrawn from the git repo, the external mirror, and the
        MCP server, but stays visible in the web UI flagged as locked. While it is
        locked, <code>PUT</code> returns <code>403</code>, and only an admin can
        delete it. Locks are set manually by an admin or automatically by the
        <a href="#audit">security audit</a>; only an admin can lock or unlock.
        Locking does not bump the plugin version.
      </p>
    </aside>

    <Endpoint
      method="POST"
      path="/api/plugins/{name}/agents/{agent}/lock"
      summary="Lock an agent, withdrawing it from git, the external mirror, and MCP. Admin-only."
    >
      <template #request>
<pre>{
  "reason": "under security review"   // optional, shown in the UI
}</pre>
      </template>
      <template #response>
        <p><code>200 OK</code> with the updated agent object (<code>locked: true</code>).</p>
      </template>
      <template #errors>
        <ul class="dev-list">
          <li><code>403</code> — caller is not an admin</li>
          <li><code>404</code> — plugin or agent not found</li>
        </ul>
      </template>
    </Endpoint>

    <Endpoint
      method="DELETE"
      path="/api/plugins/{name}/agents/{agent}/lock"
      summary="Unlock an agent, restoring it to git, the external mirror, and MCP. Admin-only."
    >
      <template #response>
        <p>
          <code>200 OK</code> with the updated agent object. Unlocking an audit lock
          acknowledges the finding: later audits do not lock the agent again until
          it is edited.
        </p>
      </template>
      <template #errors>
        <ul class="dev-list">
          <li><code>403</code> — caller is not an admin</li>
          <li><code>404</code> — plugin or agent not found</li>
          <li><code>409</code> — agent is not locked</li>
        </ul>
      </template>
    </Endpoint>
  </div>
</template>
