<script setup lang="ts">
import Endpoint from '../Endpoint.vue'
</script>

<template>
  <div class="dev-subsection">
    <header class="section-head">
      <h2>Skill file endpoints</h2>
      <p class="section-lede">
        Skills can ship supporting files under any top-level folder whose name passes the
        segment rules below. <code>scripts/</code>, <code>references/</code>, and
        <code>assets/</code> are the conventional Anthropic-skill folders that the UI
        surfaces by default, but the API accepts arbitrary folder names.
      </p>
    </header>

    <div class="limits">
      <h4>Limits</h4>
      <ul class="limits-grid">
        <li><strong>10 MB</strong><span>per file</span></li>
        <li><strong>100 MB</strong><span>per skill (total)</span></li>
        <li><strong>50</strong><span>files per skill</span></li>
        <li><strong>6 / 256</strong><span>max path segments / chars</span></li>
      </ul>
      <p class="muted">
        Path segments must match <code>[A-Za-z0-9_.-]+</code>. No <code>..</code>,
        leading slashes, or double-slashes.
      </p>
    </div>

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
  </div>
</template>

<style scoped>
.limits {
  border: 1px solid var(--border-soft);
  background: var(--bg-2);
  padding: 14px 16px;
  margin: 8px 0 22px;
}
.limits h4 {
  margin: 0 0 10px;
  font-family: var(--mono);
  font-size: 11px;
  letter-spacing: 0.2em;
  text-transform: uppercase;
  color: var(--accent-2);
}
.limits-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 12px;
  margin: 0 0 10px;
  padding: 0;
  list-style: none;
}
.limits-grid li {
  display: flex;
  flex-direction: column;
  border-left: 2px solid var(--accent);
  padding-left: 10px;
}
.limits-grid strong { font-size: 18px; color: var(--text); }
.limits-grid span { font-size: 12px; color: var(--text-soft); }
.limits .muted { margin: 0; font-size: 12px; }
</style>
