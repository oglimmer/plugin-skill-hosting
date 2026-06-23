<script setup lang="ts">
import Endpoint from '../Endpoint.vue'
import ParamRow from '../ParamRow.vue'
</script>

<template>
  <div class="dev-subsection">
    <header class="section-head">
      <h2>Skill endpoints</h2>
      <p class="section-lede">
        Every skill write also bumps the parent plugin's semver and rewrites its git repo.
      </p>
    </header>

    <aside class="callout">
      <h4>Plugin version bumps</h4>
      <ul class="dev-list">
        <li><strong>First skill in a plugin</strong> — no version bump (the plugin's debut version stays); only <code>updatedAt</code> is refreshed.</li>
        <li><strong>Subsequent create / delete / restore</strong> — bumps the <strong>major</strong>.</li>
        <li><strong>Update body+description</strong> — bumps the <strong>minor</strong> when the body size changes by more than 30%, otherwise the <strong>patch</strong>.</li>
        <li><strong>Skill file upsert/delete</strong> — bumps the <strong>patch</strong>.</li>
      </ul>
    </aside>

    <Endpoint
      method="POST"
      path="/api/plugins/{name}/skills"
      summary="Create a new skill inside a plugin."
    >
      <template #request>
<pre>{
  "name":        "summarize-pr",            // slug, required
  "description": "Summarises a GitHub PR.",  // required, non-empty
  "body":        "# SKILL\n\nSteps..."       // SKILL.md body, no YAML frontmatter
}</pre>
      </template>
      <template #errors>
        <ul class="dev-list">
          <li><code>400</code> — invalid skill name or empty description</li>
          <li><code>404</code> — plugin not found</li>
          <li><code>409</code> — skill with that name already exists</li>
        </ul>
      </template>
    </Endpoint>

    <Endpoint
      method="POST"
      path="/api/plugins/{name}/skills/import"
      summary="Create a skill by uploading a packaged ZIP archive."
    >
      <template #request>
        <p>
          <code>multipart/form-data</code> with a single <code>file</code> field
          containing the ZIP. Max archive size <strong>110 MB</strong>.
        </p>
        <p>
          The ZIP must contain a <code>SKILL.md</code> at the root or inside a single
          top-level directory. The file's YAML frontmatter supplies the skill
          <code>name</code> and <code>description</code>; everything after the closing
          <code>---</code> becomes the body. Any sibling files under
          <code>scripts/</code>, <code>references/</code>, or <code>assets/</code> are
          imported as supporting files (subject to the same per-file / per-skill /
          file-count limits as the file endpoints).
        </p>
      </template>
      <template #response>
        <p>
          <code>200 OK</code> with the created skill object (same shape as
          <code>POST /skills</code> returns).
        </p>
      </template>
      <template #errors>
        <ul class="dev-list">
          <li><code>400</code> — archive too large, missing/duplicate <code>SKILL.md</code>, malformed frontmatter, or a supporting file violates path/size rules</li>
          <li><code>404</code> — plugin not found</li>
          <li><code>409</code> — a skill with that name already exists</li>
        </ul>
      </template>
    </Endpoint>

    <Endpoint
      method="PUT"
      path="/api/plugins/{name}/skills/{skill}"
      summary="Replace a skill's description and body."
    >
      <template #request>
<pre>{
  "description": "...",
  "body":        "..."
}</pre>
      </template>
      <template #response>
        <p><code>204 No Content</code></p>
      </template>
    </Endpoint>

    <Endpoint
      method="DELETE"
      path="/api/plugins/{name}/skills/{skill}"
      summary="Soft-delete a skill. Bumps the plugin major."
    >
      <template #response>
        <p><code>204 No Content</code></p>
      </template>
    </Endpoint>

    <aside class="callout">
      <h4>Locking</h4>
      <p>
        A locked skill is withdrawn from the git repo, the external mirror, and the
        MCP server, but stays visible in the web UI flagged as locked and read-only.
        While a skill is locked, content writes below (update, move, revert, and
        file writes) return <code>403</code>. Deleting is the exception: an admin may
        delete a locked skill directly (a non-admin still gets <code>403</code>), since
        removal can't republish withdrawn content. Locks are set manually by an admin or
        automatically by the <a href="#audit">security audit</a>; only an admin can
        lock or unlock. Locking does not bump the plugin version.
      </p>
    </aside>

    <Endpoint
      method="POST"
      path="/api/plugins/{name}/skills/{skill}/lock"
      summary="Lock a skill, withdrawing it from git, the external mirror, and MCP. Admin-only."
    >
      <template #request>
<pre>{
  "reason": "under security review"   // optional, shown in the UI
}</pre>
      </template>
      <template #response>
        <p><code>200 OK</code> with the updated skill object (<code>locked: true</code>).</p>
      </template>
      <template #errors>
        <ul class="dev-list">
          <li><code>403</code> — caller is not an admin</li>
          <li><code>404</code> — plugin or skill not found</li>
        </ul>
      </template>
    </Endpoint>

    <Endpoint
      method="DELETE"
      path="/api/plugins/{name}/skills/{skill}/lock"
      summary="Unlock a skill, restoring it to git, the external mirror, and MCP. Admin-only."
    >
      <template #response>
        <p><code>200 OK</code> with the updated skill object (<code>locked: false</code>).</p>
      </template>
      <template #notes>
        <p>
          If the audit had auto-locked the skill, unlocking acknowledges it: later
          audit sweeps will not re-lock it until the skill is next edited.
        </p>
      </template>
      <template #errors>
        <ul class="dev-list">
          <li><code>403</code> — caller is not an admin</li>
          <li><code>404</code> — plugin or skill not found</li>
          <li><code>409</code> — skill is not locked</li>
        </ul>
      </template>
    </Endpoint>

    <Endpoint
      method="GET"
      path="/api/plugins/{name}/deleted-skills"
      summary="List soft-deleted skills for a plugin (so you can restore them)."
    />

    <Endpoint
      method="POST"
      path="/api/plugins/{name}/skills/{skill}/restore"
      summary="Un-delete the most-recently-deleted skill of that name. Bumps the plugin major."
    >
      <template #errors>
        <ul class="dev-list">
          <li><code>404</code> — no deleted skill with that name</li>
          <li><code>409</code> — an active skill with that name already exists</li>
        </ul>
      </template>
    </Endpoint>

    <Endpoint
      method="GET"
      path="/api/plugins/{name}/skills/{skill}/versions"
      summary="Return the full edit history for a skill, newest first."
    >
      <template #response>
<pre>[
  {
    "id":          "uuid",
    "skillId":     "uuid",
    "version":     7,
    "action":      "update",   // create | update | delete | restore | revert
    "name":        "summarize-pr",
    "description": "...",
    "body":        "...",      // snapshot at this version
    "editedBy":     "uuid",
    "editedByName": "alice",
    "editedAt":    "2026-05-10T08:30:00Z"
  }
]</pre>
      </template>
    </Endpoint>

    <Endpoint
      method="POST"
      path="/api/plugins/{name}/skills/{skill}/revert/{version}"
      summary="Restore a skill (description, body, and supporting files) to an earlier version."
    >
      <template #params>
        <ParamRow name="version" type="path int" required>
          The version number from the skill's history.
        </ParamRow>
      </template>
      <template #notes>
        <p>
          Acts as both un-delete (if the skill is currently soft-deleted) and content
          rollback in one operation. A new history row of action <code>revert</code> is
          appended.
        </p>
      </template>
    </Endpoint>
  </div>
</template>

<style scoped>
.callout {
  border-left: 3px solid var(--accent);
  background: rgb(var(--accent-rgb) / 0.06);
  padding: 12px 16px;
  margin: 8px 0 20px;
}
.callout h4 {
  margin: 0 0 6px;
  font-family: var(--mono);
  font-size: 11px;
  letter-spacing: 0.2em;
  text-transform: uppercase;
  color: var(--accent-2);
}
.callout ul { margin: 4px 0; }
</style>
