<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, RouterLink } from 'vue-router'

type Crumb = { label: string; to?: string }

const route = useRoute()

const HOME: Crumb = { label: 'Plugins', to: '/' }

const trail = computed<Crumb[]>(() => {
  const path = route.path
  const params = route.params as Record<string, string>

  // Pending users are confined to /pending; navigation links would 404 for them.
  if (path === '/pending') return [{ label: 'Pending' }]

  if (path === '/') return [{ label: HOME.label }]
  if (path === '/users') return [HOME, { label: 'Users' }]
  if (path === '/audit') return [HOME, { label: 'Security audit' }]
  if (path === '/developers') return [HOME, { label: 'Developers' }]
  if (path === '/register') return [HOME, { label: 'Sign up' }]
  if (path === '/plugins/new') return [HOME, { label: 'New plugin' }]

  if (params.name) {
    const pluginCrumb: Crumb = { label: params.name, to: `/plugins/${params.name}` }
    if (params.skillName) {
      if (path.endsWith('/compare')) {
        return [
          HOME,
          pluginCrumb,
          { label: params.skillName, to: `/plugins/${params.name}/skills/${params.skillName}/edit` },
          { label: 'Compare versions' },
        ]
      }
      return [HOME, pluginCrumb, { label: params.skillName }]
    }
    if (path.endsWith('/skills/new')) {
      return [HOME, pluginCrumb, { label: 'New skill' }]
    }
    return [HOME, { label: params.name }]
  }

  return [HOME]
})
</script>

<template>
  <nav v-if="trail.length" class="breadcrumbs" aria-label="Breadcrumb">
    <ol>
      <li
        v-for="(crumb, i) in trail"
        :key="i"
        :class="{ current: i === trail.length - 1, home: i === 0 }"
      >
        <RouterLink v-if="crumb.to && i !== trail.length - 1" :to="crumb.to">
          {{ crumb.label }}
        </RouterLink>
        <span v-else aria-current="page">{{ crumb.label }}</span>
      </li>
    </ol>
  </nav>
</template>

<style scoped>
.breadcrumbs {
  /* Lives inside nav.top — no own backdrop, padding, or sticky positioning. */
  min-width: 0;
  flex: 1 1 auto;
}
.breadcrumbs ol {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  font-family: var(--mono);
  font-size: 12.5px;
  font-weight: 600;
  letter-spacing: 0.22em;
  text-transform: uppercase;
}
.breadcrumbs li {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  color: var(--muted);
  min-width: 0;
}
.breadcrumbs li + li::before {
  content: '/';
  color: var(--accent);
  opacity: 0.8;
  font-weight: 400;
}
.breadcrumbs a {
  color: var(--text);
  border-bottom: 1px solid transparent;
  padding: 2px 0;
  transition: color 0.2s ease, border-color 0.2s ease;
}
.breadcrumbs a:hover {
  color: var(--accent);
  border-bottom-color: var(--accent);
}
.breadcrumbs li.current span {
  color: var(--text);
  text-transform: none;
  letter-spacing: 0.02em;
  font-weight: 500;
  font-size: 13px;
  /* Truncate very long skill/plugin names rather than wrapping. */
  max-width: 48ch;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* The home crumb always reads as a brand-like anchor, even when it's
   the current page on `/`. Overrides the readable leaf styling above. */
.breadcrumbs li.home.current span {
  color: var(--text);
  text-transform: uppercase;
  letter-spacing: 0.22em;
  font-weight: 600;
  font-size: 12.5px;
  max-width: none;
}

@media (max-width: 720px) {
  .breadcrumbs ol { gap: 4px; font-size: 11px; letter-spacing: 0.18em; }
  .breadcrumbs li.current span { max-width: 22ch; font-size: 12px; }
  .breadcrumbs li.home.current span { font-size: 11px; letter-spacing: 0.18em; }
}
</style>
