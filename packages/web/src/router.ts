import { createRouter, createWebHistory } from 'vue-router'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/overview' },
    { path: '/login', component: () => import('./pages/login.vue') },
    { path: '/overview', component: () => import('./pages/overview.vue') },
    { path: '/connectors', component: () => import('./pages/connectors.vue') },
    { path: '/connectors/:type', component: () => import('./pages/connector-config.vue') },
    { path: '/connections', component: () => import('./pages/connections.vue') },
    { path: '/tokens', component: () => import('./pages/tokens.vue') },
    { path: '/settings', component: () => import('./pages/settings.vue') },
  ],
})
