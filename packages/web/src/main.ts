import { createApp } from 'vue'

import App from './App.vue'
import { setUnauthorizedHandler } from './api/client'
import { router } from './router'
import './style.css'

setUnauthorizedHandler(() => {
  if (router.currentRoute.value.path !== '/login') {
    void router.push('/login')
  }
})

createApp(App).use(router).mount('#app')
