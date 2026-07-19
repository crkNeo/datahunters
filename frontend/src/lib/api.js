// 全站 API 層。所有後端呼叫都走這裡,好處是 token 與 401 只在一個地方處理。
//
// 為什麼還留著 authFetch:全站有 60+ 個呼叫點是照 fetch 的介面寫的
// (res.ok / res.json() / res.text() / res.blob())。一次全部改寫成 axios 風格
// 風險太高,所以這裡用 axios 實作出一個 fetch 相容層 —— 呼叫點不用動就先享受到
// 統一的 token 注入與錯誤處理,之後拆元件時再逐檔改成 api.get/api.post。
import axios from 'axios'
import { ref } from 'vue'

// 登入憑證。App.vue 與各元件共用這一份,不再各自持有。
export const token = ref(localStorage.getItem('token') || '')

export function setToken(v) {
  token.value = v || ''
  if (v) localStorage.setItem('token', v)
  else localStorage.removeItem('token')
}

// 401 的統一處理由外部注入(App.vue 需要清狀態 + 提示),避免這層反過來依賴 UI。
let onUnauthorized = null
export function setUnauthorizedHandler(fn) {
  onUnauthorized = fn
}

export const api = axios.create({
  timeout: 20000,
  // 不讓 axios 因為非 2xx 就丟例外:這裡要模擬 fetch 的 res.ok 語意,
  // 由呼叫端自行判斷狀態碼。
  validateStatus: () => true,
})

api.interceptors.request.use((cfg) => {
  if (token.value) cfg.headers.Authorization = 'Bearer ' + token.value
  return cfg
})

api.interceptors.response.use((res) => {
  // 帶著 token 卻被打回 401/403 → 憑證失效或權限被調降,交給外層決定怎麼處理。
  if (token.value && (res.status === 401 || res.status === 403) && onUnauthorized) {
    onUnauthorized(res.status, res.config && res.config.url)
  }
  return res
})

// fetch 相容層:回傳一個有 ok / status / json() / text() / blob() 的物件。
// 用 arraybuffer 收回應,三種讀法都能從同一份資料衍生,不必事先知道呼叫端要什麼。
export async function authFetch(url, opts = {}) {
  const method = (opts.method || 'GET').toLowerCase()
  const headers = { ...(opts.headers || {}) }
  let res
  try {
    res = await api.request({
      url,
      method,
      headers,
      data: opts.body,
      responseType: 'arraybuffer',
    })
  } catch (e) {
    // 連線層面的失敗(斷網/逾時)—— 照 fetch 的行為往外丟
    throw e
  }
  const buf = res.data
  const text = () => new TextDecoder('utf-8').decode(buf || new ArrayBuffer(0))
  return {
    ok: res.status >= 200 && res.status < 300,
    status: res.status,
    text: async () => text(),
    json: async () => JSON.parse(text() || 'null'),
    blob: async () => new Blob([buf]),
  }
}
