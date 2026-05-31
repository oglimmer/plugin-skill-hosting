<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { RouterLink } from 'vue-router'
import { useBuildInfo } from '../composables/useBuildInfo'

const { frontend, backend, load } = useBuildInfo()

onMounted(() => { load() })

const year = new Date().getFullYear()

function shortCommit(commit: string): string {
  return commit && commit !== 'unknown' ? commit.slice(0, 7) : commit
}

const frontendLine = computed(() =>
  `v${frontend.version} · ${shortCommit(frontend.gitCommit)} · ${frontend.buildTime}`,
)
const backendLine = computed(() => {
  if (!backend.value) return '…'
  return `v${backend.value.version} · ${shortCommit(backend.value.gitCommit)} · ${backend.value.buildTime}`
})
</script>

<template>
  <footer class="site-footer">
    <div class="footer-inner">
      <div class="footer-meta">
        <p class="copyright">
          © {{ year }} Oli Zimpasser ·
          <a href="https://github.com/oglimmer/plugin-skill-hosting/blob/master/LICENSE"
             target="_blank" rel="noopener">MIT License</a>
        </p>
        <nav class="footer-links">
          <RouterLink to="/developers">
            Developer Portal
            <svg class="link-icon" viewBox="0 0 16 16" aria-hidden="true" focusable="false">
              <path d="M6 3h7v7" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
              <path d="M13 3 6.5 9.5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
              <path d="M11 9v3.5A1.5 1.5 0 0 1 9.5 14h-6A1.5 1.5 0 0 1 2 12.5v-6A1.5 1.5 0 0 1 3.5 5H7" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
          </RouterLink>
          <a href="https://github.com/oglimmer/plugin-skill-hosting" target="_blank" rel="noopener">
            <svg class="link-icon" viewBox="0 0 16 16" aria-hidden="true" focusable="false">
              <path fill="currentColor" fill-rule="evenodd" d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0 0 16 8c0-4.42-3.58-8-8-8z" clip-rule="evenodd"/>
            </svg>
            GitHub
          </a>
        </nav>
      </div>
      <dl class="footer-versions" aria-label="Build information">
        <div class="version-row">
          <dt>Frontend</dt>
          <dd>{{ frontendLine }}</dd>
        </div>
        <div class="version-row">
          <dt>Backend</dt>
          <dd>{{ backendLine }}</dd>
        </div>
      </dl>
    </div>
  </footer>
</template>

<style scoped>
.site-footer {
  margin-top: 64px;
  padding: 28px 32px 36px;
  border-top: 1px solid var(--border);
  background: var(--bg-2);
  color: var(--text-soft);
  font-family: var(--mono);
  font-size: 11px;
  letter-spacing: 0.04em;
}

.footer-inner {
  max-width: 1080px;
  margin: 0 auto;
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 32px;
  flex-wrap: wrap;
}

.footer-meta {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-width: 0;
}
.copyright {
  margin: 0;
  font-size: 11.5px;
  color: var(--text-soft);
}
.copyright a {
  color: var(--text-soft);
  border-bottom: 1px solid var(--border);
  transition: color 0.2s ease, border-color 0.2s ease;
}
.copyright a:hover { color: var(--accent); border-bottom-color: var(--accent); }

.footer-links {
  display: flex;
  gap: 18px;
}
.footer-links a {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--text-soft);
  border-bottom: 1px solid transparent;
  padding-bottom: 2px;
  transition: color 0.2s ease, border-color 0.2s ease;
}
.footer-links .link-icon {
  width: 11px;
  height: 11px;
  flex-shrink: 0;
}
.footer-links a:hover {
  color: var(--text);
  border-bottom-color: var(--accent);
}

.footer-versions {
  margin: 0;
  display: grid;
  grid-template-columns: auto;
  gap: 4px;
  text-align: right;
  font-size: 10.5px;
  color: var(--muted);
  letter-spacing: 0.04em;
}
.version-row {
  display: flex;
  gap: 10px;
  justify-content: flex-end;
  align-items: baseline;
}
.version-row dt {
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--text-soft);
  font-size: 10px;
  min-width: 64px;
  text-align: right;
}
.version-row dd {
  margin: 0;
  font-family: var(--mono);
  color: var(--muted);
  word-break: break-all;
}

@media (max-width: 720px) {
  .site-footer { padding: 24px 18px 32px; }
  .footer-inner { flex-direction: column; gap: 18px; }
  .footer-versions { text-align: left; }
  .version-row { justify-content: flex-start; }
  .version-row dt { text-align: left; min-width: 0; }
}
</style>
