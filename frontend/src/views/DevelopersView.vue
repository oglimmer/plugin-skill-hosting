<script setup lang="ts">
import { computed, defineComponent, h, type PropType, type SlotsType } from 'vue'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()

const origin = computed(() => window.location.origin)
const apiToken = computed(() => auth.user?.apiToken ?? '')
const exampleToken = computed(() => apiToken.value || '<your-api-token>')

const hostNoScheme = computed(() => origin.value.replace(/^https?:\/\//, ''))
const authedOrigin = computed(() => {
  const t = apiToken.value || 'TOKEN'
  return origin.value.replace(/^(https?:\/\/)/, `$1_:${t}@`)
})

const sections = [
  { id: 'overview', title: 'Overview' },
  { id: 'auth', title: 'Authentication' },
  { id: 'rest-auth', title: 'Auth endpoints' },
  { id: 'rest-me', title: 'Account endpoints' },
  { id: 'rest-plugins', title: 'Plugin endpoints' },
  { id: 'rest-skills', title: 'Skill endpoints' },
  { id: 'rest-files', title: 'Skill file endpoints' },
  { id: 'rest-validate', title: 'Skill validator' },
  { id: 'marketplace', title: 'Marketplace feed' },
  { id: 'git', title: 'Git access' },
  { id: 'mcp', title: 'MCP server' },
  { id: 'errors', title: 'Errors' },
]

const ParamRow = defineComponent({
  name: 'ParamRow',
  props: {
    name: { type: String, required: true },
    type: { type: String, required: true },
    required: { type: Boolean, default: false },
  },
  setup(props, { slots }) {
    return () =>
      h('div', { class: 'param-row' }, [
        h('div', { class: 'param-head' }, [
          h('code', { class: 'param-name' }, props.name),
          h('span', { class: 'param-type' }, props.type),
          props.required ? h('span', { class: 'param-required' }, 'required') : null,
        ]),
        h('div', { class: 'param-desc' }, slots.default?.()),
      ])
  },
})

const Endpoint = defineComponent({
  name: 'Endpoint',
  props: {
    method: { type: String as PropType<'GET' | 'POST' | 'PUT' | 'DELETE'>, required: true },
    path: { type: String, required: true },
    summary: { type: String, required: true },
    auth: { type: Boolean, default: true },
  },
  slots: Object as SlotsType<{
    request?: () => any
    response?: () => any
    params?: () => any
    notes?: () => any
    errors?: () => any
    example?: () => any
  }>,
  setup(props, { slots }) {
    const slug = (s: string) =>
      s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/(^-|-$)/g, '')
    return () => {
      const id = `ep-${props.method.toLowerCase()}-${slug(props.path)}`
      return h('div', { class: 'endpoint', id }, [
        h('div', { class: 'endpoint-head' }, [
          h(
            'span',
            { class: ['endpoint-method', `method-${props.method.toLowerCase()}`] },
            props.method,
          ),
          h('code', { class: 'endpoint-path' }, props.path),
          props.auth
            ? h('span', { class: 'endpoint-auth' }, 'auth required')
            : h('span', { class: 'endpoint-auth endpoint-auth--public' }, 'public'),
        ]),
        h('p', { class: 'endpoint-summary' }, props.summary),
        slots.params
          ? h('div', { class: 'endpoint-block' }, [h('h4', 'Path parameters'), slots.params()])
          : null,
        slots.request
          ? h('div', { class: 'endpoint-block' }, [h('h4', 'Request body'), slots.request()])
          : null,
        slots.response
          ? h('div', { class: 'endpoint-block' }, [h('h4', 'Response'), slots.response()])
          : null,
        slots.errors
          ? h('div', { class: 'endpoint-block' }, [h('h4', 'Errors'), slots.errors()])
          : null,
        slots.example
          ? h('div', { class: 'endpoint-block' }, [h('h4', 'Example'), slots.example()])
          : null,
        slots.notes
          ? h('div', { class: 'endpoint-block endpoint-notes' }, [slots.notes()])
          : null,
      ])
    }
  },
})
</script>

<template>
  <div class="dev-layout">
    <aside class="dev-toc">
      <h3 class="dev-toc-title">On this page</h3>
      <nav>
        <a v-for="s in sections" :key="s.id" :href="`#${s.id}`">{{ s.title }}</a>
      </nav>
    </aside>

    <article class="dev-content">
      <header class="dev-header">
        <p class="kicker">API Reference</p>
        <h1>For Developers</h1>
        <p class="lede">
          Everything you need to talk to the marketplace from your own tools — REST,
          Git, and MCP — with the exact endpoints, parameters, and example calls.
        </p>
      </header>

      <!-- Overview -->
      <section id="overview" class="card">
        <h2>Overview</h2>
        <p>The marketplace exposes four surfaces:</p>
        <ul class="dev-list">
          <li>
            <strong>REST API</strong> under <code>/api/*</code> — used by the web UI and any
            programmatic client. JSON in, JSON out.
          </li>
          <li>
            <strong>Marketplace feed</strong> at <code>/marketplace.json</code> — the
            machine-readable index Claude Code consumes when you add this server as a plugin
            marketplace.
          </li>
          <li>
            <strong>Git</strong> over HTTP under <code>/git/*</code> — every plugin is a real
            bare repository, served via Smart HTTP, ready for <code>git clone</code>.
          </li>
          <li>
            <strong>MCP</strong> at <code>/mcp</code> — a Model Context Protocol server so
            Claude (or any MCP-aware client) can list plugins and create / modify skills as
            tool calls.
          </li>
        </ul>

        <h3>Base URL</h3>
        <pre>{{ origin }}</pre>
        <p class="muted">All paths in this document are relative to the base URL above.</p>

        <h3>Conventions</h3>
        <ul class="dev-list">
          <li>Request and response bodies are JSON encoded as UTF-8.</li>
          <li>Names — for plugins and skills — are lowercase slugs matching <code>^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$</code> (3–64 characters).</li>
          <li>Timestamps are RFC 3339 / ISO 8601 in UTC.</li>
          <li>Unless noted otherwise, success returns <code>200</code> with a JSON body, or <code>204 No Content</code> for write operations that don't need to echo state.</li>
        </ul>
      </section>

      <!-- Authentication -->
      <section id="auth" class="card">
        <h2>Authentication</h2>
        <p>
          Almost every endpoint requires a credential. Three forms are accepted; pick whichever fits the caller.
        </p>

        <h3>1. JWT (browser session)</h3>
        <p>
          Issued by <code>POST /api/auth/register</code> and <code>POST /api/auth/login</code>.
          Valid for 30 days. Send it as a Bearer token:
        </p>
        <pre>Authorization: Bearer eyJhbGciOiJIUzI1NiIs...</pre>
        <p class="muted">
          JWTs are recognised by their three dot-separated segments. They're meant for the
          web UI; for scripts, prefer the API token.
        </p>

        <h3>2. API token (recommended for automation)</h3>
        <p>
          A long-lived opaque token tied to your user. Find it on the home page under
          <em>Advanced: raw API token</em>, or fetch it from <code>GET /api/me</code>.
          Send it the same way as a JWT:
        </p>
        <pre>Authorization: Bearer {{ exampleToken }}</pre>

        <h3>3. HTTP Basic</h3>
        <p>
          Username is ignored; the password must be your API token. This is what
          <code>git clone</code> uses, and it's why the marketplace URL embeds the token
          as <code>https://_:&lt;token&gt;@host/...</code>.
        </p>
        <pre>curl -u _:{{ exampleToken }} {{ origin }}/marketplace.json</pre>

        <h3>Regenerating the token</h3>
        <p>
          <code>POST /api/me/token/regenerate</code> issues a new token and invalidates the
          old one. Existing marketplace links and Git remotes will stop working until you
          update them.
        </p>

        <h3>OIDC mode</h3>
        <p>
          When the server is started with <code>AUTH_MODE=oidc</code>, the password endpoints
          are replaced by an OAuth Authorization Code flow:
          <code>GET /api/auth/oidc/login</code> redirects to the IdP and
          <code>GET /api/auth/oidc/callback</code> completes the exchange. The result is the
          same JWT + API-token shape as password mode. Use
          <code>GET /api/auth/config</code> to discover which mode is active.
        </p>
      </section>

      <!-- Auth endpoints -->
      <section id="rest-auth" class="card">
        <h2>Auth endpoints</h2>

        <Endpoint
          method="GET"
          path="/api/auth/config"
          summary="Returns server configuration the login UI needs."
          :auth="false"
        >
          <template #response>
<pre>{
  "mode": "password",
  "marketplaceName": "oglimmer-marketplace",
  "defaultLicense": "MIT"
}</pre>
          </template>
          <template #notes>
            <p><code>mode</code> is either <code>password</code> or <code>oidc</code>.</p>
          </template>
        </Endpoint>

        <Endpoint
          method="POST"
          path="/api/auth/register"
          summary="Create a new account (password mode only)."
          :auth="false"
        >
          <template #request>
<pre>{
  "email":    "you@example.com",
  "username": "your-handle",
  "password": "at-least-8-chars"
}</pre>
          </template>
          <template #response>
<pre>{
  "token": "eyJhbGciOi...",       // JWT, send as Bearer
  "user": {
    "id":       "uuid",
    "email":    "you@example.com",
    "username": "your-handle",
    "apiToken": "32-byte hex",    // permanent API token
    "createdAt": "2026-05-10T12:00:00Z"
  }
}</pre>
          </template>
          <template #errors>
            <ul class="dev-list">
              <li><code>400</code> — invalid email, bad username, or password &lt; 8 chars</li>
              <li><code>409</code> — email or username already taken</li>
            </ul>
          </template>
        </Endpoint>

        <Endpoint
          method="POST"
          path="/api/auth/login"
          summary="Exchange email + password for a JWT (password mode only)."
          :auth="false"
        >
          <template #request>
<pre>{ "email": "you@example.com", "password": "..." }</pre>
          </template>
          <template #response>
            <p>Same shape as <code>/api/auth/register</code>.</p>
          </template>
          <template #errors>
            <ul class="dev-list"><li><code>401</code> — invalid credentials</li></ul>
          </template>
        </Endpoint>

        <Endpoint
          method="GET"
          path="/api/auth/oidc/login"
          summary="Begin the OIDC Authorization Code flow."
          :auth="false"
        >
          <template #notes>
            <p>
              Redirects (<code>302</code>) to the configured IdP. State + nonce are stored in
              short-lived cookies scoped to <code>/api/auth/oidc</code>. Available only when
              <code>AUTH_MODE=oidc</code>.
            </p>
          </template>
        </Endpoint>

        <Endpoint
          method="GET"
          path="/api/auth/oidc/callback"
          summary="OIDC redirect target. Validates the response and finishes login."
          :auth="false"
        >
          <template #notes>
            <p>
              On success it issues the same JWT + API-token pair as the password endpoints
              and redirects the browser back to the SPA.
            </p>
          </template>
        </Endpoint>
      </section>

      <!-- Account endpoints -->
      <section id="rest-me" class="card">
        <h2>Account endpoints</h2>

        <Endpoint method="GET" path="/api/me" summary="Return the authenticated user.">
          <template #response>
<pre>{
  "id":        "uuid",
  "email":     "you@example.com",
  "username":  "your-handle",
  "apiToken":  "32-byte hex",
  "createdAt": "2026-05-10T12:00:00Z"
}</pre>
          </template>
          <template #example>
<pre>curl -H "Authorization: Bearer {{ exampleToken }}" \
  {{ origin }}/api/me</pre>
          </template>
        </Endpoint>

        <Endpoint
          method="POST"
          path="/api/me/token/regenerate"
          summary="Roll the API token. The previous token stops working immediately."
        >
          <template #response>
<pre>{ "apiToken": "new 32-byte hex" }</pre>
          </template>
        </Endpoint>

        <Endpoint
          method="GET"
          path="/api/me/deleted-plugins"
          summary="List soft-deleted plugins owned by the caller."
        >
          <template #notes>
            <p>
              Returns the same shape as <code>GET /api/plugins</code>, restricted to rows
              with a non-null <code>deletedAt</code>. Use the restore endpoint to bring one
              back.
            </p>
          </template>
        </Endpoint>
      </section>

      <!-- Plugins -->
      <section id="rest-plugins" class="card">
        <h2>Plugin endpoints</h2>

        <Endpoint method="GET" path="/api/plugins" summary="List every active (non-deleted) plugin.">
          <template #response>
<pre>[
  {
    "id":          "uuid",
    "ownerId":     "uuid",
    "ownerName":   "alice",
    "name":        "my-plugin",
    "description": "Short summary",
    "version":     "1.2.0",
    "authorName":  "Alice",
    "authorEmail": "alice@example.com",
    "homepage":    "https://...",
    "license":     "MIT",
    "createdAt":   "2026-04-01T10:00:00Z",
    "updatedAt":   "2026-05-10T08:30:00Z"
  }
]</pre>
          </template>
        </Endpoint>

        <Endpoint
          method="GET"
          path="/api/plugins/{name}"
          summary="Fetch a single plugin and its skills."
        >
          <template #params>
            <ParamRow name="name" type="path string" required>
              The plugin slug.
            </ParamRow>
          </template>
          <template #response>
            <p>
              Same fields as the list endpoint, plus a <code>skills</code> array. Each skill
              row carries <code>id</code>, <code>name</code>, <code>description</code>,
              <code>body</code>, audit columns (<code>createdBy</code> / <code>updatedBy</code>
              with usernames), and timestamps.
            </p>
          </template>
          <template #errors>
            <ul class="dev-list"><li><code>404</code> — plugin not found</li></ul>
          </template>
        </Endpoint>

        <Endpoint
          method="POST"
          path="/api/plugins"
          summary="Create a new plugin owned by the caller."
        >
          <template #request>
<pre>{
  "name":        "my-plugin",          // slug, required
  "description": "Short summary",
  "authorName":  "Alice",
  "authorEmail": "alice@example.com",
  "homepage":    "https://...",
  "license":     "MIT"
}</pre>
          </template>
          <template #notes>
            <p>
              The first plugin you create starts at version <code>0.1.0</code>; every
              subsequent plugin starts at <code>1.0.0</code>. The <code>version</code> field
              in the request is ignored — it is fully managed by the server based on later
              skill activity.
            </p>
            <p>
              On success the plugin's bare git repo is materialized at
              <code>/git/{name}.git</code>.
            </p>
          </template>
          <template #example>
<pre>curl -X POST -H "Authorization: Bearer {{ exampleToken }}" \
  -H "Content-Type: application/json" \
  -d '{"name":"weather-tools","description":"Forecast skills"}' \
  {{ origin }}/api/plugins</pre>
          </template>
          <template #errors>
            <ul class="dev-list">
              <li><code>400</code> — invalid name (not a valid slug)</li>
              <li><code>409</code> — plugin name already taken</li>
            </ul>
          </template>
        </Endpoint>

        <Endpoint
          method="DELETE"
          path="/api/plugins/{name}"
          summary="Soft-delete a plugin you own."
        >
          <template #notes>
            <p>
              The row stays in the database; it disappears from listings, the marketplace
              feed, and the bare repo on disk is removed. Use the restore endpoint to
              recreate it.
            </p>
          </template>
          <template #response>
            <p><code>204 No Content</code></p>
          </template>
          <template #errors>
            <ul class="dev-list">
              <li><code>403</code> — not your plugin</li>
              <li><code>404</code> — plugin not found</li>
            </ul>
          </template>
        </Endpoint>

        <Endpoint
          method="POST"
          path="/api/plugins/{name}/restore"
          summary="Un-delete a plugin and re-materialize its git repo."
        >
          <template #errors>
            <ul class="dev-list">
              <li><code>400</code> — plugin is not deleted</li>
              <li><code>403</code> — not your plugin</li>
              <li><code>404</code> — plugin not found</li>
              <li><code>409</code> — an active plugin with that name already exists</li>
            </ul>
          </template>
        </Endpoint>
      </section>

      <!-- Skills -->
      <section id="rest-skills" class="card">
        <h2>Skill endpoints</h2>
        <p class="muted">
          Every skill write also bumps the parent plugin's semver and rewrites its git
          repository.
        </p>

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
          <template #notes>
            <ul class="dev-list">
              <li>The <strong>first</strong> skill added to a plugin doesn't bump the version (the plugin's initial version is its debut version), but it does refresh <code>updatedAt</code>.</li>
              <li>Subsequent skill creations bump the <strong>major</strong> version.</li>
            </ul>
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
          <template #notes>
            <p>
              The plugin version is bumped based on body size delta:
              a large change bumps the <strong>minor</strong>, a small one the <strong>patch</strong>.
            </p>
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
      </section>

      <!-- Skill files -->
      <section id="rest-files" class="card">
        <h2>Skill file endpoints</h2>
        <p>
          Skills can ship supporting files under three whitelisted top-level directories:
          <code>scripts/</code>, <code>references/</code>, and <code>assets/</code>.
        </p>

        <h3>Limits</h3>
        <ul class="dev-list">
          <li>Up to <strong>10 MB</strong> per file</li>
          <li>Up to <strong>100 MB</strong> total per skill</li>
          <li>Up to <strong>50</strong> files per skill</li>
          <li>Path segments must match <code>[A-Za-z0-9_.-]+</code>; max 6 segments deep, max 256 chars total</li>
        </ul>

        <Endpoint
          method="GET"
          path="/api/plugins/{name}/skills/{skill}/files"
          summary="List file metadata (no contents)."
        >
          <template #response>
<pre>[
  {
    "path":      "scripts/run.py",
    "isBinary":  false,
    "sizeBytes": 1234,
    "updatedAt": "2026-05-10T08:30:00Z"
  }
]</pre>
          </template>
        </Endpoint>

        <Endpoint
          method="GET"
          path="/api/plugins/{name}/skills/{skill}/files/{path...}"
          summary="Read one file's metadata and content."
        >
          <template #response>
<pre>{
  "path":      "scripts/run.py",
  "isBinary":  false,
  "sizeBytes": 1234,
  "content":   "raw UTF-8 text",      // base64 when isBinary is true
  "updatedAt": "2026-05-10T08:30:00Z"
}</pre>
          </template>
        </Endpoint>

        <Endpoint
          method="PUT"
          path="/api/plugins/{name}/skills/{skill}/files/{path...}"
          summary="Create or overwrite a file. Bumps the plugin patch version."
        >
          <template #request>
<pre>{
  "content":  "raw UTF-8 text or base64",
  "isBinary": false                    // optional; default false (text)
}</pre>
          </template>
          <template #notes>
            <p>
              When <code>isBinary</code> is <code>true</code> the server base64-decodes the
              content. When it's <code>false</code>, the bytes must be valid UTF-8 — otherwise
              the request is rejected and you should set <code>isBinary=true</code> and
              re-encode.
            </p>
          </template>
          <template #errors>
            <ul class="dev-list">
              <li><code>400</code> — invalid path, bad base64, non-UTF-8 text, or one of the size/count limits exceeded</li>
            </ul>
          </template>
        </Endpoint>

        <Endpoint
          method="DELETE"
          path="/api/plugins/{name}/skills/{skill}/files/{path...}"
          summary="Remove a single file. Bumps the plugin patch version."
        >
          <template #response>
            <p><code>204 No Content</code></p>
          </template>
        </Endpoint>
      </section>

      <!-- Validate -->
      <section id="rest-validate" class="card">
        <h2>Skill validator</h2>

        <Endpoint
          method="POST"
          path="/api/skills/validate"
          summary="Ask Claude to review a draft skill before you save it."
        >
          <template #request>
<pre>{
  "name":        "summarize-pr",
  "description": "...",
  "body":        "...",
  "files": [                           // optional, paths-only
    { "path": "scripts/run.py", "isBinary": false, "sizeBytes": 1234,
      "updatedAt": "2026-05-10T08:30:00Z" }
  ]
}</pre>
          </template>
          <template #response>
<pre>{
  "summary": "one short sentence verdict",
  "findings": [
    {
      "severity": "problem",     // problem | warning | info
      "title":    "Description is too vague",
      "detail":   "The description doesn't say WHEN to invoke the skill..."
    }
  ],
  "suggestedDescription": "rewritten description, or empty"
}</pre>
          </template>
          <template #notes>
            <p>
              Requires the server to have <code>ANTHROPIC_API_KEY</code> configured. This is
              the same validator the editor UI runs.
            </p>
          </template>
          <template #errors>
            <ul class="dev-list">
              <li><code>400</code> — neither description nor body provided</li>
              <li><code>502</code> — Claude API call failed or returned unparseable output</li>
            </ul>
          </template>
        </Endpoint>
      </section>

      <!-- Marketplace feed -->
      <section id="marketplace" class="card">
        <h2>Marketplace feed</h2>

        <Endpoint
          method="GET"
          path="/marketplace.json"
          summary="The machine-readable plugin index Claude Code consumes."
        >
          <template #notes>
            <p>
              Authenticated like every other endpoint, but it accepts both
              <code>Bearer</code> and HTTP Basic so <code>git</code> and <code>curl</code>
              can both fetch it. Each plugin's <code>source.url</code> embeds <em>your</em>
              API token as <code>https://_:&lt;token&gt;@host/git/&lt;name&gt;.git</code>,
              so the URL works as-is for cloning.
            </p>
          </template>
          <template #response>
<pre>{
  "name":  "oglimmer-marketplace",
  "owner": { "name": "...", "url": "{{ origin }}" },
  "plugins": [
    {
      "name":        "my-plugin",
      "description": "...",
      "version":     "1.2.0",
      "author":      { "name": "Alice", "email": "alice@example.com" },
      "homepage":    "https://...",
      "license":     "MIT",
      "source":      {
        "source": "url",
        "url":    "{{ authedOrigin }}/git/my-plugin.git"
      }
    }
  ]
}</pre>
          </template>
          <template #example>
<pre># Add the marketplace to Claude Code:
/plugin marketplace add {{ authedOrigin }}/marketplace.json</pre>
          </template>
        </Endpoint>
      </section>

      <!-- Git -->
      <section id="git" class="card">
        <h2>Git access</h2>
        <p>
          Every plugin is a real bare git repository, served via Smart HTTP under
          <code>/git/&lt;name&gt;.git</code>. Standard git tooling Just Works.
        </p>

        <pre>git clone https://_:{{ exampleToken }}@{{ hostNoScheme }}/git/my-plugin.git</pre>

        <h3>Repository layout</h3>
        <p>On every skill change the server rewrites the working tree to:</p>
<pre>my-plugin/
├── plugin.json                # name, version, author, license, ...
└── skills/
    └── &lt;skill-name&gt;/
        ├── SKILL.md            # YAML frontmatter (name, description) + body
        ├── scripts/...         # if any supporting files
        ├── references/...
        └── assets/...</pre>
        <p class="muted">
          History is squashed: the bare repo is regenerated from the database on every
          change. Don't push to it — the server rewrites <code>main</code> on the next
          skill update.
        </p>
      </section>

      <!-- MCP -->
      <section id="mcp" class="card">
        <h2>MCP server</h2>
        <p>
          The server speaks the
          <a href="https://modelcontextprotocol.io" target="_blank" rel="noopener">Model Context Protocol</a>
          over Streamable HTTP at <code>/mcp</code>. Claude (or any MCP client) can use it
          to read plugins and create / modify skills as tool calls. Plugins themselves are
          read-only over MCP — nothing can be deleted from this surface.
        </p>

        <h3>Connect from Claude Code</h3>
<pre>claude mcp add --transport http skill-host {{ origin }}/mcp \
  -H "Authorization: Bearer {{ exampleToken }}"</pre>

        <h3>JSON config (Claude Desktop and other MCP clients)</h3>
<pre>{
  "mcpServers": {
    "skill-host": {
      "type": "http",
      "url":  "{{ origin }}/mcp",
      "headers": { "Authorization": "Bearer {{ exampleToken }}" }
    }
  }
}</pre>

        <h3>Tools</h3>
        <table class="dev-table">
          <thead>
            <tr><th>Tool</th><th>Description</th></tr>
          </thead>
          <tbody>
            <tr>
              <td><code>list_plugins</code></td>
              <td>List all active plugins (name, description, version, owner, updatedAt).</td>
            </tr>
            <tr>
              <td><code>get_plugin</code></td>
              <td>Read a plugin's metadata and the list of its skills (names + descriptions, no bodies).</td>
            </tr>
            <tr>
              <td><code>get_skill</code></td>
              <td>Read a skill's description, SKILL.md body, and the list of its supporting files.</td>
            </tr>
            <tr>
              <td><code>create_skill</code></td>
              <td>Add a new skill to a plugin. Bumps the plugin version and rewrites the git repo.</td>
            </tr>
            <tr>
              <td><code>update_skill</code></td>
              <td>Replace a skill's description and body. Bumps the plugin version.</td>
            </tr>
            <tr>
              <td><code>list_skill_files</code></td>
              <td>List supporting files attached to a skill (paths + sizes, no content).</td>
            </tr>
            <tr>
              <td><code>get_skill_file</code></td>
              <td>Read one supporting file. Binary files are returned as base64 (<code>isBinary=true</code>).</td>
            </tr>
            <tr>
              <td><code>upsert_skill_file</code></td>
              <td>Write a supporting file under <code>scripts/</code>, <code>references/</code>, or <code>assets/</code>. Bumps the plugin patch version.</td>
            </tr>
          </tbody>
        </table>
      </section>

      <!-- Errors -->
      <section id="errors" class="card">
        <h2>Errors</h2>
        <p>
          Errors are returned with an appropriate HTTP status and a JSON body of the form:
        </p>
<pre>{ "error": "human-readable message" }</pre>

        <table class="dev-table">
          <thead>
            <tr><th>Status</th><th>When</th></tr>
          </thead>
          <tbody>
            <tr><td><code>400</code></td><td>Malformed JSON, invalid slug, body too large, or an explicit validation rule failed.</td></tr>
            <tr><td><code>401</code></td><td>Missing or invalid credential. The marketplace and git endpoints add a <code>WWW-Authenticate</code> header so <code>git</code> and <code>curl</code> prompt for credentials.</td></tr>
            <tr><td><code>403</code></td><td>Authenticated, but the resource belongs to another user.</td></tr>
            <tr><td><code>404</code></td><td>Plugin, skill, file, or version doesn't exist (or is soft-deleted on a read path).</td></tr>
            <tr><td><code>409</code></td><td>Unique-key violation: a plugin or skill with that name already exists.</td></tr>
            <tr><td><code>500</code></td><td>Database or server error. Check server logs.</td></tr>
            <tr><td><code>502</code></td><td>An upstream call (Claude API) failed.</td></tr>
          </tbody>
        </table>
      </section>
    </article>
  </div>
</template>

<style scoped>
.dev-layout {
  display: grid;
  grid-template-columns: 220px minmax(0, 1fr);
  gap: 48px;
  align-items: start;
}

.dev-toc {
  position: sticky;
  top: 92px;
  align-self: start;
  border-left: 1px solid var(--border-soft);
  padding-left: 18px;
}
.dev-toc-title { margin: 0 0 12px; }
.dev-toc nav {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.dev-toc nav a {
  font-family: var(--mono);
  font-size: 11px;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--text-soft);
  padding: 4px 0;
  border-left: 2px solid transparent;
  padding-left: 10px;
  margin-left: -12px;
  transition: color 0.2s ease, border-color 0.2s ease;
}
.dev-toc nav a:hover {
  color: var(--accent);
  border-left-color: var(--accent);
}

.dev-content { min-width: 0; }

.dev-header { margin-bottom: 28px; }
.dev-header .kicker {
  margin: 0 0 8px;
  font-family: var(--mono);
  font-size: 11px;
  letter-spacing: 0.26em;
  text-transform: uppercase;
  color: var(--accent);
}
.dev-header h1 { margin: 0 0 14px; }
.dev-header .lede {
  font-family: var(--serif);
  font-size: 18px;
  line-height: 1.5;
  color: var(--text-soft);
  max-width: 60ch;
  margin: 0;
}

.dev-list {
  margin: 8px 0 14px;
  padding-left: 22px;
  color: var(--text-soft);
}
.dev-list li { margin: 4px 0; }
.dev-list strong { color: var(--text); }

.dev-table {
  width: 100%;
  border-collapse: collapse;
  margin-top: 12px;
}
.dev-table th, .dev-table td {
  text-align: left;
  vertical-align: top;
}

/* Endpoint blocks */
.endpoint {
  border-top: 1px solid var(--border-soft);
  padding: 22px 0 6px;
  margin: 4px 0 0;
  scroll-margin-top: 100px;
}
.endpoint:first-of-type { border-top: 0; padding-top: 6px; }

.endpoint-head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 6px;
}
.endpoint-method {
  display: inline-block;
  padding: 3px 9px;
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.16em;
  text-transform: uppercase;
  color: var(--bg);
  background: var(--text);
  border-radius: 0;
}
.endpoint-method.method-get    { background: #5ea0ff; }
.endpoint-method.method-post   { background: var(--success); color: var(--bg); }
.endpoint-method.method-put    { background: var(--accent); }
.endpoint-method.method-delete { background: var(--rust); color: var(--text); }

.endpoint-path {
  font-family: var(--mono);
  font-size: 14px;
  color: var(--text);
  background: var(--bg-2);
  border: 1px solid var(--border-soft);
  padding: 4px 10px;
  word-break: break-all;
}

.endpoint-auth {
  font-family: var(--mono);
  font-size: 10px;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--muted);
  border: 1px solid var(--border);
  padding: 2px 8px;
  border-radius: 999px;
}
.endpoint-auth--public {
  color: var(--success);
  border-color: rgba(95, 255, 143, 0.4);
}

.endpoint-summary {
  margin: 6px 0 12px;
  color: var(--text-soft);
  font-size: 13.5px;
}

.endpoint-block { margin: 14px 0; }
.endpoint-block h4 {
  margin: 0 0 8px;
  font-family: var(--mono);
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--accent-2);
}
.endpoint-notes h4 { display: none; }
.endpoint-notes p { color: var(--text-soft); margin: 6px 0; }
.endpoint-notes p:first-child { margin-top: 0; }

.param-row {
  border-left: 2px solid var(--border);
  padding: 6px 0 6px 12px;
  margin: 8px 0;
}
.param-head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.param-name { background: var(--bg-2); }
.param-type {
  font-family: var(--mono);
  font-size: 11px;
  color: var(--muted);
  letter-spacing: 0.04em;
}
.param-required {
  font-family: var(--mono);
  font-size: 10px;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--rust);
  border: 1px solid rgba(214, 90, 49, 0.45);
  padding: 1px 6px;
  border-radius: 999px;
}
.param-desc {
  margin-top: 4px;
  color: var(--text-soft);
  font-size: 13px;
}

@media (max-width: 900px) {
  .dev-layout { grid-template-columns: 1fr; gap: 24px; }
  .dev-toc {
    position: static;
    border-left: 0;
    border-bottom: 1px solid var(--border-soft);
    padding: 0 0 14px;
  }
  .dev-toc nav { flex-direction: row; flex-wrap: wrap; gap: 8px 16px; }
  .dev-toc nav a { padding-left: 0; margin-left: 0; border-left: 0; }
}
</style>
