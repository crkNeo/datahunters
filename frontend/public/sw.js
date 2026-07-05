// JMCH service worker — installability + Web Push.
self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()))

// A passthrough fetch handler (required for PWA installability on some browsers).
self.addEventListener('fetch', () => {})

// Web Push: render an OS notification.
self.addEventListener('push', (e) => {
  let data = { title: 'JMCH', body: '', url: '/' }
  try { data = { ...data, ...(e.data ? e.data.json() : {}) } } catch (err) {}
  e.waitUntil(
    self.registration.showNotification(data.title, {
      body: data.body,
      icon: '/icon-192.png',
      badge: '/icon-192.png',
      data: { url: data.url },
      vibrate: [80, 40, 80],
    }),
  )
})

self.addEventListener('notificationclick', (e) => {
  e.notification.close()
  const url = (e.notification.data && e.notification.data.url) || '/'
  let tab = ''
  try { tab = new URL(url, self.location.origin).searchParams.get('tab') || '' } catch (err) {}
  e.waitUntil(
    clients.matchAll({ type: 'window', includeUncontrolled: true }).then((list) => {
      for (const c of list) {
        if ('focus' in c) {
          // app already open: focus it and tell the SPA which tab to switch to
          if (tab) c.postMessage({ type: 'nav', tab })
          return c.focus()
        }
      }
      return clients.openWindow(url) // app closed: open with ?tab= so it deep-links
    }),
  )
})
