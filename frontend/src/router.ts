import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from './stores/auth'

import PluginListView from './views/PluginListView.vue'
import PluginDetailView from './views/PluginDetailView.vue'
import NewPluginView from './views/NewPluginView.vue'
import SkillEditView from './views/SkillEditView.vue'
import LoginView from './views/LoginView.vue'
import RegisterView from './views/RegisterView.vue'
import OIDCCallbackView from './views/OIDCCallbackView.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: PluginListView, meta: { requiresAuth: true } },
    { path: '/login', component: LoginView },
    { path: '/register', component: RegisterView },
    { path: '/auth/callback', component: OIDCCallbackView },
    { path: '/plugins/new', component: NewPluginView, meta: { requiresAuth: true } },
    { path: '/plugins/:name', component: PluginDetailView, props: true, meta: { requiresAuth: true } },
    {
      path: '/plugins/:name/skills/new',
      component: SkillEditView,
      props: route => ({ pluginName: route.params.name, skillName: null }),
      meta: { requiresAuth: true },
    },
    {
      path: '/plugins/:name/skills/:skillName/edit',
      component: SkillEditView,
      props: route => ({
        pluginName: route.params.name,
        skillName: route.params.skillName,
      }),
      meta: { requiresAuth: true },
    },
  ],
})

router.beforeEach((to) => {
  if (to.meta.requiresAuth) {
    const auth = useAuthStore()
    if (!auth.token) {
      return { path: '/login', query: { redirect: to.fullPath } }
    }
  }
})
