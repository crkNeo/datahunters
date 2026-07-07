import { createApp } from 'vue'
import App from './App.vue'

const app = createApp(App)

// White-screen safety net: a render/setup exception can leave Vue's DOM blank
// with no way to recover on its own. Log it (so the cause is visible in the
// console) and auto-reload ONCE to rebuild a clean state. The sessionStorage
// marker guards against a reload loop — a persistent error reloads at most once
// per 10s, so it degrades to "broken but not looping" instead of thrashing.
app.config.errorHandler = (err, instance, info) => {
  console.error('[vue error]', info, err)
  // only UI-fatal phases blank the page; event-handler/watcher errors don't, so
  // don't disrupt the user by reloading for those.
  if (!/render|setup|scheduler|mount/i.test(info || '')) return
  try {
    const now = Date.now()
    const last = +sessionStorage.getItem('lastAutoReload') || 0
    if (now - last > 10000) {
      sessionStorage.setItem('lastAutoReload', String(now))
      location.reload()
    }
  } catch (e) {
    /* sessionStorage blocked (private mode) → skip the auto-reload */
  }
}

app.mount('#app')
