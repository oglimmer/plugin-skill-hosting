<script setup lang="ts">
import { useApiExamples } from '../useApiExamples'
const { exampleToken, hostNoScheme } = useApiExamples()
</script>

<template>
  <section class="dev-section">
    <header class="section-head">
      <h2>Git access</h2>
      <p class="section-lede">
        Every plugin is a real bare git repository served via Smart HTTP under
        <code>/git/&lt;name&gt;.git</code>. Standard git tooling Just Works.
      </p>
    </header>

    <h3>Clone</h3>
    <pre>git clone https://_:{{ exampleToken }}@{{ hostNoScheme }}/git/my-plugin.git</pre>

    <h3>Repository layout</h3>
    <p>On every skill or agent change the server rewrites the working tree to:</p>
<pre>my-plugin/
├── plugin.json                # name, version, author, license, ...
├── skills/
│   └── &lt;skill-name&gt;/
│       ├── SKILL.md            # YAML frontmatter (name, description) + body
│       ├── scripts/...         # if any supporting files
│       ├── references/...
│       └── assets/...
└── agents/                    # only when the plugin has agents
    └── &lt;agent-name&gt;.md        # YAML frontmatter (name, description, model, tools) + system prompt</pre>

    <aside class="warn">
      <h4>Read-only over Git</h4>
      <p>
        History is squashed: the bare repo is regenerated from the database on every
        change. Don't push to it — the server rewrites <code>main</code> on the next
        skill update. Use the REST or MCP surfaces to write.
      </p>
    </aside>
  </section>
</template>

<style scoped>
.warn {
  border-left: 3px solid var(--rust, #d65a31);
  background: rgb(var(--rust-rgb) / 0.08);
  padding: 12px 16px;
  margin: 16px 0 0;
}
.warn h4 {
  margin: 0 0 4px;
  font-family: var(--mono);
  font-size: 11px;
  letter-spacing: 0.2em;
  text-transform: uppercase;
  color: var(--rust, #d65a31);
}
.warn p { margin: 0; color: var(--text-soft); }
</style>
