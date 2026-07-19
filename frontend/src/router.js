// SPA 路由。
//
// 這裡刻意「只管網址、不管畫面」:內容仍由 App.vue 依 mainTab 渲染,router 負責把
// mainTab 同步成真實網址(/conv、/gamble…),換來可分享的連結、瀏覽器上一頁、以及
// 重新整理能回到同一頁(後端 withStatic 已有 SPA fallback)。
//
// 為什麼不直接把每個分頁做成 route component:那需要先把 27 個 v-if 區塊全部拆成
// 元件,一次做完無法驗證。等元件逐塊拆出來後,再把 Blank 換成真正的頁面即可,
// 網址與權限守衛這層不用重做。
import { createRouter, createWebHistory } from 'vue-router'

// 內容由 App.vue 自己渲染,route 只承載網址狀態
const Blank = { render: () => null }

// 可路由的分頁(與 App.vue 的 NAV_TABS 一致)
export const ROUTE_TABS = [
  'ranking', 'list', 'events', 'flow', 'upbit', 'news', 'funding', 'unlock',
  'sectors', 'robinhood', 'articles',
  'oi', 'signals', 'scorelog', 'radar',
  'paper', 'gamble', 'emaonly', 'conv', 'sr',
  'bollfade', 'meanrev', 'bgv2', 'bollema',
  'admin', 'referral',
]

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'home', component: Blank },
    { path: '/:tab', name: 'tab', component: Blank },
    // 其餘路徑(例如舊的深層連結)一律回首頁,不要留下死頁
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
  scrollBehavior() {
    return { top: 0 }
  },
})

// 舊格式相容:推播通知送出的連結是 /?tab=conv(後端 paper.go 寫死的格式),
// 已經發出去的通知不能失效,所以在這裡轉成新的路徑式網址。
router.beforeEach((to) => {
  const q = to.query.tab
  if (typeof q === 'string' && ROUTE_TABS.includes(q)) {
    const rest = { ...to.query }
    delete rest.tab
    return { path: '/' + q, query: rest, replace: true }
  }
  return true
})
