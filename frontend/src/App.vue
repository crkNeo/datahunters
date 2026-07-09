<script setup>
import { ref, onMounted, onUnmounted, computed, watch } from 'vue'

// ---- shared data ----
const home = ref(null)
const board = ref({})
const boardUpdated = ref('')
const error = ref('')
let timer = null

const mainTab = ref('ranking')
const marketSort = ref('vol') // vol | gainers | losers

// ---- auth (public web build) ----
const token = ref(localStorage.getItem('token') || '')
const role = ref('public')
const username = ref('')
const loginOpen = ref(false)
const loginForm = ref({ u: '', p: '' })
const loginErr = ref('')
const status = ref('')
const authReady = ref(false)
const authMsg = ref('')
const authTab = ref('login')
const regForm = ref({ u: '', p: '', uid: '', email: '', exchange: '' })
const regFile = ref(null)
const regErr = ref('')
const regDone = ref('')
const roleRank = { public: 0, member: 1, vip: 2, admin: 3 }
const authed = computed(() => role.value !== 'public' && status.value === 'active')
function can(min) {
  return (roleRank[role.value] || 0) >= (roleRank[min] || 0)
}
function clearAuth(msg) {
  token.value = ''
  localStorage.removeItem('token')
  role.value = 'public'
  status.value = ''
  username.value = ''
  authMsg.value = msg || ''
}
// ---- toast prompt ----
const toastMsg = ref('')
const toastType = ref('ok')
let toastTimer = null
function showToast(msg, type = 'ok') {
  toastMsg.value = msg
  toastType.value = type
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => { toastMsg.value = '' }, 3200)
}
// ---- account/password input rules (英數/特殊符號) ----
function sanitizeAcct(v) { return v.replace(/[^A-Za-z0-9]/g, '').slice(0, 16) }        // 帳號:只留英數
function sanitizePw(v) { return v.replace(/[^\x21-\x7e]/g, '').slice(0, 16) }          // 密碼:只留可見 ASCII(英數+特殊,無空白/中文)
function validAcct(v) { return /^[A-Za-z0-9]{4,16}$/.test(v) }
function validPw(v) {
  return v.length >= 4 && v.length <= 16 &&
    /[a-z]/.test(v) && /[A-Z]/.test(v) && /[0-9]/.test(v) &&
    /[^A-Za-z0-9]/.test(v) && /^[\x21-\x7e]+$/.test(v)
}
function authFetch(url, opts = {}) {
  const headers = { ...(opts.headers || {}) }
  if (token.value) headers.Authorization = 'Bearer ' + token.value
  return fetch(url, { ...opts, headers })
}
async function loadMe() {
  if (!token.value) {
    clearAuth('')
    authReady.value = true
    return
  }
  try {
    const res = await authFetch('/api/auth/me')
    // /api/auth/me ALWAYS answers 200 with a JSON role (public for an invalid
    // token). So a non-200 here is a transient server/proxy/network hiccup —
    // common right when a PWA/tab returns to the foreground — NOT a real logout.
    // Treat it as a hiccup and keep the current session, or a stray 502 on resume
    // would wipe the role and blank whatever gated tab the user was on.
    if (!res.ok) {
      authReady.value = true
      return
    }
    const d = await res.json()
    if (!d.role || d.role === 'public') {
      // no idle timeout now — public means the account was removed or the
      // signing secret was rotated, so the stored token is no longer valid.
      clearAuth('登入已失效,請重新登入')
      authTab.value = 'login'
    } else if (d.status !== 'active') {
      clearAuth(d.status === 'pending' ? '帳號審核中,請待管理員通過' : '帳號已停用,請聯繫管理員')
      authTab.value = 'login'
    } else {
      role.value = d.role
      status.value = 'active'
      username.value = d.username || ''
      authMsg.value = ''
    }
  } catch (e) {
    /* network hiccup: keep current state */
  }
  authReady.value = true
}
async function doLogin() {
  loginErr.value = ''
  if (!loginForm.value.u || !loginForm.value.p) {
    loginErr.value = '請輸入帳號與密碼'
    showToast(loginErr.value, 'err')
    return
  }
  try {
    const res = await authFetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: loginForm.value.u, password: loginForm.value.p }),
    })
    if (!res.ok) {
      loginErr.value = (await res.text()).trim() || '帳號或密碼錯誤'
      showToast(loginErr.value, 'err')
      return
    }
    const d = await res.json()
    token.value = d.token
    localStorage.setItem('token', d.token)
    role.value = d.role
    status.value = 'active'
    username.value = d.username
    authMsg.value = ''
    loginOpen.value = false
    loginForm.value = { u: '', p: '' }
    showToast('登入成功,歡迎回來!', 'ok')
    welcomeOpen.value = true // 使用須知 modal(續用資格 / 加入主畫面 / 訊號提醒)
    loadAll()
  } catch (e) {
    loginErr.value = '登入失敗'
    showToast('登入失敗,請稍後再試', 'err')
  }
}
const welcomeOpen = ref(false)
function onRegFile(e) {
  const f = (e.target.files && e.target.files[0]) || null
  if (f && !validImage(f)) { e.target.value = ''; regFile.value = null; return }
  regFile.value = f
}
async function doRegister() {
  regErr.value = ''
  regDone.value = ''
  if (!validAcct(regForm.value.u)) {
    regErr.value = '帳號需 4–16 碼,僅限英文與數字'
    showToast(regErr.value, 'err')
    return
  }
  if (!validPw(regForm.value.p)) {
    regErr.value = '密碼需 4–16 碼,且同時含大寫、小寫、數字與特殊符號'
    showToast(regErr.value, 'err')
    return
  }
  if (!regForm.value.uid.trim()) {
    regErr.value = '請填寫 UID'
    showToast(regErr.value, 'err')
    return
  }
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(regForm.value.email.trim())) {
    regErr.value = '請填寫有效的 Email'
    showToast(regErr.value, 'err')
    return
  }
  const fd = new FormData()
  fd.append('username', regForm.value.u)
  fd.append('password', regForm.value.p)
  fd.append('uid', regForm.value.uid)
  // email goes into the notes(備註) field alongside the optional exchange name,
  // so the admin review card shows it without any backend schema change
  fd.append('exchange', [regForm.value.email.trim(), regForm.value.exchange.trim()].filter(Boolean).join(' · '))
  if (regFile.value) fd.append('proof', regFile.value)
  try {
    const res = await fetch('/api/auth/register', { method: 'POST', body: fd })
    if (!res.ok) {
      regErr.value = (await res.text()).trim() || '註冊失敗'
      showToast(regErr.value, 'err')
      return
    }
    regDone.value = '註冊成功!帳號審核中,待管理員通過後即可登入。'
    showToast('註冊成功!帳號審核中,待管理員通過', 'ok')
    regForm.value = { u: '', p: '', uid: '', email: '', exchange: '' }
    regFile.value = null
    authTab.value = 'login'
  } catch (e) {
    regErr.value = '註冊失敗'
    showToast('註冊失敗,請稍後再試', 'err')
  }
}
function logout() {
  clearAuth('')
  mainTab.value = 'ranking'
}
const ranking = ref(null)
async function loadRanking() {
  try {
    const res = await authFetch('/api/ranking')
    if (res.ok) ranking.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}

// ---- admin: user management ----
const users = ref([])
const adminMsg = ref('')
const newUser = ref({ u: '', p: '', role: 'member', status: 'active' })
async function loadUsers() {
  if (!can('admin')) return
  try {
    const res = await authFetch('/api/admin/users')
    if (res.ok) users.value = (await res.json()) || []
  } catch (e) {
    /* ignore */
  }
}
async function createUser() {
  adminMsg.value = ''
  if (!newUser.value.u || !newUser.value.p) {
    adminMsg.value = '帳號與密碼必填'
    return
  }
  const res = await authFetch('/api/admin/users', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      username: newUser.value.u,
      password: newUser.value.p,
      role: newUser.value.role,
      status: newUser.value.status,
    }),
  })
  if (res.ok) {
    adminMsg.value = '✓ 已新增 ' + newUser.value.u
    newUser.value = { u: '', p: '', role: 'member', status: 'active' }
    loadUsers()
  } else {
    adminMsg.value = '✗ ' + (await res.text())
  }
}
async function updateUser(u) {
  const res = await authFetch('/api/admin/users', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: u.username, role: u.role, status: u.status }),
  })
  adminMsg.value = res.ok ? '✓ 已更新 ' + u.username : '✗ 更新失敗'
  loadUsers()
}
const proofView = ref('')
const pendingUsers = computed(() => users.value.filter((u) => u.status === 'pending'))
// ---- admin user-management filters ----
const userRoleFilter = ref('all') // all | member | vip | admin
const userFrom = ref('')          // YYYY-MM-DD (registration >= this day, inclusive)
const userTo = ref('')            // YYYY-MM-DD (registration <= this day, inclusive)
const userSort = ref('new')       // new | old (by registration time)
function ymd(d) {
  const p = (x) => String(x).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}
// quick shortcut: fill the date range to the last n days (0 = clear both)
function setUserDays(n) {
  if (!n) { userFrom.value = ''; userTo.value = ''; return }
  userFrom.value = ymd(new Date(Date.now() - n * 86400000))
  userTo.value = ymd(new Date())
}
const filteredUsers = computed(() => {
  let list = users.value.slice()
  if (userRoleFilter.value !== 'all') list = list.filter((u) => u.role === userRoleFilter.value)
  if (userFrom.value) {
    const from = new Date(userFrom.value + 'T00:00:00').getTime()
    list = list.filter((u) => (u.created || 0) >= from)
  }
  if (userTo.value) {
    const to = new Date(userTo.value + 'T23:59:59').getTime()
    list = list.filter((u) => (u.created || 0) <= to)
  }
  list.sort((a, b) => (userSort.value === 'new' ? (b.created || 0) - (a.created || 0) : (a.created || 0) - (b.created || 0)))
  return list
})
function fmtReg(ms) {
  if (!ms) return '—'
  const d = new Date(ms)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}
async function approveUser(u) { u.status = 'active'; await updateUser(u) }
async function rejectUser(u) { u.status = 'banned'; await updateUser(u) }
async function toggleEnabled(u) { u.status = u.status === 'active' ? 'banned' : 'active'; await updateUser(u) }
async function toggleVip(u) {
  if (u.role === 'admin') return
  u.role = u.role === 'vip' ? 'member' : 'vip'
  await updateUser(u)
}
async function deleteUser(u) {
  if (u.username === username.value) return
  if (!confirm('確定刪除帳號「' + u.username + '」?此動作無法復原。')) return
  const res = await authFetch('/api/admin/users?username=' + encodeURIComponent(u.username), { method: 'DELETE' })
  adminMsg.value = res.ok ? '✓ 已刪除 ' + u.username : '✗ 刪除失敗'
  loadUsers()
}

// ---- articles (Feature 3) ----
const articles = ref([])
const articleView = ref(null) // open article detail
const artEdit = ref(null) // admin editor draft (null = closed)
async function loadArticles() {
  try {
    const res = await authFetch('/api/articles')
    if (res.ok) articles.value = (await res.json()) || []
  } catch (e) {
    /* secondary */
  }
}
// validImage: 3MB cap + common formats + iPhone photos (heic/heif). Shows a
// toast and returns false when rejected (server enforces the same rules).
const IMG_MAX = 3 * 1024 * 1024
const IMG_EXT = /\.(png|jpe?g|webp|gif|heic|heif)$/i
function validImage(f) {
  if (!f) return false
  if (!IMG_EXT.test(f.name)) {
    showToast('僅接受圖片檔(png / jpg / webp / gif / heic)', 'err')
    return false
  }
  if (f.size > IMG_MAX) {
    showToast('圖片過大,上限 3MB', 'err')
    return false
  }
  return true
}
async function uploadImage(file, sub) {
  if (!validImage(file)) return ''
  const fd = new FormData()
  fd.append('file', file)
  fd.append('sub', sub)
  const res = await authFetch('/api/admin/upload', { method: 'POST', body: fd })
  if (!res.ok) { showToast((await res.text()).trim() || '上傳失敗', 'err'); return '' }
  return (await res.json()).path
}
function newArticle() { artEdit.value = { id: 0, title: '', cover: '', tags: [], blocks: [{ type: 'text', text: '' }] } }
function editArticle(a) { artEdit.value = JSON.parse(JSON.stringify(a)); articleView.value = null }
function addBlock(type) { artEdit.value.blocks.push(type === 'image' ? { type: 'image', image: '' } : { type: 'text', text: '' }) }
function removeBlock(i) { artEdit.value.blocks.splice(i, 1) }
function moveBlock(i, d) {
  const b = artEdit.value.blocks, j = i + d
  if (j < 0 || j >= b.length) return
  ;[b[i], b[j]] = [b[j], b[i]]
}
async function onCoverPick(e) {
  const f = e.target.files && e.target.files[0]
  if (f) artEdit.value.cover = await uploadImage(f, 'articles')
}
async function onBlockImg(e, i) {
  const f = e.target.files && e.target.files[0]
  if (f) artEdit.value.blocks[i].image = await uploadImage(f, 'articles')
}
function setArtTags(v) { artEdit.value.tags = v.split(',').map((t) => t.trim()).filter(Boolean) }
async function saveArticle() {
  if (!artEdit.value.title.trim()) return
  const res = await authFetch('/api/admin/articles', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(artEdit.value),
  })
  if (res.ok) { artEdit.value = null; loadArticles() }
}
async function removeArticle(a) {
  if (!confirm('確定刪除「' + a.title + '」?')) return
  await authFetch('/api/admin/articles?id=' + a.id, { method: 'DELETE' })
  articleView.value = null
  loadArticles()
}
async function togglePin(a) {
  const pin = a.pinned ? 0 : 1
  const res = await authFetch('/api/admin/article-pin?id=' + a.id + '&pin=' + pin, { method: 'POST' })
  if (res.ok) { a.pinned = !a.pinned; loadArticles() }
}

// ---- site config: logo / social / QR (Feature 4) ----
const config = ref({})
const qrHidden = ref(false)
const socialMeta = {
  youtube: { icon: '▶', color: '#ff0000', name: 'YouTube' },
  telegram: { icon: '✈', color: '#229ed9', name: 'Telegram' },
  instagram: { icon: '◎', color: '#e1306c', name: 'Instagram' },
  facebook: { icon: 'f', color: '#1877f2', name: 'Facebook' },
  line: { icon: 'L', color: '#06c755', name: 'LINE' },
  custom: { icon: '🔗', color: '#888', name: '連結' },
}
function socialInfo(p) { return socialMeta[p] || socialMeta.custom }
// real brand glyphs (simple-icons paths), white fill on the brand-colour circle
const socialSvgMap = {
  youtube: '<path d="M23.498 6.186a3.016 3.016 0 0 0-2.122-2.136C19.505 3.545 12 3.545 12 3.545s-7.505 0-9.377.505A3.017 3.017 0 0 0 .502 6.186C0 8.07 0 12 0 12s0 3.93.502 5.814a3.016 3.016 0 0 0 2.122 2.136c1.871.505 9.376.505 9.376.505s7.505 0 9.377-.505a3.015 3.015 0 0 0 2.122-2.136C24 15.93 24 12 24 12s0-3.93-.502-5.814zM9.545 15.568V8.432L15.818 12l-6.273 3.568z"/>',
  instagram: '<path d="M12 2.163c3.204 0 3.584.012 4.85.07 3.252.148 4.771 1.691 4.919 4.919.058 1.265.069 1.645.069 4.849 0 3.205-.012 3.584-.069 4.849-.149 3.225-1.664 4.771-4.919 4.919-1.266.058-1.644.07-4.85.07-3.204 0-3.584-.012-4.849-.07-3.26-.149-4.771-1.699-4.919-4.92-.058-1.265-.07-1.644-.07-4.849 0-3.204.013-3.583.07-4.849.149-3.227 1.664-4.771 4.919-4.919 1.266-.057 1.645-.069 4.849-.069zM12 0C8.741 0 8.333.014 7.053.072 2.695.272.273 2.69.073 7.052.014 8.333 0 8.741 0 12c0 3.259.014 3.668.072 4.948.2 4.358 2.618 6.78 6.98 6.98C8.333 23.986 8.741 24 12 24c3.259 0 3.668-.014 4.948-.072 4.354-.2 6.782-2.618 6.979-6.98.059-1.28.073-1.689.073-4.948 0-3.259-.014-3.667-.072-4.947-.196-4.354-2.617-6.78-6.979-6.98C15.668.014 15.259 0 12 0zm0 5.838a6.162 6.162 0 1 0 0 12.324 6.162 6.162 0 0 0 0-12.324zM12 16a4 4 0 1 1 0-8 4 4 0 0 1 0 8zm6.406-11.845a1.44 1.44 0 1 0 0 2.881 1.44 1.44 0 0 0 0-2.881z"/>',
  line: '<path d="M19.365 9.863c.349 0 .63.285.63.631 0 .345-.281.63-.63.63H17.61v1.125h1.755c.349 0 .63.283.63.63 0 .344-.281.629-.63.629h-2.386c-.345 0-.627-.285-.627-.629V8.108c0-.345.282-.63.63-.63h2.386c.346 0 .627.285.627.63 0 .349-.281.63-.63.63H17.61v1.125h1.755zm-3.855 3.016c0 .27-.174.51-.432.596-.064.021-.133.031-.199.031-.211 0-.391-.09-.51-.25l-2.443-3.317v2.94c0 .344-.279.629-.631.629-.346 0-.626-.285-.626-.629V8.108c0-.27.173-.51.43-.595.06-.023.136-.033.194-.033.195 0 .375.104.495.254l2.462 3.33V8.108c0-.345.282-.63.63-.63.345 0 .63.285.63.63v4.771zm-5.741 0c0 .344-.282.629-.631.629-.345 0-.627-.285-.627-.629V8.108c0-.345.282-.63.63-.63.346 0 .628.285.628.63v4.771zm-2.466.629H4.917c-.345 0-.63-.285-.63-.629V8.108c0-.345.285-.63.63-.63.348 0 .63.285.63.63v4.141h1.756c.348 0 .629.283.629.63 0 .344-.282.629-.629.629M24 10.314C24 4.943 18.615.572 12 .572S0 4.943 0 10.314c0 4.811 4.27 8.842 10.035 9.608.391.082.923.258 1.058.59.12.301.079.766.038 1.08l-.164 1.02c-.045.301-.24 1.186 1.049.645 1.291-.539 6.916-4.078 9.436-6.975C23.176 14.393 24 12.458 24 10.314"/>',
  telegram: '<path d="M11.944 0A12 12 0 0 0 0 12a12 12 0 0 0 12 12 12 12 0 0 0 12-12A12 12 0 0 0 12 0a12 12 0 0 0-.056 0zm4.962 7.224c.1-.002.321.023.465.14a.506.506 0 0 1 .171.325c.016.093.036.306.02.472-.18 1.898-.962 6.502-1.36 8.627-.168.9-.499 1.201-.82 1.23-.696.065-1.225-.46-1.9-.902-1.056-.693-1.653-1.124-2.678-1.8-1.185-.78-.417-1.21.258-1.91.177-.184 3.247-2.977 3.307-3.23.007-.032.014-.15-.056-.212s-.174-.041-.249-.024c-.106.024-1.793 1.14-5.061 3.345-.48.33-.913.49-1.302.48-.428-.008-1.252-.241-1.865-.44-.752-.245-1.349-.374-1.297-.789.027-.216.325-.437.893-.663 3.498-1.524 5.83-2.529 6.998-3.014 3.332-1.386 4.025-1.627 4.476-1.635z"/>',
  facebook: '<path d="M24 12.073c0-6.627-5.373-12-12-12s-12 5.373-12 12c0 5.99 4.388 10.954 10.125 11.854v-8.385H7.078v-3.47h3.047V9.43c0-3.007 1.792-4.669 4.533-4.669 1.312 0 2.686.235 2.686.235v2.953H15.83c-1.491 0-1.956.925-1.956 1.874v2.25h3.328l-.532 3.47h-2.796v8.385C19.612 23.027 24 18.062 24 12.073z"/>',
}
function socialSvg(p) {
  const path = socialSvgMap[p]
  return path ? `<svg viewBox="0 0 24 24" width="22" height="22" fill="#fff">${path}</svg>` : ''
}
const socialLinks = computed(() => { try { return JSON.parse(config.value.social || '[]') } catch (e) { return [] } })
const logoUrl = computed(() => config.value.logo || '/logo.png')
async function loadConfig() {
  try {
    const res = await authFetch('/api/config')
    if (res.ok) config.value = (await res.json()) || {}
  } catch (e) {
    /* secondary */
  }
}
const cfgSocial = ref([])
function loadCfgEditor() { cfgSocial.value = socialLinks.value.map((s) => ({ ...s })) }
function addSocial() { cfgSocial.value.push({ platform: 'youtube', url: '' }) }
function removeSocial(i) { cfgSocial.value.splice(i, 1) }
async function setConfig(key, value) {
  const res = await authFetch('/api/admin/config', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ key, value }),
  })
  if (res.ok) { await loadConfig(); adminMsg.value = '✓ 已儲存設定' }
  return res.ok
}
async function saveSocial() { await setConfig('social', JSON.stringify(cfgSocial.value.filter((s) => s.url))) }
async function onLogoPick(e) {
  const f = e.target.files && e.target.files[0]
  if (f) { const p = await uploadImage(f, 'logo'); if (p) await setConfig('logo', p) }
}
async function onQrPick(e) {
  const f = e.target.files && e.target.files[0]
  if (f) { const p = await uploadImage(f, 'qr'); if (p) await setConfig('qr', p) }
}
// ---- admin: instant push broadcast to a user group (optional article deep-link) ----
const bcTitle = ref('')
const bcBody = ref('')
const bcGroup = ref('admin')
const bcArticle = ref('') // '' = no jump; otherwise an article id → push opens that article
async function sendBroadcast() {
  if (!bcTitle.value.trim() || !bcBody.value.trim()) { showToast('標題與內容必填', 'err'); return }
  const res = await authFetch('/api/admin/push-broadcast', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title: bcTitle.value.trim(), body: bcBody.value.trim(), group: bcGroup.value, article: bcArticle.value }),
  })
  if (!res.ok) { showToast((await res.text()).trim() || '推播失敗', 'err'); return }
  const d = await res.json()
  showToast('已推播給 ' + d.sent + ' 個裝置', 'ok')
  bcTitle.value = ''; bcBody.value = ''
}

// open a specific article by id (used by push deep-links → 文章專欄)
async function openArticleById(id) {
  try {
    const res = await authFetch('/api/articles/' + id)
    if (res.ok) { articleView.value = await res.json(); mainTab.value = 'articles' }
  } catch (e) { /* ignore */ }
}

async function loadHome() {
  try {
    const res = await authFetch('/api/home')
    if (!res.ok) throw new Error('HTTP ' + res.status)
    home.value = await res.json()
    error.value = ''
  } catch (e) {
    error.value = String(e)
  }
}

async function loadBoard() {
  try {
    const res = await authFetch('/api/oi-cache')
    if (!res.ok) return
    const json = await res.json()
    board.value = json.data || {}
    boardUpdated.value = json.updated_at || ''
  } catch (e) {
    /* board is secondary */
  }
}

const radar = ref(null)
async function loadRadar() {
  try {
    const res = await authFetch('/api/radar')
    if (!res.ok) return
    const d = await res.json()
    // normalise: when Binance has no data (e.g. a ban) the arrays come back
    // null → guard so the template's .length / v-for never crash the view.
    d.pump = d.pump || []
    d.dump = d.dump || []
    d.stocks = d.stocks || []
    radar.value = d
  } catch (e) {
    /* radar is secondary */
  }
}

const paper = ref(null)
const gamble = ref(null)
const gambleHedge = ref(null)
const emaOnly = ref(null)
async function loadGambleHedge() {
  try {
    const res = await authFetch('/api/admin/gamble-hedge')
    if (res.ok) gambleHedge.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}

// ---- admin: 30幣掃描池 1H strategy ----
const pool = ref(null)
async function loadPool() {
  try {
    const res = await authFetch('/api/admin/pool')
    if (res.ok) pool.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
function poolOutcome(o) {
  return o === 'signal' ? '死叉出場' : o === 'chandelier' ? '吊燈停損' : o === 'lock' ? '早鎖利出場' : o
}

// ---- admin: 動態ATR 4H 均線收斂 strategy ----
const conv = ref(null)
async function loadConv() {
  try {
    const res = await authFetch('/api/admin/conv')
    if (res.ok) conv.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
function convOutcome(o) {
  return o === 'tp' ? '止盈 TP' : o === 'sl' ? '止損 SL' : o === 'expired' ? '逾時' : o
}

// ---- admin: mean-reversion strategies (逆勢超買空 / 布林重回 / 乖離回歸) ----
const rsifade = ref(null)
const bollfade = ref(null)
const meanrev = ref(null)
async function loadRsifade() {
  try {
    const res = await authFetch('/api/admin/rsifade')
    if (res.ok) rsifade.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
async function loadBollfade() {
  try {
    const res = await authFetch('/api/admin/bollfade')
    if (res.ok) bollfade.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
async function loadMeanrev() {
  try {
    const res = await authFetch('/api/admin/meanrev')
    if (res.ok) meanrev.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
// admin: wipe a strategy book's simulated trades (memory + DB), then reload it.
async function clearStrat(book, loader) {
  if (!confirm('確定清空此策略的所有模擬單?此動作無法復原。')) return
  const res = await authFetch('/api/admin/strat-clear?book=' + book, { method: 'POST' })
  if (res.ok && loader) loader()
}
async function loadPaper() {
  try {
    const [p, g, eo] = await Promise.all([
      authFetch('/api/paper'), authFetch('/api/gamble'), authFetch('/api/ema-only')
    ])
    if (p.ok) paper.value = await p.json()
    if (g.ok) gamble.value = await g.json()
    if (eo.ok) emaOnly.value = await eo.json()
  } catch (e) {
    /* paper is secondary */
  }
}
const book = computed(() =>
  mainTab.value === 'gamble' ? gamble.value
    : mainTab.value === 'gamblehedge' ? gambleHedge.value
    : mainTab.value === 'emaonly' ? emaOnly.value
    : paper.value
)

// admin-only: force-close an open 銀河 trade at market (recorded as 逾時), then
// refresh the book. Pushes to TG + admin the same as an automatic close.
async function manualExit(t) {
  if (!confirm(`確定手動出場 ${t.coin}？將以現價結算並標記「逾時」。`)) return
  try {
    const res = await authFetch('/api/admin/ema-close', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: t.id }),
    })
    if (!res.ok) { showToast((await res.text()).trim() || '出場失敗', 'err'); return }
    showToast('已手動出場（逾時結算）', 'ok')
    loadPaper()
  } catch (e) {
    showToast('出場失敗', 'err')
  }
}

// admin-only: download the current strategy book's full trade history as CSV
async function exportCSV() {
  const map = { paper: 'main', gamble: 'gamble', gamblehedge: 'gamblehedge', emaonly: 'emaonly' }
  const bookName = map[mainTab.value]
  if (!bookName) return
  try {
    const res = await authFetch('/api/admin/export?book=' + bookName)
    if (!res.ok) { showToast('匯出失敗', 'err'); return }
    const blob = await res.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = bookName + '-' + new Date().toISOString().slice(0, 19).replace(/[:T]/g, '') + '.csv'
    document.body.appendChild(a); a.click(); a.remove()
    URL.revokeObjectURL(url)
    showToast('CSV 已匯出', 'ok')
  } catch (e) { showToast('匯出失敗', 'err') }
}

// ---- time-window filter for the record pages (訊號紀錄 / 模擬倉 / 賭博單) ----
const timeWin = ref(0) // ms; 0 = all
const timePresets = [
  { label: '全部', ms: 0 },
  { label: '近1h', ms: 3600e3 },
  { label: '近6h', ms: 6 * 3600e3 },
  { label: '近24h', ms: 24 * 3600e3 },
  { label: '近3天', ms: 3 * 24 * 3600e3 },
  { label: '近7天', ms: 7 * 24 * 3600e3 },
]
function withinWin(iso) {
  if (!timeWin.value || !iso) return true
  return Date.now() - new Date(iso).getTime() <= timeWin.value
}
const scoreLogF = computed(() => scoreLog.value.filter((e) => withinWin(e.time)))
// book filtered by time window, with stats recomputed over the filtered set
const bookF = computed(() => {
  const b = book.value
  if (!b) return null
  const open = (b.open || []).filter((t) => withinWin(t.open_time))
  const closed = (b.closed || []).filter((t) => withinWin(t.close_time))
  let wins = 0,
    sum = 0
  for (const t of closed) {
    if (t.pnl_pct > 0) wins++
    sum += t.pnl_pct
  }
  const n = closed.length
  return {
    open,
    closed,
    stats: {
      closed: n,
      wins,
      losses: n - wins,
      win_rate: n ? +((wins / n) * 100).toFixed(2) : 0,
      avg_pnl: n ? +(sum / n).toFixed(2) : 0,
      total_pnl: +sum.toFixed(2),
    },
  }
})

const scoreLog = ref([])
async function loadScoreLog() {
  try {
    const res = await authFetch('/api/scorelog')
    if (res.ok) scoreLog.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}

const risk = ref(null)
async function loadRisk() {
  try {
    const res = await authFetch('/api/risk')
    if (res.ok) risk.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
const riskLabel = (r) => (r === 'risk-on' ? '風險偏好' : r === 'risk-off' ? '風險趨避' : '中性')

const eventList = ref([])
async function loadEvents() {
  try {
    const res = await authFetch('/api/events')
    if (res.ok) eventList.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
function evSoon(e) {
  if (e.released || !e.countdown) return false
  const h = e.countdown.includes('h') ? parseInt(e.countdown) : 0
  return h < 6 // highlight events firing within ~6h (minutes-only ⇒ h=0)
}

const liquidations = ref(null)
async function loadFlow() {
  try {
    const lq = await authFetch('/api/liquidations')
    if (lq.ok) liquidations.value = await lq.json()
  } catch (e) {
    /* secondary */
  }
}
function liqClock(ms) {
  return new Date(ms).toLocaleTimeString('zh-TW', { hour: '2-digit', minute: '2-digit', hour12: false })
}

const upbitNotices = ref([])
async function loadUpbit() {
  try {
    const res = await authFetch('/api/upbit')
    if (res.ok) upbitNotices.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
function upbitTime(s) {
  if (!s) return ''
  const d = new Date(s)
  if (isNaN(d)) return s
  return d.toLocaleString('zh-TW', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })
}

const news = ref([])
async function loadNews() {
  try {
    const res = await authFetch('/api/news')
    if (res.ok) news.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}

const funding = ref(null)
async function loadFunding() {
  try {
    const res = await authFetch('/api/funding')
    if (res.ok) funding.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
function fundClock(ms) {
  if (!ms) return '—'
  return new Date(ms).toLocaleString('zh-TW', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })
}
const fundingSector = ref('')
const fundingAbs = ref(false) // true: sort by |rate| (find both-side extremes)
const fundingSectors = computed(() => {
  if (!funding.value) return []
  const counts = {}
  for (const r of funding.value.rows) counts[r.sector] = (counts[r.sector] || 0) + 1
  return Object.keys(counts).sort((a, b) => counts[b] - counts[a]) // most coins first
})
const fundingRows = computed(() => {
  if (!funding.value) return []
  let rows = fundingSector.value ? funding.value.rows.filter((r) => r.sector === fundingSector.value) : funding.value.rows
  if (fundingAbs.value) rows = [...rows].sort((a, b) => Math.abs(b.rate) - Math.abs(a.rate))
  return rows
})
// 代幣解鎖 (DefiLlama emissions) — public tab
const unlock = ref(null)
async function loadUnlock() {
  try {
    const res = await authFetch('/api/unlock')
    if (res.ok) unlock.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
const unlockSort = ref('sell') // 'sell': 30d 佔流通% 大→小(賣壓); 'date': 最近懸崖優先
const unlockRows = computed(() => {
  if (!unlock.value) return []
  const rows = [...unlock.value.rows]
  if (unlockSort.value === 'date') {
    rows.sort((a, b) => {
      const ta = a.peak_date ? new Date(a.peak_date).getTime() : Infinity
      const tb = b.peak_date ? new Date(b.peak_date).getTime() : Infinity
      return ta - tb
    })
  }
  return rows
})
function unlockDays(d) {
  if (!d) return '—'
  const days = Math.round((new Date(d).getTime() - Date.now()) / 86400000)
  if (days <= 0) return '今日'
  return days + ' 天'
}
function unlockDate(d) {
  if (!d) return '—'
  return new Date(d).toLocaleDateString('zh-TW', { month: '2-digit', day: '2-digit' })
}

const newsCat = ref('')
const newsCatList = [
  { key: 'figure', label: '🗣 人物' },
  { key: 'cb', label: '🏦 央行' },
  { key: 'trade', label: '📉 貿易' },
  { key: 'geo', label: '⚔️ 地緣' },
  { key: 'reg', label: '⚖️ 監管' },
  { key: 'hack', label: '🚨 爆雷' },
  { key: 'inst', label: '🏛 機構' },
  { key: 'whale', label: '🐋 巨鯨' },
  { key: 'crypto', label: '🪙 加密' },
  { key: 'misc', label: '📰 綜合' },
]
const newsF = computed(() => (newsCat.value ? news.value.filter((n) => n.category === newsCat.value) : news.value))

const sr = ref(null)
async function loadSR() {
  try {
    const res = await authFetch('/api/sr')
    if (res.ok) sr.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
const srStatusMeta = {
  break_down: { txt: '🔻 跌破支撐', cls: 'short' },
  break_up: { txt: '🚀 突破壓力', cls: 'long' },
  range: { txt: '區間內', cls: 'neutral' },
}

const boardRows = computed(() =>
  Object.entries(board.value)
    .map(([coin, v]) => ({ coin, ...v }))
    .sort((a, b) => Math.abs(b.score) - Math.abs(a.score))
)

// ---- BTC regime filter (backtest: counter-BTC-trend signals lose money) ----
const regimeFilter = ref(localStorage.getItem('regimeFilter') !== '0')
function toggleRegime() {
  regimeFilter.value = !regimeFilter.value
  localStorage.setItem('regimeFilter', regimeFilter.value ? '1' : '0')
}
const btcChg = computed(() => (home.value ? home.value.ticker.BTC.chg : 0))
const btcRegime = computed(() => (btcChg.value > 0 ? 'long' : btcChg.value < 0 ? 'short' : 'neutral'))
function regimeAllows(bias) {
  if (!regimeFilter.value) return true
  if (bias === 'long') return btcChg.value >= 0
  if (bias === 'short') return btcChg.value <= 0
  return true
}
// ---- OI-contraction quality gate (OOS-validated: signals fire best while OI
// is contracting = exhaustion/unwind, not while new money is piling in) ----
const qualityFilter = ref(localStorage.getItem('qualityFilter') !== '0')
function toggleQuality() {
  qualityFilter.value = !qualityFilter.value
  localStorage.setItem('qualityFilter', qualityFilter.value ? '1' : '0')
}
const boardOf = (coin) => board.value[coin] || null
function oiContracting(r) {
  return !!r && r.oi_chg_1h < 0
}
function fundingHot(r) {
  return !!r && Math.abs(r.funding_rate * 100) >= 0.0035
}
function isHighQuality(r) {
  return oiContracting(r) && fundingHot(r) // the strongest OOS bucket: both
}
function qualityAllows(r) {
  if (!qualityFilter.value) return true
  if (!r) return true // no board data yet → don't filter it out
  return r.oi_chg_1h < 0
}

const filteredLongRecs = computed(() => {
  if (!home.value || !regimeAllows('long')) return []
  return (home.value.long_recs || []).filter((r) => qualityAllows(boardOf(r.coin)))
})
const filteredShortRecs = computed(() => {
  if (!home.value || !regimeAllows('short')) return []
  return (home.value.short_recs || []).filter((r) => qualityAllows(boardOf(r.coin)))
})

// actionable entry signals: coins the scorer actually rates long/short
// (|score| >= 20), gated by BTC trend + OI contraction when filters are on.
const signals = computed(() =>
  boardRows.value.filter(
    (r) => (r.bias === 'long' || r.bias === 'short') && regimeAllows(r.bias) && qualityAllows(r)
  )
)

function strengthOf(score) {
  const b = Math.ceil(Math.abs(score) / 8)
  return Math.min(5, Math.max(1, b))
}

const market = computed(() => {
  if (!home.value) return []
  const m = [...home.value.market]
  if (marketSort.value === 'gainers') m.sort((a, b) => b.chg - a.chg)
  else if (marketSort.value === 'losers') m.sort((a, b) => a.chg - b.chg)
  // 'vol' already sorted by backend
  return m
})

// ---- formatting helpers ----
function fmtPrice(n) {
  if (n == null) return '-'
  if (n >= 1000) return '$' + n.toLocaleString('en-US', { maximumFractionDigits: 2 })
  if (n >= 1) return '$' + n.toFixed(n >= 100 ? 2 : 4)
  return '$' + n.toPrecision(4)
}
function fmtNum(n) {
  const a = Math.abs(n)
  if (a >= 1e9) return (n / 1e9).toFixed(2) + 'B'
  if (a >= 1e6) return (n / 1e6).toFixed(1) + 'M'
  if (a >= 1e3) return (n / 1e3).toFixed(1) + 'K'
  return n.toFixed(2)
}
function fmtPct(n) {
  return (n >= 0 ? '+' : '') + n.toFixed(2) + '%'
}
function fmtClock(iso) {
  if (!iso) return '-'
  return new Date(iso).toLocaleString('zh-TW', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })
}
function fmtDur(ms) {
  if (!isFinite(ms) || ms < 0) return '-'
  const m = Math.floor(ms / 60000)
  if (m < 60) return m + 'm'
  const h = Math.floor(m / 60)
  if (h < 24) return h + 'h' + (m % 60 ? (m % 60) + 'm' : '')
  return Math.floor(h / 24) + 'd' + (h % 24) + 'h'
}
function holdMs(t) {
  const o = new Date(t.open_time).getTime()
  const e = t.close_time ? new Date(t.close_time).getTime() : Date.now()
  return e - o
}
// directional % from entry to a level (TP gain / SL loss), for the trade's side
function pnlAt(t, price) {
  if (!t.entry) return 0
  return t.dir === 'short' ? ((t.entry - price) / t.entry) * 100 : ((price - t.entry) / t.entry) * 100
}
function fmtFund(f) {
  if (f === undefined || f === null) return '—'
  return (f >= 0 ? '+' : '') + (f * 100).toFixed(4) + '%'
}
// live momentum light for an open position (from backend radar score + CVD)
const momMeta = {
  alive: { txt: '🟢 動能在', cls: 'mom-alive' },
  weak: { txt: '🟡 轉弱', cls: 'mom-weak' },
  dead: { txt: '🔴 熄火', cls: 'mom-dead' },
}
function momText(m) {
  return (momMeta[m] || {}).txt || '—'
}
function momClass(m) {
  return (momMeta[m] || {}).cls || ''
}
function medal(i) {
  return ['🥇', '🥈', '🥉'][i] || i + 1
}
function biasClass(b) {
  return b === 'long' ? 'long' : b === 'short' ? 'short' : 'neutral'
}

// ---- altcoin season gauge ----
const gaugeNeedle = computed(() => {
  const v = home.value ? home.value.alt_season.value : 50
  return -90 + (v / 100) * 180 // -90deg (left) .. +90deg (right)
})
const gaugeLabelClass = computed(() => {
  const v = home.value ? home.value.alt_season.value : 50
  if (v < 45) return 'short'
  if (v > 55) return 'long'
  return 'neutral'
})

// ---- detail drawer ----
const detail = ref(null)
const detailCoin = ref('')
const detailLoading = ref(false)
const detailError = ref('')

async function openDetail(coin) {
  detailCoin.value = coin
  detail.value = null
  detailError.value = ''
  detailLoading.value = true
  try {
    const res = await authFetch('/api/coin/' + coin)
    if (!res.ok) throw new Error('HTTP ' + res.status)
    detail.value = await res.json()
  } catch (e) {
    detailError.value = String(e)
  } finally {
    detailLoading.value = false
  }
}
function closeDetail() {
  detailCoin.value = ''
  detail.value = null
}
const ratingDots = computed(() => {
  const r = detail.value ? detail.value.rating : 0
  return Array.from({ length: 10 }, (_, i) => i < r)
})
const headerBadge = computed(() => {
  if (!detail.value) return ''
  const r = detail.value.rating
  if (detail.value.bias === 'long') return '+' + r
  if (detail.value.bias === 'short') return '-' + r
  return String(r)
})
function rationaleTitle() {
  if (!detail.value) return '依據'
  if (detail.value.bias === 'long') return '做多依據'
  if (detail.value.bias === 'short') return '做空依據'
  return '觀察依據'
}
function toneClass(t) {
  return t === 'pos' ? 'long' : t === 'neg' ? 'short' : 'neutral'
}
function scoreClass(n) {
  return n > 0 ? 'long' : n < 0 ? 'short' : 'neutral'
}

// load everything the current role is allowed to see (gated endpoints 403 quietly)
function loadAll() {
  if (!authed.value) return // gated: nothing loads until an active member+ is in
  loadRanking()
  loadHome()
  loadRisk()
  loadEvents()
  loadFlow()
  loadUpbit()
  loadNews()
  loadFunding()
  loadUnlock()
  loadArticles()
  if (can('member')) {
    loadBoard()
    loadRadar()
    loadScoreLog()
  }
  if (can('vip')) {
    loadPaper()
    loadSR()
  }
  if (can('admin')) {
    loadUsers()
    loadGambleHedge()
    // load every strategy each cycle so the nav badges show open counts without
    // having to open each tab first
    loadPool()
    loadConv()
    loadRsifade()
    loadBollfade()
    loadMeanrev()
  }
}
// per-tick: re-verify the session (idle timeout / ban take effect within 15s),
// then refresh data. Also re-check whenever the user switches page (tab).
async function tick() {
  await loadMe()
  loadAll()
}

// ---- PWA: install + web push (Feature 5) ----
const deferredPrompt = ref(null)
const canInstall = ref(false)
const notifState = ref('') // '' | 'on' | 'denied' | 'unsupported'
function urlB64ToUint8Array(base64String) {
  const padding = '='.repeat((4 - (base64String.length % 4)) % 4)
  const b64 = (base64String + padding).replace(/-/g, '+').replace(/_/g, '/')
  const raw = atob(b64)
  const arr = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) arr[i] = raw.charCodeAt(i)
  return arr
}
function sameKey(a, b) {
  if (!a || !b) return false
  const x = new Uint8Array(a)
  if (x.length !== b.length) return false
  for (let i = 0; i < x.length; i++) if (x[i] !== b[i]) return false
  return true
}
// ensurePush (re)creates the push subscription and re-registers it on the server.
// interactive=false: silent self-heal on load (only if permission already granted).
// interactive=true: user clicked 🔔 通知 — may prompt for permission.
async function ensurePush(interactive) {
  if (!('serviceWorker' in navigator) || !('PushManager' in window)) { notifState.value = 'unsupported'; return }
  let perm = Notification.permission
  if (perm === 'default') {
    if (!interactive) return // don't prompt during silent auto-sync
    perm = await Notification.requestPermission()
  }
  if (perm !== 'granted') { notifState.value = 'denied'; return }
  try {
    const reg = await navigator.serviceWorker.ready
    const res = await authFetch('/api/push/key')
    const { key } = await res.json()
    if (!key) { notifState.value = 'unsupported'; return }
    const appKey = urlB64ToUint8Array(key)
    let sub = await reg.pushManager.getSubscription()
    // resubscribe if none, or if the stored one used a different VAPID key
    if (!sub || !sameKey(sub.options.applicationServerKey, appKey)) {
      if (sub) { try { await sub.unsubscribe() } catch (e) {} }
      sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: appKey })
    }
    // always re-register with the server (idempotent upsert by endpoint)
    await authFetch('/api/push/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(sub) })
    notifState.value = 'on'
  } catch (e) { if (interactive) notifState.value = 'denied' }
}
async function enableNotifications() { await ensurePush(true) }
async function installApp() {
  if (!deferredPrompt.value) return
  deferredPrompt.value.prompt()
  await deferredPrompt.value.userChoice
  deferredPrompt.value = null
  canInstall.value = false
}

// tabs a push notification may deep-link to (from the ?tab= query on cold start
// or a SW postMessage when the app is already open).
const NAV_TABS = ['paper', 'gamble', 'gamblehedge', 'emaonly', 'ranking', 'radar', 'signals', 'scorelog', 'sr', 'upbit', 'news', 'funding', 'unlock', 'articles', 'pool', 'conv', 'rsifade', 'bollfade', 'meanrev']
function gotoTab(t) { if (NAV_TABS.includes(t)) mainTab.value = t }
let onVisibility = null
let onPageShow = null
let onDocClick = null
let hiddenAt = 0
onMounted(async () => {
  // help popovers: click the ? to toggle, click anywhere else (or another ?) to
  // close — mobile :hover would otherwise stick open until a re-render.
  onDocClick = (e) => {
    if (e.target.closest('.help-pop')) return // tap inside the tooltip → keep open
    const help = e.target.closest('.help')
    document.querySelectorAll('.help.open').forEach((el) => { if (el !== help) el.classList.remove('open') })
    if (help) help.classList.toggle('open')
  }
  document.addEventListener('click', onDocClick)
  loadConfig()
  await loadMe()
  loadAll()
  timer = setInterval(tick, 15000)
  // deep-link: notification opened the app cold with /?tab=gamble (and optionally
  // &article=<id> to jump straight into a column post) → apply it
  const qs = new URLSearchParams(location.search)
  const qtab = qs.get('tab')
  const qart = qs.get('article')
  if (qtab) gotoTab(qtab)
  if (qart) openArticleById(qart)
  if (qtab || qart) history.replaceState({}, '', location.pathname)
  // register service worker for PWA + push
  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/sw.js').catch(() => {})
    // app already open → SW tells us which tab (and article) the tapped notification wants
    navigator.serviceWorker.addEventListener('message', (e) => {
      if (e.data && e.data.type === 'nav') {
        gotoTab(e.data.tab)
        if (e.data.article) openArticleById(e.data.article)
      }
    })
  }
  // silent self-heal: if notifications were already granted, make sure this
  // device actually has a live subscription registered on the server.
  if (can('member')) ensurePush(false)
  window.addEventListener('beforeinstallprompt', (e) => {
    e.preventDefault()
    deferredPrompt.value = e
    canInstall.value = true
  })
  // PWA/tab resume: mobile browsers throttle background timers and may freeze or
  // discard a backgrounded standalone PWA — you return to a blank/stale screen.
  // On return to the foreground, re-sync immediately; after a long background,
  // reload to rebuild a clean state instead of risking the blank-resume screen.
  onVisibility = () => {
    if (document.visibilityState === 'hidden') { hiddenAt = Date.now(); return }
    if (hiddenAt && Date.now() - hiddenAt > 30 * 60 * 1000) { location.reload(); return }
    tick()
  }
  document.addEventListener('visibilitychange', onVisibility)
  // restored from the back/forward cache → reload to get a clean, current page
  onPageShow = (e) => { if (e.persisted) location.reload() }
  window.addEventListener('pageshow', onPageShow)
})
onUnmounted(() => {
  clearInterval(timer)
  if (onVisibility) document.removeEventListener('visibilitychange', onVisibility)
  if (onPageShow) window.removeEventListener('pageshow', onPageShow)
  if (onDocClick) document.removeEventListener('click', onDocClick)
})
watch(mainTab, loadMe)

// keep the visible tab renderable: if a role change (a resume re-check, a ban,
// or a rotated token) drops the user below the current tab's requirement, fall
// back to the public ranking tab — so the content area can never land on a gated
// v-else-if branch that matches nothing and renders blank.
const TAB_MIN_ROLE = {
  oi: 'member', signals: 'member', scorelog: 'member', radar: 'member',
  paper: 'vip', gamble: 'vip', emaonly: 'vip',
  sr: 'vip',
  admin: 'admin', gamblehedge: 'admin', pool: 'admin', conv: 'admin',
  rsifade: 'admin', bollfade: 'admin', meanrev: 'admin',
}
watch(role, () => {
  const need = TAB_MIN_ROLE[mainTab.value]
  if (need && !can(need)) mainTab.value = 'ranking'
})
</script>

<template>
  <!-- toast prompt -->
  <transition name="toastfade">
    <div v-if="toastMsg" class="toast" :class="toastType">{{ toastMsg }}</div>
  </transition>

  <!-- auth gate: shown until an APPROVED (active) member+ is logged in -->
  <div v-if="!authed" class="authgate">
    <div class="authcard">
      <img src="/logo.png" class="authlogo" alt="JMCH" />
      <p class="authslogan">Just MONEY Come Here</p>

      <div v-if="!authReady" class="authmsg">驗證中…</div>
      <template v-else>
        <div class="authtabs">
          <button :class="{ on: authTab === 'login' }" @click="authTab = 'login'; regErr = ''; regDone = ''">登入</button>
          <button :class="{ on: authTab === 'register' }" @click="authTab = 'register'; loginErr = ''">註冊</button>
        </div>

        <div v-if="authMsg" class="authnote">{{ authMsg }}</div>

        <template v-if="authTab === 'login'">
          <input :value="loginForm.u" @input="loginForm.u = sanitizeAcct($event.target.value)" class="authin" placeholder="帳號(4–16 英數)" @keyup.enter="doLogin" />
          <input :value="loginForm.p" @input="loginForm.p = sanitizePw($event.target.value)" class="authin" type="password" placeholder="密碼" @keyup.enter="doLogin" />
          <button class="authbtn" @click="doLogin">登入</button>
          <div v-if="loginErr" class="autherr">{{ loginErr }}</div>
        </template>

        <template v-else>
          <div class="regcond">
            <b>註冊條件</b>
            <span>① 使用我們推薦碼註冊的 Bitunix 帳戶,持有 300U 以上</span>
            <span>② 填寫交易所 UID 與 Email</span>
            <span>③ 上傳資產證明圖片</span>
          </div>
          <a class="bitunix-cta" href="https://www.bitunix.com/register?vipCode=jmch" target="_blank" rel="noopener">
            🚀 還沒有 Bitunix 帳戶?點此註冊(專屬推薦碼 jmch)
          </a>
          <input :value="regForm.u" @input="regForm.u = sanitizeAcct($event.target.value)" class="authin" placeholder="帳號(4–16 英文或數字)" />
          <input :value="regForm.p" @input="regForm.p = sanitizePw($event.target.value)" class="authin" type="password" placeholder="密碼(4–16,含大小寫+數字+特殊符號)" />
          <input v-model="regForm.uid" class="authin" placeholder="UID" />
          <input v-model="regForm.email" class="authin" type="email" placeholder="Email" />
          <input v-model="regForm.exchange" class="authin" placeholder="交易所名稱(備註,選填)" />
          <label class="authfile">
            <span>{{ regFile ? '📎 ' + regFile.name : '＋ 上傳資產證明圖片(300U 以上)' }}</span>
            <input type="file" accept="image/*,.heic,.heif" @change="onRegFile" hidden />
          </label>
          <button class="authbtn" @click="doRegister">註冊</button>
          <div v-if="regErr" class="autherr">{{ regErr }}</div>
          <div v-if="regDone" class="authok">{{ regDone }}</div>
          <p class="authhint">註冊後為「審核中」狀態,需管理員審核通過才能登入。</p>
        </template>
      </template>
    </div>
  </div>

  <!-- top bar -->
  <header class="topbar">
    <div class="tickers" v-if="home">
      <span class="tk"><b>BTC</b> {{ fmtPrice(home.ticker.BTC.price) }}
        <em :class="home.ticker.BTC.chg >= 0 ? 'long' : 'short'">{{ fmtPct(home.ticker.BTC.chg) }}</em></span>
      <span class="tk"><b>ETH</b> {{ fmtPrice(home.ticker.ETH.price) }}
        <em :class="home.ticker.ETH.chg >= 0 ? 'long' : 'short'">{{ fmtPct(home.ticker.ETH.chg) }}</em></span>
    </div>
    <div class="search">🔍 搜尋幣種…</div>
    <div class="topmeta">
      <span v-if="error" class="err">{{ error }}</span>
      <span v-if="home" class="regime">BTC 趨勢
        <b :class="btcRegime">{{ btcRegime === 'long' ? '偏多' : btcRegime === 'short' ? '偏空' : '中性' }}</b>
      </span>
      <button class="regbtn" :class="{ on: regimeFilter }" @click="toggleRegime" title="只保留順 BTC 趨勢的方向訊號(回測有效)">
        順勢過濾 {{ regimeFilter ? '✓' : '✕' }}
      </button>
      <button class="regbtn" :class="{ on: qualityFilter }" @click="toggleQuality" title="只保留 OI 收縮(衰竭/平倉)時的訊號;樣本外驗證有效">
        OI收縮過濾 {{ qualityFilter ? '✓' : '✕' }}
      </button>
      <span v-if="role !== 'public'" class="userchip">{{ username }} <em>{{ role }}</em>
        <button v-if="canInstall" class="regbtn" @click="installApp" title="安裝為 App">📲 安裝</button>
        <button v-if="notifState !== 'on'" class="regbtn" @click="enableNotifications" title="開啟推播通知">🔔 通知</button>
        <span v-else class="qtag good" title="推播已開啟">🔔 已開</span>
        <button class="regbtn" @click="logout">登出</button>
      </span>
      <button v-else class="regbtn login" @click="loginOpen = true">登入</button>
      <span class="brand">數據看板</span>
    </div>
  </header>

  <!-- login modal -->
  <div v-if="loginOpen" class="overlay" @click="loginOpen = false">
    <div class="loginbox" @click.stop>
      <h3>會員登入</h3>
      <input v-model="loginForm.u" placeholder="帳號" autocomplete="username" @keyup.enter="doLogin" />
      <input v-model="loginForm.p" type="password" placeholder="密碼" autocomplete="current-password" @keyup.enter="doLogin" />
      <p v-if="loginErr" class="err">{{ loginErr }}</p>
      <button class="loginbtn" @click="doLogin">登入</button>
      <p class="loginhint">尚無帳號?請依公告填寫 Google 表單申請(附入金與 UID 證明)。</p>
    </div>
  </div>

  <!-- 登入成功 使用須知 modal -->
  <div v-if="welcomeOpen" class="overlay welcomeov" @click="welcomeOpen = false">
    <div class="welcomebox" @click.stop>
      <h3>📌 使用須知</h3>

      <div class="wc-sec">
        <div class="wc-title">👉 續用資格</div>
        <p>根據申請進入的日期按月計算</p>
        <p>🌟 第一個月無限制使用</p>
        <p>🌟 第二個月開始享有三個月的交易額優惠</p>
        <p>➡️ 每月達成 <b>50 萬合約交易額</b> 即可計入下月續用</p>
      </div>

      <div class="wc-sec">
        <div class="wc-title">👉 加入主畫面・開啟通知</div>
        <p>網址打開,右下選擇「分享」即可加入「手機主畫面」</p>
        <p>打開書籤 ➡️ 畫面最上方開啟「通知📢」即可獲得即時訊號📶</p>
      </div>

      <div class="wc-sec">
        <div class="wc-title">👉 下單前必讀</div>
        <p>每種訊號請務必先熟知 <b>問號小泡泡 ?</b> 的建議提醒再下單</p>
        <p>交易有任何問題請私訊 IG 或加入社群詢問:</p>
        <p>📷 <a class="wc-link" href="https://www.instagram.com/jmch_crypto?igsh=NzJ0bTJ0b3VhdWdw&utm_source=qr" target="_blank" rel="noopener">JMCH|加密貨幣技術分析</a></p>
        <p>💬 <a class="wc-link" href="https://line.me/ti/g2/v_YR0Oqc-BHmRf1jXsfbNgvbO_GOm-FuRFZqdA?utm_source=invitation&utm_medium=link_copy&utm_campaign=default" target="_blank" rel="noopener">社群《Crypto JMCH》</a></p>
      </div>

      <button class="authbtn" @click="welcomeOpen = false">我知道了</button>
    </div>
  </div>

  <!-- 被帶崩/帶噴 預警 (only when elevated) -->
  <div v-if="risk && risk.push && risk.push.level !== '低'" class="ddbanner down" :class="risk.push.level === '高' ? 'lv-high' : 'lv-mid'">
    <b class="dd-lv">⚠️ 被帶崩風險:{{ risk.push.level }}</b>
    <span class="dd-why">{{ risk.push.reasons.join(' · ') }}</span>
    <span class="dd-act">{{ risk.push.action }}</span>
  </div>

  <!-- 美股/風險背景燈 (always-visible strip) -->
  <div v-if="risk && risk.items.length" class="riskbar" :class="risk.risk">
    <span class="rb-light" :class="risk.risk">●</span>
    <span class="rb-tag">美股風險:{{ riskLabel(risk.risk) }}</span>
    <span class="rb-items">
      <span v-for="it in risk.items" :key="it.name" class="rb-it">
        {{ it.name }} <b :class="(it.name === 'VIX' || it.name === '美元DXY' ? -it.chg_pct : it.chg_pct) >= 0 ? 'long' : 'short'">{{ it.chg_pct >= 0 ? '+' : '' }}{{ it.chg_pct }}%</b>
      </span>
    </span>
    <span class="rb-us" :class="{ hot: risk.high_impact }">
      🇺🇸 {{ risk.us_status }}<template v-if="risk.countdown"> · {{ risk.countdown }}</template>
      <template v-if="risk.high_impact"> · ⚠️高影響時段</template>
    </span>
    <span v-if="risk.events && risk.events.length" class="rb-events">
      <span v-for="e in risk.events.slice(0, 3)" :key="e.title + e.time" class="rb-ev" :class="{ released: e.released }">
        📅 {{ e.title }}
        <b v-if="e.released">實際 {{ e.actual || '—' }} / 預期 {{ e.forecast || '—' }}</b>
        <b v-else>{{ e.countdown }}</b>
      </span>
    </span>
    <span v-if="risk.risk_reasons.length" class="rb-reason">{{ risk.risk_reasons.join(' · ') }}</span>
    <span class="rb-note" title="風險時段提醒,非回測訊號;紐約盤+VIX高+美股弱時對多單保守">ⓘ 背景燈</span>
  </div>

  <div class="wrap">
    <!-- three cards -->
    <div class="cards" v-if="home">
      <!-- 做多推薦 -->
      <section class="card rec">
        <div class="rec-head"><span class="led long"></span>做多推薦</div>
        <div class="rec-cols"><span>幣種</span><span>價格</span><span>推薦指數</span><span class="r">漲跌幅</span></div>
        <button v-for="(r, i) in filteredLongRecs" :key="r.coin" class="rec-row" :class="{ featured: r.featured }" @click="openDetail(r.coin)">
          <span class="rec-coin">
            <i class="medal">{{ medal(i) }}</i>{{ r.coin }}
            <em v-if="r.featured" class="hot">★ 強力</em>
            <em v-if="isHighQuality(boardOf(r.coin))" class="qtag hq" title="OI 收縮 + 費率極端(樣本外最佳組)">★優質</em>
            <em v-else-if="oiContracting(boardOf(r.coin))" class="qtag good" title="OI 收縮(衰竭/平倉,訊號較可靠)">OI↓</em>
            <em v-else class="qtag warn" title="OI 擴張(新倉湧入,追高風險)">OI↑</em>
          </span>
          <span class="rec-price">{{ fmtPrice(r.price) }}</span>
          <span class="bars">
            <i v-for="n in 5" :key="n" class="bar" :class="{ on: n <= r.strength, long: n <= r.strength }"></i>
          </span>
          <span class="r" :class="r.chg >= 0 ? 'long' : 'short'">{{ fmtPct(r.chg) }}</span>
        </button>
        <p v-if="!filteredLongRecs.length" class="empty">{{ regimeFilter && btcChg < 0 ? 'BTC 偏空 · 已過濾做多訊號' : '目前無做多訊號' }}</p>
      </section>

      <!-- 做空推薦 -->
      <section class="card rec">
        <div class="rec-head"><span class="led short"></span>做空推薦</div>
        <div class="rec-cols"><span>幣種</span><span>價格</span><span>推薦指數</span><span class="r">漲跌幅</span></div>
        <button v-for="(r, i) in filteredShortRecs" :key="r.coin" class="rec-row" :class="{ 'featured short-feat': r.featured }" @click="openDetail(r.coin)">
          <span class="rec-coin">
            <i class="medal">{{ medal(i) }}</i>{{ r.coin }}
            <em v-if="r.featured" class="hot short-hot">★ 強力</em>
            <em v-if="isHighQuality(boardOf(r.coin))" class="qtag hq" title="OI 收縮 + 費率極端(樣本外最佳組)">★優質</em>
            <em v-else-if="oiContracting(boardOf(r.coin))" class="qtag good" title="OI 收縮(衰竭/平倉,訊號較可靠)">OI↓</em>
            <em v-else class="qtag warn" title="OI 擴張(新倉湧入,追高風險)">OI↑</em>
          </span>
          <span class="rec-price">{{ fmtPrice(r.price) }}</span>
          <span class="bars">
            <i v-for="n in 5" :key="n" class="bar" :class="{ on: n <= r.strength, short: n <= r.strength }"></i>
          </span>
          <span class="r" :class="r.chg >= 0 ? 'long' : 'short'">{{ fmtPct(r.chg) }}</span>
        </button>
        <p v-if="!filteredShortRecs.length" class="empty">{{ regimeFilter && btcChg > 0 ? 'BTC 偏多 · 已過濾做空訊號' : '目前無做空訊號' }}</p>
      </section>

      <!-- 山寨季指數 -->
      <section class="card gauge">
        <div class="gauge-title">山寨季指數</div>
        <svg viewBox="0 0 200 120" class="gsvg">
          <path d="M20 110 A80 80 0 0 1 180 110" fill="none" stroke="#23262d" stroke-width="14" stroke-linecap="round" />
          <path d="M20 110 A80 80 0 0 1 180 110" fill="none" stroke="url(#gg)" stroke-width="14" stroke-linecap="round"
            :stroke-dasharray="251.2" :stroke-dashoffset="251.2 * (1 - (home.alt_season.value / 100))" />
          <defs>
            <linearGradient id="gg" x1="0" y1="0" x2="1" y2="0">
              <stop offset="0%" stop-color="#ff5c5c" />
              <stop offset="50%" stop-color="#e0b341" />
              <stop offset="100%" stop-color="#2ec26b" />
            </linearGradient>
          </defs>
          <line x1="100" y1="110" x2="100" y2="42" stroke="#e8eaed" stroke-width="3" stroke-linecap="round"
            :transform="`rotate(${gaugeNeedle} 100 110)`" />
          <circle cx="100" cy="110" r="6" fill="#e8eaed" />
        </svg>
        <div class="gauge-val">{{ home.alt_season.value }}</div>
        <div class="gauge-label" :class="gaugeLabelClass">{{ home.alt_season.label }}</div>
        <div class="gauge-prev" v-if="home.alt_season.prev">
          昨日 {{ home.alt_season.prev }}
          <em :class="home.alt_season.value - home.alt_season.prev >= 0 ? 'long' : 'short'">
            ({{ home.alt_season.value - home.alt_season.prev >= 0 ? '+' : '' }}{{ home.alt_season.value - home.alt_season.prev }})
          </em>
        </div>
        <div class="gauge-zones">
          <span class="short">BTC季</span><span>偏BTC</span><span class="neutral">中性</span><span>偏山寨</span><span class="long">山寨季</span>
        </div>
      </section>
    </div>

    <!-- nav -->
    <nav class="mainnav">
      <div class="navrow">
        <span class="navgroup">公開</span>
        <div class="navbtns">
          <button :class="{ active: mainTab === 'ranking' }" @click="mainTab = 'ranking'">綜合排行</button>
          <button :class="{ active: mainTab === 'list' }" @click="mainTab = 'list'">幣種一覽</button>
          <button :class="{ active: mainTab === 'events' }" @click="mainTab = 'events'">
            財經事件<em v-if="eventList.filter((e) => !e.released).length" class="navbadge">{{ eventList.filter((e) => !e.released).length }}</em>
          </button>
          <button :class="{ active: mainTab === 'flow' }" @click="mainTab = 'flow'">清算</button>
          <button :class="{ active: mainTab === 'upbit' }" @click="mainTab = 'upbit'">
            Upbit 公告<em v-if="upbitNotices.length" class="navbadge">{{ upbitNotices.length }}</em>
          </button>
          <button :class="{ active: mainTab === 'news' }" @click="mainTab = 'news'; loadNews()">
            市場快訊<em v-if="news.length" class="navbadge">{{ news.length }}</em>
          </button>
          <button :class="{ active: mainTab === 'funding' }" @click="mainTab = 'funding'; loadFunding()">資金費率</button>
          <button :class="{ active: mainTab === 'unlock' }" @click="mainTab = 'unlock'; loadUnlock()">代幣解鎖</button>
          <button :class="{ active: mainTab === 'articles' }" @click="mainTab = 'articles'; articleView = null">
            文章專欄<em v-if="articles.length" class="navbadge">{{ articles.length }}</em>
          </button>
        </div>
      </div>
      <div class="navrow" v-if="can('member')">
        <span class="navgroup">會員</span>
        <div class="navbtns">
          <button :class="{ active: mainTab === 'oi' }" @click="mainTab = 'oi'">OI 儀表板</button>
          <button :class="{ active: mainTab === 'signals' }" @click="mainTab = 'signals'">
            數據訊號<em v-if="signals.length" class="navbadge">{{ signals.length }}</em>
          </button>
          <button :class="{ active: mainTab === 'scorelog' }" @click="mainTab = 'scorelog'">
            訊號紀錄<em v-if="scoreLog.length" class="navbadge">{{ scoreLog.length }}</em>
          </button>
          <button :class="{ active: mainTab === 'radar' }" @click="mainTab = 'radar'">爆發雷達</button>
        </div>
      </div>
      <div class="navrow" v-if="can('vip')">
        <span class="navgroup">VIP</span>
        <div class="navbtns">
          <button :class="{ active: mainTab === 'paper' }" @click="mainTab = 'paper'">
            星軌<em v-if="paper && paper.open.length" class="navbadge">{{ paper.open.length }}</em>
          </button>
          <button :class="{ active: mainTab === 'gamble' }" @click="mainTab = 'gamble'">
            超新星<em v-if="gamble && gamble.open.length" class="navbadge">{{ gamble.open.length }}</em>
          </button>
          <button :class="{ active: mainTab === 'emaonly' }" @click="mainTab = 'emaonly'">
            銀河<em v-if="emaOnly && emaOnly.open.length" class="navbadge">{{ emaOnly.open.length }}</em>
          </button>
          <button :class="{ active: mainTab === 'sr' }" @click="mainTab = 'sr'; loadSR()">支撐壓力</button>
        </div>
      </div>
      <div class="navrow" v-if="can('admin')">
        <span class="navgroup">管理</span>
        <div class="navbtns">
          <button :class="{ active: mainTab === 'admin' }" @click="mainTab = 'admin'; loadUsers(); loadCfgEditor()">
            後台<em v-if="users.length" class="navbadge">{{ users.length }}</em>
          </button>
          <button :class="{ active: mainTab === 'gamblehedge' }" @click="mainTab = 'gamblehedge'; loadGambleHedge()">
            超新星·保本<em v-if="gambleHedge && gambleHedge.open.length" class="navbadge">{{ gambleHedge.open.length }}</em>
          </button>
          <button :class="{ active: mainTab === 'pool' }" @click="mainTab = 'pool'; loadPool()">
            30幣掃描池<em v-if="pool && pool.open.length" class="navbadge">{{ pool.open.length }}</em>
          </button>
          <button :class="{ active: mainTab === 'conv' }" @click="mainTab = 'conv'; loadConv()">
            均線收斂<em v-if="conv && conv.open.length" class="navbadge">{{ conv.open.length }}</em>
          </button>
          <button :class="{ active: mainTab === 'rsifade' }" @click="mainTab = 'rsifade'; loadRsifade()">
            逆勢超買空<em v-if="rsifade && rsifade.open.length" class="navbadge">{{ rsifade.open.length }}</em>
          </button>
          <button :class="{ active: mainTab === 'bollfade' }" @click="mainTab = 'bollfade'; loadBollfade()">
            布林重回<em v-if="bollfade && bollfade.open.length" class="navbadge">{{ bollfade.open.length }}</em>
          </button>
          <button :class="{ active: mainTab === 'meanrev' }" @click="mainTab = 'meanrev'; loadMeanrev()">
            乖離回歸<em v-if="meanrev && meanrev.open.length" class="navbadge">{{ meanrev.open.length }}</em>
          </button>
        </div>
      </div>
    </nav>

    <!-- 綜合排行 Top 10 (public, scores only) -->
    <section v-if="mainTab === 'ranking'">
      <div class="mk-head">
        <h2>綜合評分排行榜<span class="help" tabindex="0">?<span class="help-pop">綜合評分(OI 變化率 + CVD 趨勢 + 結構 + 動能 + 費率…)。公開版只提供<b>數據與分數</b>,<b>不提供進場/止盈止損點位</b>。⚠️ 非投資建議。</span></span></h2>
        <span class="mk-count" v-if="ranking && ranking.updated_at">每小時更新 · {{ new Date(ranking.updated_at).toLocaleTimeString() }}</span>
      </div>
      <div class="rank-grid" v-if="ranking">
        <section class="card">
          <h3 class="psub"><span class="led long"></span>多頭 Top 10</h3>
          <table class="grid">
            <thead><tr><th>#</th><th>幣種</th><th class="r">綜合分</th><th class="r">OI 1h%</th><th class="r">CVD%</th></tr></thead>
            <tbody>
              <tr v-for="(r, i) in ranking.long" :key="r.coin">
                <td class="rank">{{ i + 1 }}</td><td class="coin">{{ r.coin }}</td>
                <td class="r score long"><b>{{ r.score }}</b></td>
                <td class="r" :class="r.oi_chg_1h >= 0 ? 'long' : 'short'">{{ r.oi_chg_1h?.toFixed(2) }}</td>
                <td class="r" :class="r.cvd_ratio >= 0 ? 'long' : 'short'">{{ r.cvd_ratio?.toFixed(2) }}</td>
              </tr>
            </tbody>
          </table>
        </section>
        <section class="card">
          <h3 class="psub"><span class="led short"></span>空頭 Top 10</h3>
          <table class="grid">
            <thead><tr><th>#</th><th>幣種</th><th class="r">綜合分</th><th class="r">OI 1h%</th><th class="r">CVD%</th></tr></thead>
            <tbody>
              <tr v-for="(r, i) in ranking.short" :key="r.coin">
                <td class="rank">{{ i + 1 }}</td><td class="coin">{{ r.coin }}</td>
                <td class="r score short"><b>{{ r.score }}</b></td>
                <td class="r" :class="r.oi_chg_1h >= 0 ? 'long' : 'short'">{{ r.oi_chg_1h?.toFixed(2) }}</td>
                <td class="r" :class="r.cvd_ratio >= 0 ? 'long' : 'short'">{{ r.cvd_ratio?.toFixed(2) }}</td>
              </tr>
            </tbody>
          </table>
        </section>
      </div>
      <p v-else class="loading">載入排行榜中…</p>
      <p v-if="role === 'public'" class="radar-note" style="margin-top:14px">
        🔒 想看 <b>OI 儀表板、爆發雷達、VIP 量化訊號(含進出場)</b>?請<b @click="loginOpen = true" style="cursor:pointer;text-decoration:underline">登入</b>會員/VIP。申請方式見公告。
      </p>
    </section>

    <!-- 支撐壓力 (VIP) -->
    <section v-else-if="mainTab === 'sr' && can('vip')">
      <div class="mk-head">
        <h2>支撐壓力<span class="help" tabindex="0">?<span class="help-pop">追蹤系統常駐的主流永續幣種(1h 週期)。用左右各 3 根的分形法找 swing low/high,價差 0.4% 內併為一群,取被測 ≥3 次的最近關卡。<b>僅提示,不進場、無止盈止損</b>:最新 1h 收盤跌破支撐或突破壓力時推播(TG + 軟體)。⚠️ 僅供參考,非投資建議。</span></span></h2>
        <span class="mk-count" v-if="sr && sr.levels">{{ sr.levels.length }} 檔有關卡 · 每根 1h 收盤更新</span>
      </div>

      <div class="sup-cards" v-if="sr">
        <div v-for="c in sr.levels" :key="c.coin" class="sup-card"
             :class="{ broken: c.status === 'break_down', broke: c.status === 'break_up' }">
          <div class="sup-coin">{{ c.coin }}</div>
          <div class="sup-price">現價 {{ c.price ? fmtPrice(c.price) : '—' }}</div>
          <div class="sup-level">壓力 <b class="short">{{ c.res_ok ? fmtPrice(c.resistance) : '—' }}</b><small v-if="c.res_ok"> ×{{ c.res_touches }}</small></div>
          <div class="sup-level">支撐 <b class="long">{{ c.sup_ok ? fmtPrice(c.support) : '—' }}</b><small v-if="c.sup_ok"> ×{{ c.sup_touches }}</small></div>
          <div class="sup-tag" :class="(srStatusMeta[c.status] || {}).cls">{{ (srStatusMeta[c.status] || {}).txt || '—' }}</div>
        </div>
      </div>
      <p v-else class="loading">載入中…(首個 1h 收盤週期後才會建立支撐壓力)</p>
      <p class="loginhint" style="margin-top:12px">跌破支撐或突破壓力時,會即時推播給 VIP(需在裝置開啟通知)。</p>
    </section>

    <!-- 30幣掃描池 1H (admin only) -->
    <section v-else-if="mainTab === 'pool' && can('admin')">
      <div class="mk-head">
        <h2>30幣掃描池 · 1H<span class="help" tabindex="0">?<span class="help-pop"><b>進場條件</b><br>EMA50 上穿 EMA200 金叉,且收盤 > EMA800(= 4H 的 EMA200)。<br><br><b>出場</b><br>持倉最高收盤 −8×ATR 吊燈停損,或 EMA50 下穿 EMA200(死叉)。<br><b>早鎖利</b>:浮盈達 +2×ATR 後,止損下限上移至 進場+0.5×ATR,之後吊燈續跟蹤。<br><b>無固定止盈</b>——跟著吊燈移動停損吃趨勢,表格「動態止損」即當前停損位。<br><br>掃描池=成交量前 30 檔;做多。進場每根 1H 收盤評估;停損以即時價執行、死叉以 1H 收盤判定。⚠️ 管理員專屬模擬單,非投資建議。</span></span></h2>
        <span class="mk-actions"><span class="mk-count" v-if="pool">進行中 {{ pool.open.length }} · 已結束 {{ pool.stats.closed }}</span><button class="clearbtn" @click="clearStrat('pool', loadPool)">清空</button></span>
      </div>
      <div v-if="pool" class="pstats">
        <div class="pstat"><div class="stat-k">已結束</div><div class="stat-v">{{ pool.stats.closed }}</div></div>
        <div class="pstat"><div class="stat-k">勝率</div><div class="stat-v" :class="pool.stats.win_rate >= 50 ? 'long' : 'short'">{{ pool.stats.win_rate }}%</div></div>
        <div class="pstat"><div class="stat-k">平均損益</div><div class="stat-v" :class="pool.stats.avg_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(pool.stats.avg_pnl) }}</div></div>
        <div class="pstat"><div class="stat-k">累計損益</div><div class="stat-v" :class="pool.stats.total_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(pool.stats.total_pnl) }}</div></div>
      </div>

      <h3 class="psub" v-if="pool && pool.open.length">進行中 ({{ pool.open.length }})</h3>
      <table v-if="pool && pool.open.length" class="grid">
        <thead><tr><th>幣種</th><th>方向</th><th class="r">進場</th><th class="r">現價</th><th class="r">損益%</th><th class="r" title="吊燈/早鎖利動態停損位(隨行情上移)">動態止損</th><th class="r">進場時間</th></tr></thead>
        <tbody>
          <tr v-for="t in pool.open" :key="t.coin + t.open_time" class="clickable" @click="openDetail(t.coin)">
            <td class="coin">{{ t.coin }}</td>
            <td><span class="dir long">做多</span></td>
            <td class="r">{{ fmtPrice(t.entry) }}</td>
            <td class="r">{{ fmtPrice(t.cur) }}</td>
            <td class="r" :class="t.pnl_pct >= 0 ? 'long' : 'short'"><b>{{ fmtPct(t.pnl_pct) }}</b></td>
            <td class="r short">{{ t.sl ? fmtPrice(t.sl) : '—' }}<small v-if="t.sl && t.sl >= t.entry" class="vtag"> 鎖利</small></td>
            <td class="r tsmall">{{ fmtClock(t.open_time) }}</td>
          </tr>
        </tbody>
      </table>

      <h3 class="psub" v-if="pool && pool.closed.length">已結束 ({{ pool.closed.length }})</h3>
      <table v-if="pool && pool.closed.length" class="grid">
        <thead><tr><th>幣種</th><th class="r">進場</th><th class="r">出場</th><th>結果</th><th class="r">損益%</th><th class="r">出場時間</th></tr></thead>
        <tbody>
          <tr v-for="(t, i) in pool.closed" :key="i" class="clickable" @click="openDetail(t.coin)">
            <td class="coin">{{ t.coin }}</td>
            <td class="r">{{ fmtPrice(t.entry) }}</td>
            <td class="r">{{ fmtPrice(t.cur) }}</td>
            <td><span class="otag" :class="t.outcome === 'lock' ? 'tp' : t.outcome === 'signal' ? 'reversed' : 'sl'">{{ poolOutcome(t.outcome) }}</span></td>
            <td class="r" :class="t.pnl_pct >= 0 ? 'long' : 'short'"><b>{{ fmtPct(t.pnl_pct) }}</b></td>
            <td class="r tsmall">{{ fmtClock(t.close_time) }}</td>
          </tr>
        </tbody>
      </table>

      <p v-if="pool && !pool.open.length && !pool.closed.length" class="loading">尚無訊號——需等 1H 收盤觸發金叉(首次啟動需抓取歷史 K 線)。</p>
      <p v-else-if="!pool" class="loading">載入中…</p>
    </section>

    <!-- 動態ATR 4H 均線收斂 (admin only) -->
    <section v-else-if="mainTab === 'conv' && can('admin')">
      <div class="mk-head">
        <h2>動態ATR 均線收斂 · 4H<span class="help" tabindex="0">?<span class="help-pop"><b>進場</b><br>4H 價格在 EMA200 同側 + 連續 4 根橫盤(區間 ≤ 3×ATR)+ 該 4 根靠 EMA200 的極值離 EMA200 ≤ 1.5×ATR(動態空間,取代固定 3%)。<br><b>止損</b>:4 根盤整區極值 ±0.3×ATR(結構止損+掃針緩衝)。<br><b>止盈</b>:成交量輪廓(VRVP 近似)——進場上方(多)/下方(空)最近的高量節點(POC/大量區)。<br><b>濾網</b>:盈虧比(TP距/SL距)≥ 1.5 才開倉。<br><br>多空雙向、每根 4H 收盤評估。⚠️ POC 為 K 線近似,管理員專屬模擬單,非投資建議。</span></span></h2>
        <span class="mk-actions"><span class="mk-count" v-if="conv">進行中 {{ conv.open.length }} · 已結束 {{ conv.stats.closed }}</span><button class="clearbtn" @click="clearStrat('conv', loadConv)">清空</button></span>
      </div>
      <div v-if="conv" class="pstats">
        <div class="pstat"><div class="stat-k">已結束</div><div class="stat-v">{{ conv.stats.closed }}</div></div>
        <div class="pstat"><div class="stat-k">勝率</div><div class="stat-v" :class="conv.stats.win_rate >= 50 ? 'long' : 'short'">{{ conv.stats.win_rate }}%</div></div>
        <div class="pstat"><div class="stat-k">平均損益</div><div class="stat-v" :class="conv.stats.avg_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(conv.stats.avg_pnl) }}</div></div>
        <div class="pstat"><div class="stat-k">累計損益</div><div class="stat-v" :class="conv.stats.total_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(conv.stats.total_pnl) }}</div></div>
      </div>

      <h3 class="psub" v-if="conv && conv.open.length">進行中 ({{ conv.open.length }})</h3>
      <table v-if="conv && conv.open.length" class="grid">
        <thead><tr><th>幣種</th><th>方向</th><th class="r">進場</th><th class="r">現價</th><th class="r">損益%</th><th class="r">止盈</th><th class="r">止損</th><th class="r">進場時間</th></tr></thead>
        <tbody>
          <tr v-for="t in conv.open" :key="t.coin + t.open_time" class="clickable" @click="openDetail(t.coin)">
            <td class="coin">{{ t.coin }}</td>
            <td><span class="dir" :class="t.dir === 'long' ? 'long' : 'short'">{{ t.dir === 'long' ? '做多' : '做空' }}</span></td>
            <td class="r">{{ fmtPrice(t.entry) }}</td>
            <td class="r">{{ fmtPrice(t.cur) }}</td>
            <td class="r" :class="t.pnl_pct >= 0 ? 'long' : 'short'"><b>{{ fmtPct(t.pnl_pct) }}</b></td>
            <td class="r long">{{ fmtPrice(t.tp) }}</td>
            <td class="r short">{{ fmtPrice(t.sl) }}</td>
            <td class="r tsmall">{{ fmtClock(t.open_time) }}</td>
          </tr>
        </tbody>
      </table>

      <h3 class="psub" v-if="conv && conv.closed.length">已結束 ({{ conv.closed.length }})</h3>
      <table v-if="conv && conv.closed.length" class="grid">
        <thead><tr><th>幣種</th><th>方向</th><th class="r">進場</th><th class="r">出場</th><th>結果</th><th class="r">損益%</th><th class="r">出場時間</th></tr></thead>
        <tbody>
          <tr v-for="(t, i) in conv.closed" :key="i" class="clickable" @click="openDetail(t.coin)">
            <td class="coin">{{ t.coin }}</td>
            <td><span class="dir" :class="t.dir === 'long' ? 'long' : 'short'">{{ t.dir === 'long' ? '做多' : '做空' }}</span></td>
            <td class="r">{{ fmtPrice(t.entry) }}</td>
            <td class="r">{{ fmtPrice(t.cur) }}</td>
            <td><span class="otag" :class="t.outcome === 'tp' ? 'tp' : t.outcome === 'sl' ? 'sl' : 'expired'">{{ convOutcome(t.outcome) }}</span></td>
            <td class="r" :class="t.pnl_pct >= 0 ? 'long' : 'short'"><b>{{ fmtPct(t.pnl_pct) }}</b></td>
            <td class="r tsmall">{{ fmtClock(t.close_time) }}</td>
          </tr>
        </tbody>
      </table>

      <p v-if="conv && !conv.open.length && !conv.closed.length" class="loading">尚無訊號——需等 4H 收盤出現橫盤收斂 + 盈虧比達標(首次啟動需抓取歷史 K 線)。</p>
      <p v-else-if="!conv" class="loading">載入中…</p>
    </section>

    <!-- 逆勢超買空 · 30m (admin only) -->
    <section v-else-if="mainTab === 'rsifade' && can('admin')">
      <div class="mk-head">
        <h2>逆勢超買空 · 30m<span class="help" tabindex="0">?<span class="help-pop"><b>進場</b>:RSI(3) &gt; 90 且收盤價 &lt; EMA200(空頭中的反彈)→ 收盤<b>放空</b>。<br><b>止損</b> +2.5×ATR,<b>止盈</b> −2.0×ATR。<br>最多持有 16 根(30m),出場後冷卻 4 根。只做空;進場以 30m 收盤判定,止盈止損以即時價執行。<br><br>管理員專屬模擬單,⚠️ 非投資建議。</span></span></h2>
        <span class="mk-actions"><span class="mk-count" v-if="rsifade">進行中 {{ rsifade.open.length }} · 已結束 {{ rsifade.stats.closed }}</span><button class="clearbtn" @click="clearStrat('rsifade', loadRsifade)">清空</button></span>
      </div>
      <div v-if="rsifade" class="pstats">
        <div class="pstat"><div class="stat-k">已結束</div><div class="stat-v">{{ rsifade.stats.closed }}</div></div>
        <div class="pstat"><div class="stat-k">勝率</div><div class="stat-v" :class="rsifade.stats.win_rate >= 50 ? 'long' : 'short'">{{ rsifade.stats.win_rate }}%</div></div>
        <div class="pstat"><div class="stat-k">平均損益</div><div class="stat-v" :class="rsifade.stats.avg_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(rsifade.stats.avg_pnl) }}</div></div>
        <div class="pstat"><div class="stat-k">累計損益</div><div class="stat-v" :class="rsifade.stats.total_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(rsifade.stats.total_pnl) }}</div></div>
      </div>
      <h3 class="psub" v-if="rsifade && rsifade.open.length">進行中 ({{ rsifade.open.length }})</h3>
      <table v-if="rsifade && rsifade.open.length" class="grid">
        <thead><tr><th>幣種</th><th>方向</th><th class="r">進場</th><th class="r">現價</th><th class="r">損益%</th><th class="r">止盈</th><th class="r">止損</th><th class="r">進場時間</th></tr></thead>
        <tbody>
          <tr v-for="t in rsifade.open" :key="t.coin + t.open_time" class="clickable" @click="openDetail(t.coin)">
            <td class="coin">{{ t.coin }}</td>
            <td><span class="dir" :class="t.dir === 'long' ? 'long' : 'short'">{{ t.dir === 'long' ? '做多' : '做空' }}</span></td>
            <td class="r">{{ fmtPrice(t.entry) }}</td>
            <td class="r">{{ fmtPrice(t.cur) }}</td>
            <td class="r" :class="t.pnl_pct >= 0 ? 'long' : 'short'"><b>{{ fmtPct(t.pnl_pct) }}</b></td>
            <td class="r long">{{ fmtPrice(t.tp) }}</td>
            <td class="r short">{{ fmtPrice(t.sl) }}</td>
            <td class="r tsmall">{{ fmtClock(t.open_time) }}</td>
          </tr>
        </tbody>
      </table>
      <h3 class="psub" v-if="rsifade && rsifade.closed.length">已結束 ({{ rsifade.closed.length }})</h3>
      <table v-if="rsifade && rsifade.closed.length" class="grid">
        <thead><tr><th>幣種</th><th>方向</th><th class="r">進場</th><th class="r">出場</th><th>結果</th><th class="r">損益%</th><th class="r">出場時間</th></tr></thead>
        <tbody>
          <tr v-for="(t, i) in rsifade.closed" :key="i" class="clickable" @click="openDetail(t.coin)">
            <td class="coin">{{ t.coin }}</td>
            <td><span class="dir" :class="t.dir === 'long' ? 'long' : 'short'">{{ t.dir === 'long' ? '做多' : '做空' }}</span></td>
            <td class="r">{{ fmtPrice(t.entry) }}</td>
            <td class="r">{{ fmtPrice(t.cur) }}</td>
            <td><span class="otag" :class="t.outcome === 'tp' ? 'tp' : t.outcome === 'sl' ? 'sl' : 'expired'">{{ convOutcome(t.outcome) }}</span></td>
            <td class="r" :class="t.pnl_pct >= 0 ? 'long' : 'short'"><b>{{ fmtPct(t.pnl_pct) }}</b></td>
            <td class="r tsmall">{{ fmtClock(t.close_time) }}</td>
          </tr>
        </tbody>
      </table>
      <p v-if="rsifade && !rsifade.open.length && !rsifade.closed.length" class="loading">尚無訊號——需等 30m 收盤觸發 RSI(3)&gt;90 空頭反彈(首次啟動需抓取歷史 K 線)。</p>
      <p v-else-if="!rsifade" class="loading">載入中…</p>
    </section>

    <!-- 布林重回 · 1h (admin only) -->
    <section v-else-if="mainTab === 'bollfade' && can('admin')">
      <div class="mk-head">
        <h2>布林重回 · 1h<span class="help" tabindex="0">?<span class="help-pop"><b>進場</b>:前一根收盤在布林(20, 2σ)通道<b>外</b>、本根收<b>回</b>通道內(過度延伸失敗),且方向與 EMA200 同側(空單需價在 EMA200 下方、多單反之)→ 朝中軌交易。<br><b>止損</b> 2.5×ATR,<b>止盈</b>=中軌(SMA20),盈虧比需 0.4–3.0。<br>多空雙向,最多 24 根,冷卻 4 根;進場以 1h 收盤判定,止盈止損以即時價執行。<br><br>管理員專屬模擬單,⚠️ 非投資建議。</span></span></h2>
        <span class="mk-actions"><span class="mk-count" v-if="bollfade">進行中 {{ bollfade.open.length }} · 已結束 {{ bollfade.stats.closed }}</span><button class="clearbtn" @click="clearStrat('bollfade', loadBollfade)">清空</button></span>
      </div>
      <div v-if="bollfade" class="pstats">
        <div class="pstat"><div class="stat-k">已結束</div><div class="stat-v">{{ bollfade.stats.closed }}</div></div>
        <div class="pstat"><div class="stat-k">勝率</div><div class="stat-v" :class="bollfade.stats.win_rate >= 50 ? 'long' : 'short'">{{ bollfade.stats.win_rate }}%</div></div>
        <div class="pstat"><div class="stat-k">平均損益</div><div class="stat-v" :class="bollfade.stats.avg_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(bollfade.stats.avg_pnl) }}</div></div>
        <div class="pstat"><div class="stat-k">累計損益</div><div class="stat-v" :class="bollfade.stats.total_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(bollfade.stats.total_pnl) }}</div></div>
      </div>
      <h3 class="psub" v-if="bollfade && bollfade.open.length">進行中 ({{ bollfade.open.length }})</h3>
      <table v-if="bollfade && bollfade.open.length" class="grid">
        <thead><tr><th>幣種</th><th>方向</th><th class="r">進場</th><th class="r">現價</th><th class="r">損益%</th><th class="r">止盈</th><th class="r">止損</th><th class="r">進場時間</th></tr></thead>
        <tbody>
          <tr v-for="t in bollfade.open" :key="t.coin + t.open_time" class="clickable" @click="openDetail(t.coin)">
            <td class="coin">{{ t.coin }}</td>
            <td><span class="dir" :class="t.dir === 'long' ? 'long' : 'short'">{{ t.dir === 'long' ? '做多' : '做空' }}</span></td>
            <td class="r">{{ fmtPrice(t.entry) }}</td>
            <td class="r">{{ fmtPrice(t.cur) }}</td>
            <td class="r" :class="t.pnl_pct >= 0 ? 'long' : 'short'"><b>{{ fmtPct(t.pnl_pct) }}</b></td>
            <td class="r long">{{ fmtPrice(t.tp) }}</td>
            <td class="r short">{{ fmtPrice(t.sl) }}</td>
            <td class="r tsmall">{{ fmtClock(t.open_time) }}</td>
          </tr>
        </tbody>
      </table>
      <h3 class="psub" v-if="bollfade && bollfade.closed.length">已結束 ({{ bollfade.closed.length }})</h3>
      <table v-if="bollfade && bollfade.closed.length" class="grid">
        <thead><tr><th>幣種</th><th>方向</th><th class="r">進場</th><th class="r">出場</th><th>結果</th><th class="r">損益%</th><th class="r">出場時間</th></tr></thead>
        <tbody>
          <tr v-for="(t, i) in bollfade.closed" :key="i" class="clickable" @click="openDetail(t.coin)">
            <td class="coin">{{ t.coin }}</td>
            <td><span class="dir" :class="t.dir === 'long' ? 'long' : 'short'">{{ t.dir === 'long' ? '做多' : '做空' }}</span></td>
            <td class="r">{{ fmtPrice(t.entry) }}</td>
            <td class="r">{{ fmtPrice(t.cur) }}</td>
            <td><span class="otag" :class="t.outcome === 'tp' ? 'tp' : t.outcome === 'sl' ? 'sl' : 'expired'">{{ convOutcome(t.outcome) }}</span></td>
            <td class="r" :class="t.pnl_pct >= 0 ? 'long' : 'short'"><b>{{ fmtPct(t.pnl_pct) }}</b></td>
            <td class="r tsmall">{{ fmtClock(t.close_time) }}</td>
          </tr>
        </tbody>
      </table>
      <p v-if="bollfade && !bollfade.open.length && !bollfade.closed.length" class="loading">尚無訊號——需等 1H 收盤出現布林通道重回 + 盈虧比達標(首次啟動需抓取歷史 K 線)。</p>
      <p v-else-if="!bollfade" class="loading">載入中…</p>
    </section>

    <!-- 乖離回歸 · 1h (admin only) -->
    <section v-else-if="mainTab === 'meanrev' && can('admin')">
      <div class="mk-head">
        <h2>乖離回歸 · 1h<span class="help" tabindex="0">?<span class="help-pop"><b>進場</b>:收盤價偏離 EMA20 超過 2.0×ATR,且與 EMA200 趨勢同側(價在 EMA200 上方只接多、下方只接空)→ 朝 EMA20 回歸。<br><b>止損</b> 3.0×ATR,<b>止盈</b>=EMA20。<br>多空雙向,最多 24 根,冷卻 4 根;進場以 1h 收盤判定,止盈止損以即時價執行。<br><br>管理員專屬模擬單,⚠️ 非投資建議。</span></span></h2>
        <span class="mk-actions"><span class="mk-count" v-if="meanrev">進行中 {{ meanrev.open.length }} · 已結束 {{ meanrev.stats.closed }}</span><button class="clearbtn" @click="clearStrat('meanrev', loadMeanrev)">清空</button></span>
      </div>
      <div v-if="meanrev" class="pstats">
        <div class="pstat"><div class="stat-k">已結束</div><div class="stat-v">{{ meanrev.stats.closed }}</div></div>
        <div class="pstat"><div class="stat-k">勝率</div><div class="stat-v" :class="meanrev.stats.win_rate >= 50 ? 'long' : 'short'">{{ meanrev.stats.win_rate }}%</div></div>
        <div class="pstat"><div class="stat-k">平均損益</div><div class="stat-v" :class="meanrev.stats.avg_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(meanrev.stats.avg_pnl) }}</div></div>
        <div class="pstat"><div class="stat-k">累計損益</div><div class="stat-v" :class="meanrev.stats.total_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(meanrev.stats.total_pnl) }}</div></div>
      </div>
      <h3 class="psub" v-if="meanrev && meanrev.open.length">進行中 ({{ meanrev.open.length }})</h3>
      <table v-if="meanrev && meanrev.open.length" class="grid">
        <thead><tr><th>幣種</th><th>方向</th><th class="r">進場</th><th class="r">現價</th><th class="r">損益%</th><th class="r">止盈</th><th class="r">止損</th><th class="r">進場時間</th></tr></thead>
        <tbody>
          <tr v-for="t in meanrev.open" :key="t.coin + t.open_time" class="clickable" @click="openDetail(t.coin)">
            <td class="coin">{{ t.coin }}</td>
            <td><span class="dir" :class="t.dir === 'long' ? 'long' : 'short'">{{ t.dir === 'long' ? '做多' : '做空' }}</span></td>
            <td class="r">{{ fmtPrice(t.entry) }}</td>
            <td class="r">{{ fmtPrice(t.cur) }}</td>
            <td class="r" :class="t.pnl_pct >= 0 ? 'long' : 'short'"><b>{{ fmtPct(t.pnl_pct) }}</b></td>
            <td class="r long">{{ fmtPrice(t.tp) }}</td>
            <td class="r short">{{ fmtPrice(t.sl) }}</td>
            <td class="r tsmall">{{ fmtClock(t.open_time) }}</td>
          </tr>
        </tbody>
      </table>
      <h3 class="psub" v-if="meanrev && meanrev.closed.length">已結束 ({{ meanrev.closed.length }})</h3>
      <table v-if="meanrev && meanrev.closed.length" class="grid">
        <thead><tr><th>幣種</th><th>方向</th><th class="r">進場</th><th class="r">出場</th><th>結果</th><th class="r">損益%</th><th class="r">出場時間</th></tr></thead>
        <tbody>
          <tr v-for="(t, i) in meanrev.closed" :key="i" class="clickable" @click="openDetail(t.coin)">
            <td class="coin">{{ t.coin }}</td>
            <td><span class="dir" :class="t.dir === 'long' ? 'long' : 'short'">{{ t.dir === 'long' ? '做多' : '做空' }}</span></td>
            <td class="r">{{ fmtPrice(t.entry) }}</td>
            <td class="r">{{ fmtPrice(t.cur) }}</td>
            <td><span class="otag" :class="t.outcome === 'tp' ? 'tp' : t.outcome === 'sl' ? 'sl' : 'expired'">{{ convOutcome(t.outcome) }}</span></td>
            <td class="r" :class="t.pnl_pct >= 0 ? 'long' : 'short'"><b>{{ fmtPct(t.pnl_pct) }}</b></td>
            <td class="r tsmall">{{ fmtClock(t.close_time) }}</td>
          </tr>
        </tbody>
      </table>
      <p v-if="meanrev && !meanrev.open.length && !meanrev.closed.length" class="loading">尚無訊號——需等 1H 收盤出現乖離 2×ATR 訊號(首次啟動需抓取歷史 K 線)。</p>
      <p v-else-if="!meanrev" class="loading">載入中…</p>
    </section>

    <!-- 後台管理 (admin only) -->
    <section v-else-if="mainTab === 'admin' && can('admin')">
      <div class="mk-head"><h2>後台 · 使用者管理</h2><span class="mk-count">{{ users.length }} 位</span></div>
      <p v-if="adminMsg" class="admin-msg">{{ adminMsg }}</p>

      <!-- 待審核 -->
      <section v-if="pendingUsers.length" class="card adminbox">
        <h3 class="psub">🟡 待審核 ({{ pendingUsers.length }})</h3>
        <div class="reviewgrid">
          <div v-for="u in pendingUsers" :key="u.username" class="reviewcard">
            <div v-if="u.proof" class="reviewproof" @click="proofView = u.proof"><img :src="u.proof" alt="資產證明" /></div>
            <div v-else class="reviewproof empty">無證明圖</div>
            <div class="reviewinfo">
              <div class="ri-name">{{ u.username }}</div>
              <div class="ri-row">UID:<b>{{ u.uid || '—' }}</b></div>
              <div class="ri-row">交易所:<b>{{ u.notes || '—' }}</b></div>
              <div class="ri-row"><small>{{ u.created ? new Date(u.created).toLocaleString() : '' }}</small></div>
              <div class="reviewact">
                <button class="okbtn" @click="approveUser(u)">✓ 通過</button>
                <button class="nobtn" @click="rejectUser(u)">✕ 駁回</button>
              </div>
            </div>
          </div>
        </div>
      </section>

      <!-- 所有使用者 -->
      <section class="card">
        <h3 class="psub">所有使用者 <span class="mk-count">{{ filteredUsers.length }} / {{ users.length }}</span></h3>
        <div class="userfilter">
          <span class="tf-label">階級</span>
          <button v-for="r in [['all','全部'],['member','會員'],['vip','VIP'],['admin','管理']]" :key="r[0]"
            :class="{ on: userRoleFilter === r[0] }" @click="userRoleFilter = r[0]">{{ r[1] }}</button>
          <span class="tf-label">註冊</span>
          <input type="date" class="datein" v-model="userFrom" :max="userTo || undefined" title="起始日期" />
          <span class="tf-sep">~</span>
          <input type="date" class="datein" v-model="userTo" :min="userFrom || undefined" title="結束日期" />
          <button v-for="t in [[7,'近7天'],[30,'近30天'],[90,'近90天']]" :key="t[0]" @click="setUserDays(t[0])">{{ t[1] }}</button>
          <button v-if="userFrom || userTo" @click="setUserDays(0)">清除</button>
          <span class="tf-label">排序</span>
          <button :class="{ on: userSort === 'new' }" @click="userSort = 'new'">新→舊</button>
          <button :class="{ on: userSort === 'old' }" @click="userSort = 'old'">舊→新</button>
        </div>
        <table class="grid">
          <thead><tr><th>證明</th><th>帳號</th><th>UID / 交易所</th><th>角色</th><th class="r">註冊時間</th><th class="r">VIP</th><th class="r">啟用</th><th class="r">刪除</th></tr></thead>
          <tbody>
            <tr v-for="u in filteredUsers" :key="u.username">
              <td><img v-if="u.proof" :src="u.proof" class="proofthumb" @click="proofView = u.proof" /><span v-else>—</span></td>
              <td class="coin">{{ u.username }}
                <em v-if="u.status === 'pending'" class="qtag warn">審核中</em>
                <em v-else-if="u.status === 'banned'" class="qtag bad">停用</em>
              </td>
              <td class="rl-text"><div>{{ u.uid || '—' }}</div><small>{{ u.notes || '—' }}</small></td>
              <td>{{ u.role }}</td>
              <td class="r tsmall">{{ fmtReg(u.created) }}</td>
              <td class="r">
                <button v-if="u.role !== 'admin'" class="regbtn" :class="{ on: u.role === 'vip' }" @click="toggleVip(u)">{{ u.role === 'vip' ? 'VIP ✓' : '設 VIP' }}</button>
                <em v-else class="qtag">admin</em>
              </td>
              <td class="r">
                <label v-if="u.username !== username && u.role !== 'admin'" class="switch">
                  <input type="checkbox" :checked="u.status === 'active'" @change="toggleEnabled(u)" />
                  <span class="sw-track"></span>
                </label>
                <em v-else class="qtag good">{{ u.username === username ? '本人' : '—' }}</em>
              </td>
              <td class="r">
                <button v-if="u.username !== username" class="delbtn" @click="deleteUser(u)" title="刪除帳號">🗑</button>
                <span v-else>—</span>
              </td>
            </tr>
          </tbody>
        </table>
        <p v-if="!users.length" class="empty">尚無使用者。</p>
        <p v-else-if="!filteredUsers.length" class="empty">此條件下無符合的使用者。</p>
      </section>

      <!-- 手動新增 -->
      <section class="card adminbox">
        <h3 class="psub">手動新增使用者</h3>
        <div class="newuser">
          <input v-model="newUser.u" placeholder="帳號" />
          <input v-model="newUser.p" type="text" placeholder="密碼" />
          <select v-model="newUser.role">
            <option value="member">member</option>
            <option value="vip">vip</option>
            <option value="admin">admin</option>
          </select>
          <select v-model="newUser.status">
            <option value="active">active</option>
            <option value="pending">pending</option>
            <option value="banned">banned</option>
          </select>
          <button class="loginbtn" @click="createUser">新增</button>
        </div>
      </section>

      <!-- 站台設定:logo / 社群 / QR -->
      <section class="card adminbox">
        <h3 class="psub">站台設定</h3>
        <div class="cfg-row">
          <span class="cfg-k">品牌 Logo</span>
          <img v-if="config.logo" :src="config.logo" class="cfg-logo" />
          <label class="authfile cfg-file"><span>上傳 Logo</span><input type="file" accept="image/*,.heic,.heif" hidden @change="onLogoPick" /></label>
          <button v-if="config.logo" class="delbtn" @click="setConfig('logo', '')">清除</button>
        </div>
        <div class="cfg-row">
          <span class="cfg-k">首頁 QR</span>
          <img v-if="config.qr" :src="config.qr" class="cfg-qr" />
          <label class="authfile cfg-file"><span>上傳 QR 圖</span><input type="file" accept="image/*,.heic,.heif" hidden @change="onQrPick" /></label>
          <button v-if="config.qr" class="delbtn" @click="setConfig('qr', '')">清除</button>
        </div>
        <div class="cfg-row">
          <span class="cfg-k">QR 點擊連結</span>
          <input class="authin" :value="config.qr_link || ''" @change="setConfig('qr_link', $event.target.value)" placeholder="選填:點擊 QR 開啟的網址" />
        </div>

        <h4 class="cfg-sub">社群連結 <button class="minibtn" @click="loadCfgEditor">載入目前</button></h4>
        <div v-for="(s, i) in cfgSocial" :key="i" class="cfg-social">
          <span class="cfg-ico" :style="{ background: socialInfo(s.platform).color }"
                v-html="socialSvg(s.platform) || socialInfo(s.platform).icon"></span>
          <select v-model="s.platform">
            <option value="youtube">YouTube</option>
            <option value="telegram">Telegram</option>
            <option value="instagram">Instagram</option>
            <option value="facebook">Facebook</option>
            <option value="line">LINE</option>
            <option value="custom">其他連結</option>
          </select>
          <input class="authin" v-model="s.url" placeholder="https://…" />
          <button class="minibtn del" @click="removeSocial(i)">✕</button>
        </div>
        <div class="ae-addrow">
          <button class="regbtn" @click="addSocial">＋ 新增社群</button>
          <button class="loginbtn" @click="saveSocial">儲存社群</button>
        </div>
        <p class="loginhint">社群會顯示在頁尾(logo 引導跳轉);QR 懸浮在首頁右下角。</p>
      </section>

      <!-- 即時推播 (Feature: broadcast) -->
      <section class="card adminbox">
        <h3 class="psub">即時推播</h3>
        <div class="cfg-row">
          <span class="cfg-k">標題</span>
          <input class="authin" v-model="bcTitle" maxlength="20" placeholder="例:測試" />
        </div>
        <div class="cfg-row">
          <span class="cfg-k">內容</span>
          <input class="authin" v-model="bcBody" maxlength="20" placeholder="20 字內,例:我只是個測試文" />
        </div>
        <div class="cfg-row">
          <span class="cfg-k">用戶組</span>
          <select class="authin cfg-sel" v-model="bcGroup">
            <option value="all">全部</option>
            <option value="member">會員</option>
            <option value="vip">VIP</option>
            <option value="admin">管理員</option>
          </select>
        </div>
        <div class="cfg-row">
          <span class="cfg-k">點擊跳轉</span>
          <select class="authin cfg-sel" v-model="bcArticle">
            <option value="">不跳轉(開啟首頁)</option>
            <option v-for="a in articles" :key="a.id" :value="String(a.id)">📄 {{ a.title }}</option>
          </select>
          <button class="loginbtn" @click="sendBroadcast">發送推播</button>
        </div>
        <p class="loginhint">立即發送 Web Push 給選定用戶組(標題/內容各上限 20 字);可選點擊後跳轉到指定文章。僅發給已開啟通知的裝置。</p>
      </section>

    </section>

    <!-- 合約市場 (幣種一覽) -->
    <section v-else-if="mainTab === 'list' && home">
      <div class="mk-head">
        <h2>合約市場</h2>
        <span class="mk-count">共 {{ home.total }} 個合約，顯示前 {{ home.market.length }}</span>
      </div>
      <div class="sorttabs">
        <button :class="{ active: marketSort === 'vol' }" @click="marketSort = 'vol'">依成交量</button>
        <button :class="{ active: marketSort === 'gainers' }" @click="marketSort = 'gainers'">漲幅榜</button>
        <button :class="{ active: marketSort === 'losers' }" @click="marketSort = 'losers'">跌幅榜</button>
      </div>
      <table class="grid market">
        <thead>
          <tr><th class="rank">#</th><th>幣種</th><th class="r">價格</th><th class="r">漲跌幅</th><th class="r">24H 成交量</th></tr>
        </thead>
        <tbody>
          <tr v-for="(m, i) in market" :key="m.coin" class="clickable" @click="openDetail(m.coin)">
            <td class="rank">{{ i + 1 }}</td>
            <td class="coin">{{ m.coin }}</td>
            <td class="r">{{ fmtPrice(m.price) }}</td>
            <td class="r"><span class="chip" :class="m.chg >= 0 ? 'long' : 'short'">{{ fmtPct(m.chg) }}</span></td>
            <td class="r vol">{{ fmtNum(m.vol) }}</td>
          </tr>
        </tbody>
      </table>
    </section>

    <!-- OI 儀表板 (score board) -->
    <section v-else-if="mainTab === 'oi'">
      <div class="mk-head">
        <h2>OI 儀表板</h2>
        <span class="mk-count" v-if="boardUpdated">更新：{{ new Date(boardUpdated).toLocaleTimeString() }}</span>
      </div>
      <table class="grid">
        <thead>
          <tr><th>幣種</th><th class="r">評分</th><th>方向</th><th>品質</th><th class="r" title="最新 1 小時 K 棒的漲跌%">1H%</th><th class="r" title="未平倉量近 1 小時變化%">OI 1h%</th><th class="r" title="近 12 小時買賣單量差（CVD），正=買方主導">CVD%</th></tr>
        </thead>
        <tbody>
          <tr v-for="r in boardRows" :key="r.coin" class="clickable" :class="{ selected: r.coin === detailCoin }" @click="openDetail(r.coin)">
            <td class="coin">{{ r.coin }}</td>
            <td :class="['r', 'score', biasClass(r.bias)]">{{ r.score }}</td>
            <td :class="biasClass(r.bias)">{{ r.bias === 'long' ? '做多' : r.bias === 'short' ? '做空' : '觀察' }}</td>
            <td>{{ r.quality }}</td>
            <td class="r" :class="r.okx_chg >= 0 ? 'long' : 'short'">{{ r.okx_chg?.toFixed(2) }}</td>
            <td class="r" :class="r.oi_chg_1h >= 0 ? 'long' : 'short'">{{ r.oi_chg_1h?.toFixed(2) }}</td>
            <td class="r" :class="r.cvd_ratio >= 0 ? 'long' : 'short'">{{ r.cvd_ratio?.toFixed(2) }}</td>
          </tr>
        </tbody>
      </table>
    </section>

    <!-- 數據訊號 (actionable entries) -->
    <section v-else-if="mainTab === 'signals'">
      <div class="mk-head">
        <h2>數據訊號</h2>
        <span class="mk-count">{{ signals.length }} 個可進場訊號（評分 ≥ 20 / ≤ −20）<template v-if="regimeFilter"> · 順 BTC 趨勢</template><template v-if="qualityFilter"> · OI 收縮</template></span>
      </div>
      <table v-if="signals.length" class="grid">
        <thead>
          <tr><th>幣種</th><th>方向</th><th class="r">評分</th><th>推薦指數</th><th>品質</th><th class="r">OI 1h%</th><th class="r">CVD%</th></tr>
        </thead>
        <tbody>
          <tr v-for="r in signals" :key="r.coin" class="clickable" :class="{ selected: r.coin === detailCoin }" @click="openDetail(r.coin)">
            <td class="coin">{{ r.coin }}
              <em v-if="isHighQuality(r)" class="qtag hq" title="OI 收縮 + 費率極端(樣本外最佳組)">★優質</em>
              <em v-else-if="oiContracting(r)" class="qtag good" title="OI 收縮(衰竭/平倉,訊號較可靠)">OI↓</em>
              <em v-else class="qtag warn" title="OI 擴張(新倉湧入,追高風險)">OI↑</em>
            </td>
            <td><span class="dir" :class="biasClass(r.bias)">{{ r.bias === 'long' ? '做多' : '做空' }}</span></td>
            <td :class="['r', 'score', biasClass(r.bias)]">{{ r.score }}</td>
            <td>
              <span class="bars">
                <i v-for="n in 5" :key="n" class="bar" :class="{ on: n <= strengthOf(r.score), [biasClass(r.bias)]: n <= strengthOf(r.score) }"></i>
              </span>
            </td>
            <td>{{ r.quality }}</td>
            <td class="r" :class="r.oi_chg_1h >= 0 ? 'long' : 'short'">{{ r.oi_chg_1h?.toFixed(2) }}</td>
            <td class="r" :class="r.cvd_ratio >= 0 ? 'long' : 'short'">{{ r.cvd_ratio?.toFixed(2) }}</td>
          </tr>
        </tbody>
      </table>
      <p v-else class="empty">目前無確定可進場的訊號（沒有任何幣種評分達到 ±20）</p>
    </section>

    <!-- 爆發雷達 (breakout radar, small coins included) -->
    <section v-else-if="mainTab === 'radar'">
      <div class="mk-head">
        <h2>爆發雷達<span class="help" tabindex="0">?<span class="help-pop"><b>點火分數(0–100)</b>：回測驗證的暴噴前兆——「<b>OI 堆積</b>(最強)＋<b>成交量突增</b>＋<b>鯨魚單量</b>＋剛開始微動」，並以 24h 漲幅做「早晚」加權——<b>已經噴一大段的會被降權</b>，讓雷達偏向「<b>剛要發動</b>」而非追高。欄位：<b>量×</b>=近 3h 均量 ÷ 近 48h 均量；<b>OI</b>=未平倉近 12h 變化(堆積)；<b>3H</b>=近 3 小時漲跌。<br>⚠️ 發掘用途、高風險、誤報多，非回測驗證的精準進場訊號。</span></span></h2>
        <span class="mk-count" v-if="radar">掃描 {{ radar.scanned }} 個合約（全市場・含小幣）· 早期優先</span>
      </div>
      <div v-if="radar" class="radar-cols">
        <div class="card">
          <div class="rec-head"><span class="led long"></span>潛在爆衝</div>
          <div class="radar-row rhead"><span>幣種</span><span class="r" title="點火分數 0–100：量增+OI急拉+動能加速+CVD 的綜合強度，越高越可能正在爆發">點火</span><span class="r">24H</span><span class="r" title="近 3h 均量 ÷ 近 48h 均量">量×</span><span class="r" title="未平倉量近 12h 變化(堆積)">OI</span><span class="r" title="近 3 小時漲跌">3H</span></div>
          <div v-for="x in radar.pump" :key="x.coin" class="radar-item clickable" @click="openDetail(x.coin)">
            <div class="radar-row">
              <span class="coin">{{ x.coin }}<small class="vtag">${{ fmtNum(x.vol_24h) }}</small></span>
              <span class="r"><b class="ignite long">{{ x.score }}</b></span>
              <span class="r long">{{ fmtPct(x.chg_24h) }}</span>
              <span class="r">{{ x.vol_spike }}×</span>
              <span class="r" :class="x.oi_chg >= 0 ? 'long' : 'short'">{{ x.oi_chg >= 0 ? '+' : '' }}{{ x.oi_chg }}%</span>
              <span class="r long">{{ fmtPct(x.accel) }}</span>
            </div>
            <div class="radar-entry">現價 <b>{{ fmtPrice(x.price) }}</b> · 止盈 <b class="long">{{ fmtPrice(x.tp) }}</b> · 止損 <b class="short">{{ fmtPrice(x.sl) }}</b></div>
          </div>
          <p v-if="!radar.pump.length" class="empty">目前無爆衝候選</p>
        </div>
        <div class="card">
          <div class="rec-head"><span class="led short"></span>潛在暴跌</div>
          <div class="radar-row rhead"><span>幣種</span><span class="r" title="點火分數 0–100：量增+OI急拉+動能加速+CVD 的綜合強度，越高越可能正在爆發">點火</span><span class="r">24H</span><span class="r" title="近 3h 均量 ÷ 近 48h 均量">量×</span><span class="r" title="未平倉量近 12h 變化(堆積)">OI</span><span class="r" title="近 3 小時漲跌">3H</span></div>
          <div v-for="x in radar.dump" :key="x.coin" class="radar-item clickable" @click="openDetail(x.coin)">
            <div class="radar-row">
              <span class="coin">{{ x.coin }}<small class="vtag">${{ fmtNum(x.vol_24h) }}</small></span>
              <span class="r"><b class="ignite short">{{ x.score }}</b></span>
              <span class="r" :class="x.chg_24h >= 0 ? 'long' : 'short'">{{ fmtPct(x.chg_24h) }}</span>
              <span class="r">{{ x.vol_spike }}×</span>
              <span class="r" :class="x.oi_chg >= 0 ? 'long' : 'short'">{{ x.oi_chg >= 0 ? '+' : '' }}{{ x.oi_chg }}%</span>
              <span class="r short">{{ fmtPct(x.accel) }}</span>
            </div>
            <div class="radar-entry">現價 <b>{{ fmtPrice(x.price) }}</b> · 止盈 <b class="long">{{ fmtPrice(x.tp) }}</b> · 止損 <b class="short">{{ fmtPrice(x.sl) }}</b></div>
          </div>
          <p v-if="!radar.dump.length" class="empty">目前無暴跌候選</p>
        </div>
      </div>
      <p v-else class="loading">雷達掃描中…</p>
    </section>

    <!-- 訊號追蹤 (paper-trading from radar signals) -->
    <!-- 訊號紀錄 (when score crossed ±20) -->
    <section v-else-if="mainTab === 'scorelog'">
      <div class="mk-head">
        <h2>訊號紀錄<span class="help" tabindex="0">?<span class="help-pop">每當追蹤幣種的評分從 &lt;20 跨到 ≥20(或 ≤−20)就記錄當下的時間與價格,方便你回去對照那個時間點的線圖。資料持久保存,重啟不流失。</span></span></h2>
        <span class="mk-count">每次評分跨過 ±20(進入做多/做空)的時間點 · 顯示 {{ scoreLogF.length }} / {{ scoreLog.length }} 筆</span>
      </div>
      <div class="timefilter">
        <span class="tf-label">時間範圍</span>
        <button v-for="p in timePresets" :key="p.ms" :class="{ on: timeWin === p.ms }" @click="timeWin = p.ms">{{ p.label }}</button>
      </div>
      <table v-if="scoreLogF.length" class="grid">
        <thead><tr><th>時間</th><th>幣種</th><th>方向</th><th class="r">評分</th><th class="r">當時價格</th></tr></thead>
        <tbody>
          <tr v-for="(e, i) in scoreLogF" :key="i" class="clickable" @click="openDetail(e.coin)">
            <td class="tsmall">{{ fmtClock(e.time) }}</td>
            <td class="coin">{{ e.coin }}</td>
            <td><span class="dir" :class="e.bias === 'long' ? 'long' : 'short'">{{ e.bias === 'long' ? '做多' : '做空' }}</span></td>
            <td class="r" :class="['score', e.bias === 'long' ? 'long' : 'short']">{{ e.score }}</td>
            <td class="r">{{ fmtPrice(e.price) }}</td>
          </tr>
        </tbody>
      </table>
      <p v-else class="empty">{{ scoreLog.length ? '此時間範圍內無紀錄' : '尚無紀錄（剛啟動需等有幣種評分跨過 ±20）' }}</p>
    </section>

    <section v-else-if="mainTab === 'paper' || mainTab === 'gamble' || mainTab === 'gamblehedge' || mainTab === 'emaonly'">
      <div class="mk-head">
        <h2>{{ mainTab === 'gamble' ? '超新星' : mainTab === 'gamblehedge' ? '超新星·保本(管理)' : mainTab === 'emaonly' ? '銀河' : '星軌' }}<span class="help" tabindex="0">?<span class="help-pop"><template v-if="mainTab === 'gamblehedge'">管理員 A/B 測試:與超新星相同進場,但獲利達止盈 1/3 時把止損上移至保本(進場+0.05%)、TP 不變,回落至保本即「套保出場」。⚠️ 回測顯示此保本會剪掉肥尾止盈、期望值較差,僅供觀察。</template><template v-else-if="mainTab === 'gamble'">‼️此訊號為動能策略‼️<br>波動較大風險較高<br>止損概率較大，但止盈較遠。<br>有機會在行情出來時延續下去。<br>下單前務必確認倉位使用總本金「1%」<br>槓桿不超過「25%」<br>🌟若遇到洗盤行情風險更高，可往其他策略觀察更好的交易機會。<br><br>「此為幣種策略分享，不構成任何投資建議。」</template><template v-else-if="mainTab === 'emaonly'">‼️此訊號為順勢策略‼️<br>波動較低，<br>但有機會在行情出來後延續下去。<br>下單前務必確認倉位使用總本金「2%」<br>槓桿不超過「25-40%」<br>🌟若遇到盤整行情，可往其他策略觀察更好的交易機會。<br><br>「此為幣種策略分享，不構成任何投資建議。」</template><template v-else>‼️此訊號為動能策略‼️<br>波動較大風險較高<br>止損概率較大，但止盈較遠。<br>有機會在行情出來時延續下去。<br>下單前務必確認倉位使用總本金「1%」<br>槓桿不超過「25-30%」<br>🌟若遇到洗盤行情風險更高，可往其他策略觀察更好的交易機會。<br><br>「此為幣種策略分享，不構成任何投資建議。」</template></span></span></h2>
        <span class="mk-count" v-if="book">每 60 秒監控 · 自動止盈止損</span>
        <button v-if="can('admin')" class="csvbtn" @click="exportCSV">⬇ 匯出 CSV</button>
      </div>

      <div v-if="mainTab === 'emaonly' && book && book.market && book.market.length" class="mkt-bias">
        <span class="mkt-label">大盤方向<span class="help" tabindex="0">?<span class="help-pop">大盤(BTC / ETH)目前 <b>1 小時 EMA 趨勢</b>方向。小幣若<b>逆大盤</b>進場(例如大盤看跌卻做多小幣)風險較高、成功率較低,可作為進場前的參考。⚠️ 僅供參考,不影響本策略的自動進出場。</span></span></span>
        <span v-for="m in book.market" :key="m.coin" class="mkt-chip" :class="m.ok ? m.bias : 'na'">
          <b class="mkt-coin">{{ m.coin }}</b>
          <span class="mkt-dir">{{ !m.ok ? '評估中…' : m.bias === 'long' ? '看漲 ▲' : m.bias === 'short' ? '看跌 ▼' : '中性 —' }}</span>
        </span>
      </div>

      <div class="timefilter">
        <span class="tf-label">時間範圍</span>
        <button v-for="p in timePresets" :key="p.ms" :class="{ on: timeWin === p.ms }" @click="timeWin = p.ms">{{ p.label }}</button>
        <span class="tf-note">統計依所選範圍重算</span>
      </div>
      <div v-if="bookF" class="pstats">
        <div class="pstat"><div class="stat-k">已結束</div><div class="stat-v">{{ bookF.stats.closed }}</div></div>
        <div class="pstat"><div class="stat-k">勝率</div><div class="stat-v" :class="bookF.stats.win_rate >= 50 ? 'long' : 'short'">{{ bookF.stats.win_rate }}%</div></div>
        <div class="pstat"><div class="stat-k">平均損益</div><div class="stat-v" :class="bookF.stats.avg_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(bookF.stats.avg_pnl) }}</div></div>
        <div class="pstat"><div class="stat-k">累計損益</div><div class="stat-v" :class="bookF.stats.total_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(bookF.stats.total_pnl) }}</div></div>
      </div>

      <h3 class="psub" v-if="bookF">進行中 ({{ bookF.open.length }})</h3>
      <table v-if="bookF && bookF.open.length" class="grid">
        <thead><tr><th>幣種</th><th>方向</th><th class="r">進場</th><th class="r">現價</th><th class="r">損益%</th><th v-if="mainTab === 'paper' || mainTab === 'gamble' || mainTab === 'gamblehedge'" title="動能是否還在(雷達分數+CVD);⚠️贏單常因已漲一段而顯示轉弱,僅供參考">動能</th><th v-if="mainTab === 'gamblehedge'" class="r" title="獲利達止盈 1/3 時把止損上移至保本(進場+0.05%)、TP 不變並推播管理員;顯示保本停損價,回落至此為套保出場">套保</th><th class="r" title="當前資金費率">費率</th><th class="r">止盈</th><th class="r">止損</th><th class="r">進場時間</th><th class="r">持倉</th><th v-if="mainTab === 'emaonly' && can('admin')" class="r">操作</th></tr></thead>
        <tbody>
          <tr v-for="t in bookF.open" :key="t.coin + t.open_time" class="clickable" @click="openDetail(t.coin)">
            <td class="coin">{{ t.coin }}</td>
            <td><span class="dir" :class="t.dir === 'long' ? 'long' : 'short'">{{ t.dir === 'long' ? '做多' : '做空' }}</span></td>
            <td class="r">{{ fmtPrice(t.entry) }}</td>
            <td class="r">{{ fmtPrice(t.cur) }}</td>
            <td class="r" :class="t.pnl_pct >= 0 ? 'long' : 'short'"><b>{{ fmtPct(t.pnl_pct) }}</b></td>
            <td v-if="mainTab === 'paper' || mainTab === 'gamble' || mainTab === 'gamblehedge'"><span class="momlight" :class="momClass(t.momentum)">{{ momText(t.momentum) }}</span></td>
            <td v-if="mainTab === 'gamblehedge'" class="r"><span v-if="t.hedged" class="hedgetag">🛡 {{ fmtPrice(t.hedge_price) }}</span><span v-else class="tsmall">—</span></td>
            <td class="r tsmall">{{ fmtFund(t.cur_funding) }}</td>
            <td class="r long">{{ fmtPrice(t.tp) }} <small>({{ fmtPct(pnlAt(t, t.tp)) }})</small></td>
            <td class="r short">{{ fmtPrice(t.sl) }} <small>({{ fmtPct(pnlAt(t, t.sl)) }})</small></td>
            <td class="r tsmall">{{ fmtClock(t.open_time) }}</td>
            <td class="r">{{ fmtDur(holdMs(t)) }}</td>
            <td v-if="mainTab === 'emaonly' && can('admin')" class="r"><button class="exitbtn" @click.stop="manualExit(t)">手動出場</button></td>
          </tr>
        </tbody>
      </table>
      <p v-else-if="bookF" class="empty">此範圍內無進行中的模擬部位</p>

      <h3 class="psub" v-if="bookF && bookF.closed.length">已結束 ({{ bookF.closed.length }})</h3>
      <table v-if="bookF && bookF.closed.length" class="grid">
        <thead><tr><th>幣種</th><th>方向</th><th class="r">進場</th><th class="r">出場</th><th>結果</th><th class="r">損益%</th><th class="r" title="進場時資金費率">費率</th><th class="r">進場時間</th><th class="r">出場時間</th><th class="r">持倉</th></tr></thead>
        <tbody>
          <tr v-for="(t, i) in bookF.closed" :key="t.coin + i" class="clickable" @click="openDetail(t.coin)">
            <td class="coin">{{ t.coin }}</td>
            <td><span class="dir" :class="t.dir === 'long' ? 'long' : 'short'">{{ t.dir === 'long' ? '做多' : '做空' }}</span></td>
            <td class="r">{{ fmtPrice(t.entry) }}</td>
            <td class="r">{{ fmtPrice(t.cur) }}</td>
            <td><span class="otag" :class="t.outcome">{{ t.outcome === 'tp' ? '止盈 TP' : t.outcome === 'sl' ? '止損 SL' : t.outcome === 'trail' ? '移動止損' : t.outcome === 'reversed' ? '反向出場' : t.outcome === 'hedge' ? '套保出場' : '逾時' }}</span></td>
            <td class="r" :class="t.pnl_pct >= 0 ? 'long' : 'short'"><b>{{ fmtPct(t.pnl_pct) }}</b></td>
            <td class="r tsmall">{{ fmtFund(t.funding) }}</td>
            <td class="r tsmall">{{ fmtClock(t.open_time) }}</td>
            <td class="r tsmall">{{ fmtClock(t.close_time) }}</td>
            <td class="r">{{ fmtDur(holdMs(t)) }}</td>
          </tr>
        </tbody>
      </table>
      <p v-else-if="bookF" class="empty">此範圍內尚無已結束的模擬交易</p>
    </section>

    <!-- 財經事件 (high-impact US economic calendar) -->
    <section v-else-if="mainTab === 'events'">
      <div class="mk-head">
        <h2>財經事件(高影響 · 美國)<span class="help" tabindex="0">?<span class="help-pop">高影響美國經濟事件。這是唯一能「事前」的——<b>事件前可降風險、預期波動</b>。釋出後顯示「實際 vs 預期」(實際優於預期通常利多風險資產)。時間為你的本地時區。⚠️ 約 30 分鐘更新一次。</span></span></h2>
        <span class="mk-count">CPI / FOMC / 非農… · 共 {{ eventList.length }} 筆</span>
      </div>
      <table v-if="eventList.length" class="grid">
        <thead><tr><th>時間</th><th>事件</th><th class="r">狀態</th><th class="r">前值</th><th class="r">預期</th><th class="r">實際</th></tr></thead>
        <tbody>
          <tr v-for="(e, i) in eventList" :key="i" :class="{ 'ev-done': e.released, 'ev-soon': evSoon(e) }">
            <td class="tsmall">{{ fmtClock(e.time) }}</td>
            <td>{{ e.title }}</td>
            <td class="r">
              <span v-if="e.released" class="otag expired">已釋出</span>
              <span v-else class="ev-cd">⏳ {{ e.countdown }}</span>
            </td>
            <td class="r tsmall">{{ e.previous || '—' }}</td>
            <td class="r tsmall">{{ e.forecast || '—' }}</td>
            <td class="r"><b v-if="e.actual" :class="e.actual === e.forecast ? '' : 'hot'">{{ e.actual }}</b><span v-else>—</span></td>
          </tr>
        </tbody>
      </table>
      <p v-else class="loading">載入經濟行事曆中…(若持續空白,可能本週無高影響美國事件)</p>
    </section>

    <!-- 清算 (liquidation feed, OKX) -->
    <section v-else-if="mainTab === 'flow'">
      <div class="mk-head">
        <h2>清算<span class="help" tabindex="0">?<span class="help-pop">即時清算事件(OKX 永續)。<b>即時監控、非回測訊號</b>;持續累積,日後可驗證是否領先。多單被洗=下殺、空單被軋=上拉。</span></span></h2>
      </div>

      <!-- liquidation summary + feed -->
      <div v-if="liquidations" class="liqsum">
        <div class="liqbox short"><div class="stat-k">近 1h 多單爆倉</div><div class="stat-v short">${{ (liquidations.long_usd_1h / 1e6).toFixed(2) }}M</div></div>
        <div class="liqbox long"><div class="stat-k">近 1h 空單爆倉</div><div class="stat-v long">${{ (liquidations.short_usd_1h / 1e6).toFixed(2) }}M</div></div>
        <div class="liqbox"><div class="stat-k">偏向</div><div class="stat-v" :class="liquidations.long_usd_1h > liquidations.short_usd_1h ? 'short' : 'long'">{{ liquidations.long_usd_1h > liquidations.short_usd_1h ? '多單被洗(下殺)' : '空單被軋(上拉)' }}</div></div>
      </div>

      <h3 class="psub" v-if="liquidations && liquidations.recent.length">近期清算事件 ({{ liquidations.recent.length }})</h3>
      <table v-if="liquidations && liquidations.recent.length" class="grid">
        <thead><tr><th>時間</th><th>幣種</th><th>被清算</th><th class="r">金額</th><th class="r">價格</th></tr></thead>
        <tbody>
          <tr v-for="(r, i) in liquidations.recent" :key="i" class="clickable" @click="openDetail(r.coin)">
            <td class="tsmall">{{ liqClock(r.time) }}</td>
            <td class="coin">{{ r.coin }}</td>
            <td><span class="dir" :class="r.side === 'long' ? 'short' : 'long'">{{ r.side === 'long' ? '多單' : '空單' }}</span></td>
            <td class="r"><b>${{ r.usd >= 1e6 ? (r.usd / 1e6).toFixed(2) + 'M' : (r.usd / 1e3).toFixed(1) + 'K' }}</b></td>
            <td class="r">{{ fmtPrice(r.px) }}</td>
          </tr>
        </tbody>
      </table>
    </section>

    <!-- Upbit 公告 (韓文原文自動翻譯為繁體中文) -->
    <section v-else-if="mainTab === 'upbit'">
      <div class="mk-head">
        <h2>Upbit 公告<span class="help" tabindex="0">?<span class="help-pop">韓國 Upbit 交易所官方公告。原文為韓文,已自動翻譯為繁體中文;點擊可開啟原公告。<b>「上架」標記代表新幣上架/交易支援類公告</b>,通常對該幣種有較大影響。</span></span></h2>
        <span class="mk-count">共 {{ upbitNotices.length }} 筆</span>
      </div>
      <table v-if="upbitNotices.length" class="grid">
        <thead><tr><th>時間</th><th>標題(繁中)</th><th class="r">類型</th></tr></thead>
        <tbody>
          <tr v-for="n in upbitNotices" :key="n.id" :class="{ 'ev-soon': n.listing }">
            <td class="tsmall">{{ upbitTime(n.listed_at) }}</td>
            <td>
              <a :href="n.url" target="_blank" rel="noopener" class="upbit-link">{{ n.title_zh }}</a>
              <div class="upbit-orig">{{ n.title }}</div>
            </td>
            <td class="r">
              <span v-if="n.listing" class="otag tp">🚀 上架</span>
              <span v-else class="otag expired">公告</span>
            </td>
          </tr>
        </tbody>
      </table>
      <p v-else class="loading">載入 Upbit 公告中…(首次載入需翻譯,請稍候)</p>
    </section>

    <!-- 市場快訊 (全球新聞事件) -->
    <section v-else-if="mainTab === 'news'">
      <div class="mk-head">
        <h2>市場快訊<span class="help" tabindex="0">?<span class="help-pop">加密市場即時新聞頭條,依主題自動分類(人物/央行/貿易/地緣/監管/爆雷/機構/巨鯨/加密等)。英文原標題自動翻譯為繁中,點擊開原文。⚠️ 僅供風險參考,非投資建議。</span></span></h2>
        <span class="mk-count">共 {{ news.length }} 則 · 每 5 分更新</span>
      </div>
      <div class="timefilter" v-if="news.length">
        <button :class="{ on: newsCat === '' }" @click="newsCat = ''">全部</button>
        <button v-for="c in newsCatList" :key="c.key" :class="{ on: newsCat === c.key }" @click="newsCat = c.key">{{ c.label }}</button>
      </div>
      <table v-if="newsF.length" class="grid">
        <thead><tr><th>時間</th><th>類型</th><th>標題(繁中)</th><th>媒體</th></tr></thead>
        <tbody>
          <tr v-for="(n, i) in newsF" :key="i">
            <td class="tsmall">{{ upbitTime(n.time) }}</td>
            <td class="tsmall"><span class="newscat" :class="'nc-' + n.category">{{ n.label }}</span></td>
            <td>
              <a :href="n.url" target="_blank" rel="noopener" class="upbit-link">{{ n.title }}</a>
              <div v-if="n.title_en" class="upbit-orig">{{ n.title_en }}</div>
            </td>
            <td class="tsmall">{{ n.domain }}</td>
          </tr>
        </tbody>
      </table>
      <p v-else-if="news.length" class="empty">此分類暫無快訊</p>
      <p v-else class="loading">載入市場快訊中…(首次載入需翻譯,請稍候)</p>
    </section>

    <!-- 資金費率 (OKX) -->
    <section v-else-if="mainTab === 'funding'">
      <div class="mk-head">
        <h2>資金費率<span class="help" tabindex="0">?<span class="help-pop">各永續合約的當期資金費率(資料來源 OKX)。<b>正費率</b>=多方付費給空方(市場偏多、多單擁擠),<b>負費率</b>=空方付費給多方。每 8 小時結算一次。費率極端常是情緒過熱/反轉的參考,⚠️ 非投資建議。</span></span></h2>
        <span class="mk-count" v-if="funding && funding.updated_at">來源 OKX · {{ fundingRows.length }} / {{ funding.rows.length }} 檔 · {{ fundClock(new Date(funding.updated_at).getTime()) }} 更新</span>
      </div>
      <div class="timefilter" v-if="funding && funding.rows.length">
        <span class="tf-label">板塊</span>
        <button :class="{ on: fundingSector === '' }" @click="fundingSector = ''">全部</button>
        <button v-for="s in fundingSectors" :key="s" :class="{ on: fundingSector === s }" @click="fundingSector = s">{{ s }}</button>
        <button class="tf-sort" :class="{ on: fundingAbs }" @click="fundingAbs = !fundingAbs" title="切換:費率高→低 / 絕對值大→小(找兩邊極端)">{{ fundingAbs ? '極端排序' : '費率排序' }}</button>
      </div>
      <table v-if="fundingRows.length" class="grid">
        <thead><tr><th>幣種</th><th>板塊</th><th class="r">資金費率</th><th class="r">下次結算</th></tr></thead>
        <tbody>
          <tr v-for="f in fundingRows" :key="f.coin" class="clickable" @click="openDetail(f.coin)">
            <td class="coin">{{ f.coin }}</td>
            <td class="tsmall">{{ f.sector }}</td>
            <td class="r" :class="f.rate >= 0 ? 'short' : 'long'"><b>{{ (f.rate * 100).toFixed(4) }}%</b></td>
            <td class="r tsmall">{{ fundClock(f.next_ms) }}</td>
          </tr>
        </tbody>
      </table>
      <p v-else class="loading">載入資金費率中…</p>
    </section>

    <!-- 代幣解鎖 (DefiLlama) -->
    <section v-else-if="mainTab === 'unlock'">
      <div class="mk-head">
        <h2>代幣解鎖<span class="help" tabindex="0">?<span class="help-pop">主流代幣的即將解鎖佔<b>流通供給</b>的比例=市場潛在賣壓;比例越高、越集中,拋壓風險越大。「下次懸崖」為未來 30 天內單日最大解鎖(佔最大供給%)。持續性的質押釋放不計入。⚠️ 非投資建議。</span></span></h2>
        <span class="mk-count" v-if="unlock && unlock.updated_at">來源 DefiLlama · {{ unlockRows.length }} 檔 · {{ fundClock(new Date(unlock.updated_at).getTime()) }} 更新</span>
      </div>
      <div class="timefilter" v-if="unlock && unlock.rows.length">
        <span class="tf-label">排序</span>
        <button :class="{ on: unlockSort === 'sell' }" @click="unlockSort = 'sell'">賣壓(30天%)</button>
        <button :class="{ on: unlockSort === 'date' }" @click="unlockSort = 'date'">最近懸崖</button>
      </div>
      <table v-if="unlockRows.length" class="grid">
        <thead><tr><th>代幣</th><th class="r" title="未來 7 天解鎖佔流通供給">7天</th><th class="r" title="未來 30 天解鎖佔流通供給 + 數量">30天</th><th class="r">30天估值</th><th class="r" title="30 天內單日最大解鎖(佔最大供給%)">下次懸崖</th><th>解鎖對象</th></tr></thead>
        <tbody>
          <tr v-for="u in unlockRows" :key="u.name">
            <td class="coin">{{ u.coin }}<small class="vtag"> {{ u.name }}</small></td>
            <td class="r tsmall">{{ u.next7_pct ? u.next7_pct.toFixed(2) + '%' : '—' }}</td>
            <td class="r"><b :class="{ short: u.next30_pct >= 3 }">{{ u.next30_pct.toFixed(2) }}%</b><small class="vtag"> {{ fmtNum(u.next30_amt) }}<template v-if="!u.by_circ"> ⚠</template></small></td>
            <td class="r tsmall">{{ u.usd30 ? '$' + fmtNum(u.usd30) : '—' }}</td>
            <td class="r tsmall">{{ unlockDate(u.peak_date) }} <span class="vtag">{{ unlockDays(u.peak_date) }}</span> · {{ u.peak_pct_max ? u.peak_pct_max.toFixed(2) + '%' : '—' }}</td>
            <td class="tsmall">{{ u.cats.join('、') }}</td>
          </tr>
        </tbody>
      </table>
      <p v-else class="loading">載入代幣解鎖中…</p>
    </section>

    <!-- 文章專欄 (Feature 3) -->
    <section v-else-if="mainTab === 'articles'">
      <!-- 內頁 -->
      <template v-if="articleView">
        <button class="backbtn" @click="articleView = null">← 返回列表</button>
        <article class="artpost">
          <h1 class="arttitle">{{ articleView.title }}</h1>
          <div class="arttags"><span v-for="t in articleView.tags" :key="t" class="arttag">{{ t }}</span></div>
          <div v-for="(b, i) in articleView.blocks" :key="i" class="artblock">
            <p v-if="b.type === 'text'" class="arttext">{{ b.text }}</p>
            <img v-else-if="b.type === 'image' && b.image" :src="b.image" class="artimg" />
          </div>
          <div v-if="can('admin')" class="artadmin">
            <button class="regbtn" @click="editArticle(articleView)">編輯</button>
            <button class="pinbtn" :class="{ on: articleView.pinned }" @click="togglePin(articleView)">{{ articleView.pinned ? '取消置頂' : '置頂' }}</button>
            <button class="nobtn" @click="removeArticle(articleView)">刪除</button>
          </div>
        </article>
      </template>

      <!-- 列表 -->
      <template v-else>
        <div class="mk-head">
          <h2>文章專欄</h2>
          <button v-if="can('admin')" class="loginbtn" @click="newArticle">＋ 新增文章</button>
        </div>
        <div v-if="articles.length" class="artgrid">
          <div v-for="a in articles" :key="a.id" class="artcard" :class="{ pinned: a.pinned }" @click="articleView = a">
            <div class="artcard-cover">
              <img v-if="a.cover" :src="a.cover" /><span v-else>JMCH</span>
              <span v-if="a.pinned" class="pinbadge">📌 置頂</span>
            </div>
            <div class="artcard-body">
              <div class="artcard-title">{{ a.title }}</div>
              <div class="arttags"><span v-for="t in a.tags" :key="t" class="arttag">{{ t }}</span></div>
              <div class="artcard-foot">
                <span class="artcard-date">{{ a.created ? new Date(a.created).toLocaleDateString() : '' }}</span>
                <button v-if="can('admin')" class="pinbtn" :class="{ on: a.pinned }" @click.stop="togglePin(a)">{{ a.pinned ? '取消置頂' : '置頂' }}</button>
              </div>
            </div>
          </div>
        </div>
        <p v-else class="empty">尚無文章。</p>
      </template>
    </section>

    <footer>
      <div v-if="socialLinks.length" class="socialbar">
        <a v-for="(s, i) in socialLinks" :key="i" :href="s.url" target="_blank" rel="noopener"
           class="social" :style="{ background: socialInfo(s.platform).color }" :title="socialInfo(s.platform).name">
          <span v-if="socialSvg(s.platform)" class="social-svg" v-html="socialSvg(s.platform)"></span>
          <template v-else>{{ socialInfo(s.platform).icon }}</template>
        </a>
      </div>
      <p class="foot-note">所有數據來自交易所公開 API，僅供研究。評分權重為自訂,請以自己的回測為準。非投資建議。</p>
    </footer>
  </div>

  <!-- 首頁右下懸浮 QR (Feature 4) -->
  <div v-if="authed && config.qr && !qrHidden" class="qrfloat">
    <button class="qrclose" @click="qrHidden = true">✕</button>
    <a v-if="config.qr_link" :href="config.qr_link" target="_blank" rel="noopener"><img :src="config.qr" alt="QR" /></a>
    <img v-else :src="config.qr" alt="QR" />
    <span class="qrcap">掃碼</span>
  </div>

  <!-- asset-proof lightbox (admin) -->
  <div v-if="proofView" class="overlay proofbox" @click="proofView = ''">
    <button class="proofclose" @click.stop="proofView = ''" aria-label="關閉">✕</button>
    <img :src="proofView" class="prooffull" @click="proofView = ''" alt="資產證明" />
  </div>

  <!-- article editor (admin) -->
  <div v-if="artEdit" class="overlay" @click.self="artEdit = null">
    <div class="arteditor" @click.stop>
      <div class="ae-head">
        <h3>{{ artEdit.id ? '編輯文章' : '新增文章' }}</h3>
        <button class="close" @click="artEdit = null">✕</button>
      </div>
      <label class="ae-label">標題</label>
      <input v-model="artEdit.title" class="authin" placeholder="標題" />
      <label class="ae-label">標籤(逗號分隔)</label>
      <input :value="artEdit.tags.join(', ')" @input="setArtTags($event.target.value)" class="authin" placeholder="標籤1, 標籤2" />
      <label class="ae-label">主圖</label>
      <div class="ae-cover">
        <img v-if="artEdit.cover" :src="artEdit.cover" />
        <label class="authfile"><span>{{ artEdit.cover ? '更換主圖' : '＋ 上傳主圖' }}</span><input type="file" accept="image/*,.heic,.heif" hidden @change="onCoverPick" /></label>
      </div>
      <label class="ae-label">內文區塊(圖文穿插)</label>
      <div v-for="(b, i) in artEdit.blocks" :key="i" class="ae-block">
        <div class="ae-block-tools">
          <span class="ae-btype">{{ b.type === 'text' ? '段落' : '圖片' }}</span>
          <button class="minibtn" @click="moveBlock(i, -1)">↑</button>
          <button class="minibtn" @click="moveBlock(i, 1)">↓</button>
          <button class="minibtn del" @click="removeBlock(i)">✕</button>
        </div>
        <textarea v-if="b.type === 'text'" v-model="b.text" class="ae-textarea" placeholder="輸入段落文字…"></textarea>
        <div v-else class="ae-imgblock">
          <img v-if="b.image" :src="b.image" />
          <label class="authfile"><span>{{ b.image ? '更換圖片' : '＋ 上傳圖片' }}</span><input type="file" accept="image/*,.heic,.heif" hidden @change="onBlockImg($event, i)" /></label>
        </div>
      </div>
      <div class="ae-addrow">
        <button class="regbtn" @click="addBlock('text')">＋ 段落</button>
        <button class="regbtn" @click="addBlock('image')">＋ 圖片</button>
      </div>
      <div class="ae-foot">
        <button class="authbtn" @click="saveArticle">儲存</button>
        <button v-if="artEdit.id" class="nobtn" @click="removeArticle(artEdit)">刪除</button>
      </div>
    </div>
  </div>

  <!-- detail drawer -->
  <div v-if="detailCoin" class="overlay" @click="closeDetail">
    <aside class="drawer" @click.stop>
      <button class="close" @click="closeDetail">✕</button>
      <p v-if="detailLoading" class="loading">載入 {{ detailCoin }} 詳情…</p>
      <p v-else-if="detailError" class="err">{{ detailError }}</p>
      <template v-else-if="detail">
        <section class="card rationale" :class="biasClass(detail.bias)">
          <div class="rationale-head">
            <span class="dot" :class="biasClass(detail.bias)"></span>
            <h2>{{ detail.coin }} · {{ rationaleTitle() }}</h2>
            <span class="badge" :class="biasClass(detail.bias)">{{ headerBadge }}<small>{{ detail.bias_label }}</small></span>
          </div>
          <div v-for="r in detail.rationale" :key="r.label" class="rationale-row">
            <span class="rl-label">{{ r.label }}</span>
            <span class="tag" :class="toneClass(r.tone)">{{ r.tag }}</span>
            <span class="rl-text">{{ r.text }}</span>
          </div>
        </section>
        <div class="stats">
          <div class="stat"><div class="stat-k">24H 漲跌</div><div class="stat-v" :class="detail.stats.chg_24h >= 0 ? 'long' : 'short'">{{ fmtPct(detail.stats.chg_24h) }}</div></div>
          <div class="stat"><div class="stat-k">資金費率</div><div class="stat-v" :class="detail.stats.funding_rate >= 0 ? 'long' : 'short'">{{ (detail.stats.funding_rate * 100).toFixed(4) }}%</div></div>
          <div class="stat"><div class="stat-k">未平倉量</div><div class="stat-v">{{ fmtNum(detail.stats.oi_value) }} USDT</div></div>
          <div class="stat"><div class="stat-k">建議多空</div><div class="stat-v" :class="biasClass(detail.bias)">{{ detail.bias_label }}</div></div>
          <div class="stat span2"><div class="stat-k">綜合評分</div><div class="dots"><span v-for="(on, i) in ratingDots" :key="i" class="seg" :class="{ on, [biasClass(detail.bias)]: on }"></span></div></div>
        </div>
        <section class="card">
          <h3>評分依據</h3>
          <div v-for="b in detail.breakdown" :key="b.label" class="bd-row" :class="{ info: b.info }">
            <span class="bd-label">{{ b.label }}</span><span class="bd-note">{{ b.note }}</span>
            <span v-if="b.info" class="bd-score muted" title="回測顯示為反指標，僅供參考，不計入評分">參考</span>
            <span v-else class="bd-score" :class="scoreClass(b.score)">{{ b.score >= 0 ? '+' : '' }}{{ b.score }} 分</span>
          </div>
          <div v-if="detail.liq_factor < 1" class="bd-row info">
            <span class="bd-label">流動性抑制</span>
            <span class="bd-note">低流動性 · 小計 {{ detail.raw >= 0 ? '+' : '' }}{{ detail.raw }} ×{{ detail.liq_factor.toFixed(2) }}</span>
            <span class="bd-score muted" title="24h 成交量偏低，評分按比例縮減">×{{ detail.liq_factor.toFixed(2) }}</span>
          </div>
          <div class="bd-row total">
            <span class="bd-label">總分</span><span class="bd-note"></span>
            <span class="bd-score" :class="scoreClass(detail.total)">{{ detail.total >= 0 ? '+' : '' }}{{ detail.total }} 分 = {{ detail.rating }}/10</span>
          </div>
        </section>
        <section v-if="detail.related.length" class="card">
          <h3>相關幣種 <span class="sub">{{ detail.sector }}</span></h3>
          <div class="related">
            <button v-for="rc in detail.related" :key="rc.coin" class="rc" @click="openDetail(rc.coin)">
              <div class="rc-coin">{{ rc.coin }}</div>
              <div class="rc-chg" :class="rc.chg >= 0 ? 'long' : 'short'">{{ fmtPct(rc.chg) }}</div>
              <div class="rc-score" :class="scoreClass(rc.score)">{{ rc.score >= 0 ? '+' : '' }}{{ rc.score }}</div>
            </button>
          </div>
        </section>
      </template>
    </aside>
  </div>
</template>

<style>
:root { color-scheme: dark; }
html { background: #0a0b0e; } /* base colour lives here so the body watermark can sit above it */
body { margin: 0; background: transparent; color: #e8eaed; font-family: system-ui, -apple-system, "PingFang TC", sans-serif; }
/* logo watermark: fixed, centred, low-opacity — shows through page gaps, never over card content */
body::before {
  content: ""; position: fixed; inset: 0; z-index: -1; pointer-events: none;
  background: url('/logo.png') no-repeat center center;
  background-size: min(58vw, 500px);
  opacity: 0.4;
}
.long { color: #2ec26b; } .short { color: #ff5c5c; } .neutral { color: #b8bcc4; }
.err { color: #ff6b6b; font-size: 12px; }
.r { text-align: right; }

/* top bar */
.topbar { display: flex; align-items: center; gap: 16px; border-bottom: 1px solid #1c1f25; background: #0c0e12; position: sticky; top: 0; z-index: 10;
  padding: calc(10px + env(safe-area-inset-top)) calc(20px + env(safe-area-inset-right)) 10px calc(20px + env(safe-area-inset-left)); }
.tickers { display: flex; gap: 18px; font-size: 13px; }
.tk b { color: #8b909a; font-weight: 600; margin-right: 4px; }
.tk em { font-style: normal; margin-left: 4px; font-size: 12px; }
.search { flex: 1; max-width: 420px; background: #16181d; border: 1px solid #23262d; border-radius: 8px; padding: 7px 12px; color: #5c616b; font-size: 13px; }
.topmeta { margin-left: auto; display: flex; align-items: center; gap: 12px; }
.brand { font-size: 12px; color: #8b909a; }
.regime { font-size: 12px; color: #8b909a; }
.regime b { font-weight: 700; }
.regbtn { background: #16181d; border: 1px solid #23262d; color: #8b909a; padding: 4px 10px; border-radius: 8px; cursor: pointer; font-size: 12px; }
.regbtn.on { background: #2a2410; border-color: #e0b341; color: #f4d774; }
.regbtn.login { background: #1b2942; border-color: #5b8def; color: #cfe0ff; }
.userchip { font-size: 12px; color: #c8cdd6; display: inline-flex; align-items: center; gap: 6px; }
.userchip em { font-style: normal; background: #2a2410; color: #f4d774; padding: 1px 6px; border-radius: 6px; font-size: 11px; }
.loginbox { background: #16181d; border: 1px solid #2a2d35; border-radius: 14px; padding: 22px; width: 300px; display: flex; flex-direction: column; gap: 10px; }
.loginbox h3 { margin: 0 0 4px; }
.loginbox input { background: #0d0f13; border: 1px solid #2a2d35; border-radius: 8px; padding: 9px 11px; color: #e8eaed; font-size: 14px; }
.loginbtn { background: #1b2942; border: 1px solid #5b8def; color: #cfe0ff; padding: 9px; border-radius: 8px; cursor: pointer; font-weight: 700; }
.loginhint { font-size: 11px; color: #8b909a; margin: 2px 0 0; line-height: 1.5; }
.rank-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
@media (max-width: 900px) { .rank-grid { grid-template-columns: 1fr; } }
.adminbox { margin-bottom: 14px; }
.newuser { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
.newuser input, .newuser select, .grid select { background: #0d0f13; border: 1px solid #2a2d35; border-radius: 7px; padding: 7px 9px; color: #e8eaed; font-size: 13px; }
.newuser .loginbtn { padding: 7px 16px; }
.admin-msg { background: #11161f; border: 1px solid #2a3340; border-radius: 8px; padding: 8px 12px; font-size: 13px; color: #cfe0ff; margin: 0 0 12px; }
.momlight { font-size: 11.5px; white-space: nowrap; padding: 2px 7px; border-radius: 6px; font-weight: 600; }
.mom-alive { background: rgba(46,160,90,0.16); color: #4cd17e; }
.mom-weak { background: rgba(224,179,65,0.16); color: #f4d774; }
.mom-dead { background: rgba(229,72,77,0.16); color: #ff6b6f; }
.hedgetag { font-size: 11.5px; white-space: nowrap; padding: 2px 7px; border-radius: 6px; font-weight: 600; background: rgba(74,163,255,0.16); color: #6db5ff; }
.qtag { font-size: 10px; font-style: normal; padding: 1px 5px; border-radius: 6px; margin-left: 5px; vertical-align: middle; }
.qtag.hq { background: #2a2410; color: #f4d774; border: 1px solid #e0b341; }
.qtag.good { background: #11261a; color: #4ec77f; }
.qtag.warn { background: #2a2027; color: #c77b8b; }
.opt-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
@media (max-width: 900px) { .opt-grid { grid-template-columns: 1fr; } }
.opt-card { padding: 16px; }
.opt-head { display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 12px; }
.opt-coin { font-size: 20px; font-weight: 700; }
.opt-spot { font-size: 18px; }
.opt-metrics { display: grid; grid-template-columns: repeat(3, 1fr); gap: 10px; margin-bottom: 6px; }
.om { background: #16181d; border: 1px solid #23262d; border-radius: 8px; padding: 8px 10px; }
.om-k { font-size: 11px; color: #8b909a; }
.om-v { font-size: 16px; font-weight: 700; margin-top: 2px; }
.om-sub { font-size: 11px; margin-top: 2px; color: #8b909a; }
.opt-sub-h { font-size: 12px; color: #8b909a; margin: 12px 0 6px; font-weight: 600; }
.term-bar { display: grid; grid-template-columns: 64px 1fr 48px; align-items: center; gap: 8px; margin-bottom: 4px; font-size: 12px; }
.term-lab { color: #8b909a; }
.term-track { background: #16181d; border-radius: 4px; height: 8px; overflow: hidden; }
.term-track i { display: block; height: 100%; background: #5b8def; }
.term-iv { text-align: right; }
.opt-walls { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; margin-top: 4px; }
.wall-row { display: flex; justify-content: space-between; font-size: 12px; padding: 3px 0; border-bottom: 1px solid #1a1c21; }
.wall-row .near { color: #f4d774; font-weight: 700; }
.timefilter { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; margin: 10px 0 14px; }
.timefilter .tf-label { font-size: 12px; color: #8b909a; margin-right: 2px; }
.timefilter button { background: #16181d; border: 1px solid #23262d; color: #c8cdd6; padding: 4px 12px; border-radius: 8px; cursor: pointer; font-size: 12px; }
.timefilter button.on { background: #1b2942; border-color: #5b8def; color: #cfe0ff; }
.timefilter .tf-note { font-size: 11px; color: #6b7078; margin-left: 6px; }
.timefilter .tf-sort { margin-left: auto; border-color: #3a3320; color: #e0b341; }
.timefilter .tf-sort.on { background: #2a2410; border-color: #e0b341; color: #f4d774; }
.ddbanner { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; padding: 8px 16px; font-size: 12px; }
.ddbanner.down.lv-high { background: #3a1014; border-bottom: 1px solid #6b1f27; }
.ddbanner.down.lv-mid { background: #2e2410; border-bottom: 1px solid #5c4a1a; }
.ddbanner.up { background: #0e2417; border-bottom: 1px solid #1f5c3a; }
.ddbanner .dd-lv { font-weight: 800; }
.ddbanner.down.lv-high .dd-lv { color: #ff7a8a; }
.ddbanner.down.lv-mid .dd-lv { color: #f4d774; }
.ddbanner.up .dd-lv { color: #4ec77f; }
.ddbanner .dd-why { color: #c8cdd6; }
.ddbanner .dd-act { color: #cfd3da; margin-left: auto; font-weight: 600; }
.riskbar { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; padding: 7px 16px; font-size: 12px; border-bottom: 1px solid #1a1c21; background: #121317; }
.riskbar.risk-on { background: #0e1a12; }
.riskbar.risk-off { background: #1c1113; }
.rb-light { font-size: 10px; }
.rb-light.risk-on { color: #4ec77f; }
.rb-light.risk-off { color: #e06a82; }
.rb-light.neutral { color: #8b909a; }
.rb-tag { font-weight: 700; }
.rb-items { display: flex; gap: 12px; flex-wrap: wrap; color: #8b909a; }
.rb-it b { font-weight: 700; }
.rb-us { color: #c8cdd6; }
.rb-us.hot { color: #f4d774; }
.rb-reason { color: #c77b8b; }
.rb-events { display: flex; gap: 10px; flex-wrap: wrap; }
.rb-ev { color: #e0b341; }
.rb-ev.released { color: #8b909a; }
.rb-ev b { color: inherit; font-weight: 700; }
.rb-note { margin-left: auto; color: #6b7078; cursor: help; }
.ev-done { opacity: 0.5; }
.ev-soon { background: #221a0e; }
.ev-cd { color: #e0b341; font-weight: 700; }
.liqsum { display: flex; gap: 12px; flex-wrap: wrap; margin: 8px 0 4px; }
.liqbox { background: #16181d; border: 1px solid #23262d; border-radius: 10px; padding: 10px 14px; min-width: 140px; }
.liqbox .stat-v { font-size: 18px; font-weight: 700; margin-top: 2px; }
.chart-card { padding: 12px 14px; }
.chart-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.iv-toggle button { background: #16181d; border: 1px solid #23262d; color: #c8cdd6; padding: 2px 10px; border-radius: 6px; cursor: pointer; font-size: 12px; margin-left: 4px; }
.iv-toggle button.on { background: #1b2942; border-color: #5b8def; color: #cfe0ff; }
.kchart { width: 100%; height: 180px; display: block; background: #0d0f13; border-radius: 8px; }
.kchart line { stroke-width: 1; }
.k-up { stroke: #4ec77f; }
.k-dn { stroke: #e06a82; }
.k-up-f { fill: #4ec77f; }
.k-dn-f { fill: #e06a82; }
.chart-meta { display: flex; justify-content: space-between; font-size: 11px; color: #8b909a; margin-top: 6px; }
.ema-legend { display: flex; gap: 8px; }
.ema-legend i { font-style: normal; font-weight: 700; }
.loading.sm { font-size: 12px; padding: 8px; }
.opt-card { overflow: visible; }
.info { position: relative; display: inline-block; width: 14px; height: 14px; line-height: 14px; margin-left: 4px; border-radius: 50%; background: #2a2d35; color: #9aa0aa; font-size: 9px; font-weight: 700; font-style: normal; text-align: center; cursor: help; vertical-align: middle; }
.info .bubble { display: none; position: absolute; left: 0; top: 20px; width: 210px; background: #0d0f13; border: 1px solid #2f333c; border-radius: 8px; padding: 8px 10px; font-size: 11px; font-weight: 400; line-height: 1.55; color: #c8cdd6; text-align: left; white-space: normal; z-index: 60; box-shadow: 0 8px 24px rgba(0, 0, 0, 0.55); }
.info .bubble b { color: #e8eaed; }
.info:hover .bubble { display: block; }
.info .bubble.wide { width: 270px; }
.bubble .reason { display: block; margin-top: 4px; }
.bubble .reason.dim { color: #8b909a; margin-top: 6px; }
.opt-bias { font-size: 12px; font-style: normal; font-weight: 700; padding: 2px 8px; border-radius: 7px; margin-left: 8px; vertical-align: middle; }
.opt-bias.long { background: #11261a; color: #4ec77f; }
.opt-bias.short { background: #2a2027; color: #e06a82; }
.opt-bias.neutral { background: #1f2228; color: #c8cdd6; }
.opt-bias .info { background: rgba(255, 255, 255, 0.18); color: inherit; }
/* right-column metrics: open bubble leftward so it doesn't clip off-card */
.opt-metrics .om:nth-child(3n) .info .bubble,
.wall-col:last-child .info .bubble { left: auto; right: 0; }

.wrap { max-width: 1200px; margin: 0 auto; padding: 18px 20px 64px; }

/* three cards */
.cards { display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 14px; margin-bottom: 18px; }
.card { background: #14161b; border: 1px solid #23262d; border-radius: 14px; padding: 16px; }
.rec-head { display: flex; align-items: center; gap: 8px; font-weight: 600; font-size: 14px; margin-bottom: 12px; }
.led { width: 9px; height: 9px; border-radius: 50%; display: inline-block; }
.led.long { background: #2ec26b; } .led.short { background: #ff5c5c; }
.rec-cols, .rec-row { display: grid; grid-template-columns: 1.2fr 1fr 1.1fr 0.8fr; align-items: center; gap: 6px; }
.rec-cols { font-size: 11px; color: #8b909a; padding: 4px 6px; }
.rec-row { width: 100%; background: none; border: none; color: inherit; padding: 9px 6px; border-top: 1px solid #1c1f25; cursor: pointer; font-size: 13px; text-align: left; }
.rec-row:hover { background: #1a1d23; border-radius: 8px; }
.rec-row.featured { background: #12281c; border-radius: 8px; border-top-color: transparent; box-shadow: inset 3px 0 0 #2ec26b; }
.rec-row.featured:hover { background: #163322; }
.rec-row.featured.short-feat { background: #281414; box-shadow: inset 3px 0 0 #ff5c5c; }
.rec-row.featured.short-feat:hover { background: #331818; }
.hot { font-style: normal; font-size: 9px; font-weight: 700; color: #062a17; background: #2ec26b; border-radius: 4px; padding: 1px 5px; margin-left: 2px; }
.hot.short-hot { color: #2a0606; background: #ff5c5c; }
.rec-coin { font-weight: 600; display: flex; align-items: center; gap: 6px; }
.medal { font-style: normal; font-size: 13px; width: 16px; text-align: center; }
.rec-price { font-variant-numeric: tabular-nums; color: #c8ccd4; }
.bars { display: flex; gap: 2px; }
.bar { width: 6px; height: 14px; border-radius: 2px; background: #23262d; }
.bar.on.long { background: #2ec26b; } .bar.on.short { background: #ff5c5c; }
.empty { color: #5c616b; font-size: 12px; text-align: center; padding: 16px 0; }

/* gauge */
.gauge { display: flex; flex-direction: column; align-items: center; }
.gauge-title { font-weight: 600; font-size: 14px; align-self: center; margin-bottom: 4px; }
.gsvg { width: 100%; max-width: 240px; }
.gauge-val { font-size: 34px; font-weight: 800; line-height: 1; margin-top: -4px; }
.gauge-label { font-size: 13px; margin-top: 4px; }
.gauge-prev { font-size: 11px; color: #8b909a; margin-top: 4px; }
.gauge-prev em { font-style: normal; }
.gauge-zones { display: flex; gap: 8px; font-size: 10px; color: #5c616b; margin-top: 10px; }

/* nav */
.mainnav { display: flex; flex-direction: column; gap: 8px; margin: 8px 0 16px; }
.navrow { display: flex; align-items: flex-start; gap: 10px; }
.navrow + .navrow { padding-top: 8px; border-top: 1px solid #1c1f25; }
.navgroup { flex: 0 0 34px; font-size: 11px; font-weight: 700; letter-spacing: .5px; color: #7a8089; padding-top: 8px; }
.navbtns { display: flex; flex-wrap: wrap; gap: 8px; flex: 1 1 auto; min-width: 0; }
.mainnav button { background: #16181d; border: 1px solid #23262d; color: #b8bcc4; padding: 6px 14px; border-radius: 8px; cursor: pointer; font-size: 13px; }
.mainnav button.active { background: #2a2410; border-color: #e0b341; color: #f4d774; }

/* breakout radar */
.radar-note { font-size: 12px; color: #8b909a; margin: 0 0 12px; line-height: 1.6; }
.radar-cols { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
.radar-row { display: grid; grid-template-columns: 1.3fr 0.6fr 0.8fr 0.6fr 0.8fr 0.8fr; gap: 6px; align-items: center; padding: 8px 6px 2px; font-size: 12px; font-variant-numeric: tabular-nums; }
.radar-row.rhead { color: #8b909a; font-size: 11px; padding-bottom: 4px; }
.radar-item { border-top: 1px solid #1c1f25; cursor: pointer; }
.radar-item:hover { background: #1a1d23; border-radius: 8px; }
.radar-entry { font-size: 11px; color: #8b909a; padding: 0 6px 8px; }
.radar-entry b { color: #c8ccd4; font-weight: 600; font-variant-numeric: tabular-nums; }
.radar-row .coin { display: flex; flex-direction: column; line-height: 1.2; }
.vtag { font-size: 10px; color: #5c616b; font-weight: 400; }
.ignite { font-size: 14px; font-weight: 800; }
@media (max-width: 760px) { .radar-cols { grid-template-columns: 1fr; } }

/* paper trading */
.pstats { display: grid; grid-template-columns: repeat(4, 1fr); gap: 10px; margin-bottom: 16px; }
.pstat { background: #14161b; border: 1px solid #23262d; border-radius: 12px; padding: 12px 14px; }
.psub { font-size: 14px; margin: 18px 0 8px; }
.dir { font-size: 11px; padding: 2px 8px; border-radius: 6px; }
.dir.long { background: #103a24; color: #2ec26b; } .dir.short { background: #3a1010; color: #ff5c5c; }
.otag { font-size: 11px; padding: 2px 8px; border-radius: 6px; }

/* 支撐壓力卡片 */
.sup-cards { display: grid; grid-template-columns: repeat(4, 1fr); gap: 10px; margin: 6px 0 4px; }
.sup-card { background: #16181d; border: 1px solid #23262d; border-radius: 10px; padding: 12px 14px; }
.sup-card.broken { border-color: #6a2020; background: #1c1416; }
.sup-card.broke { border-color: #1f5a34; background: #101a14; }
.sup-coin { font-size: 15px; font-weight: 700; color: #f4f5f7; }
.sup-price { font-size: 11px; color: #8b909a; margin-top: 2px; }
.sup-level { font-size: 13px; color: #c8ccd4; margin-top: 8px; }
.sup-level b { font-size: 15px; }
.sup-level b.long { color: #2ec26b; } .sup-level b.short { color: #ff5c5c; }
.sup-level small { font-size: 10px; color: #6b7078; }
.sup-tag { display: inline-block; margin-top: 10px; font-size: 11px; padding: 2px 8px; border-radius: 6px; }
.sup-tag.short { background: #3a1010; color: #ff5c5c; }
.sup-tag.long { background: #103a24; color: #2ec26b; }
.sup-tag.neutral { background: #1f2229; color: #b8bcc4; }
@media (max-width: 640px) { .sup-cards { grid-template-columns: repeat(2, 1fr); } }
.otag.tp { background: #103a24; color: #2ec26b; } .otag.sl { background: #3a1010; color: #ff5c5c; } .otag.expired { background: #1f2229; color: #b8bcc4; } .otag.reversed { background: #2a2410; color: #f4d774; } .otag.trail { background: #11261a; color: #4ec77f; } .otag.hedge { background: #0e2a44; color: #6db5ff; }
.tsmall { font-size: 11px; color: #8b909a; }
.upbit-link { color: #e8eaed; text-decoration: none; font-weight: 600; }
.upbit-link:hover { color: #4aa3ff; text-decoration: underline; }
.upbit-orig { font-size: 11px; color: #6b7078; margin-top: 2px; }
.newscat { display: inline-block; font-size: 11px; padding: 1px 7px; border-radius: 5px; white-space: nowrap; background: #1f2229; color: #b8bcc4; }
.newscat.nc-figure { background: #2a2410; color: #f4d774; }
.newscat.nc-cb { background: #10233a; color: #6db5ff; }
.newscat.nc-trade { background: #2f1e10; color: #f0a24b; }
.newscat.nc-geo { background: #3a1010; color: #ff8a8a; }
.newscat.nc-reg { background: #10202f; color: #7fb0d8; }
.newscat.nc-hack { background: #3a1408; color: #ff9b57; }
.newscat.nc-inst { background: #1a1030; color: #b79cff; }
.newscat.nc-whale { background: #08303a; color: #5fd0e0; }
.newscat.nc-crypto { background: #103a24; color: #4cd17e; }
.newscat.nc-misc { background: #1f2229; color: #b8bcc4; }
.navbadge { font-style: normal; font-size: 10px; font-weight: 700; background: #e0b341; color: #1a1407; border-radius: 8px; padding: 0 6px; margin-left: 6px; }
.dir { display: inline-block; font-size: 12px; font-weight: 700; padding: 2px 8px; border-radius: 6px; }
.dir.long { background: #103a24; color: #2ec26b; } .dir.short { background: #3a1010; color: #ff5c5c; }

/* market head + sort */
.mk-head { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: 10px; }
.mk-head h2 { font-size: 16px; margin: 0; }
.mk-count { font-size: 12px; color: #8b909a; }
.mk-actions { display: flex; align-items: center; gap: 10px; }
.clearbtn { background: transparent; border: 1px solid #4a2c2c; color: #c56a6a; font-size: 11px; padding: 3px 9px; border-radius: 6px; cursor: pointer; transition: .15s; }
.clearbtn:hover { border-color: #e05555; color: #e05555; background: rgba(224, 85, 85, 0.08); }
.sorttabs { display: flex; gap: 8px; margin-bottom: 8px; }
.sorttabs button { background: #16181d; border: 1px solid #23262d; color: #b8bcc4; padding: 5px 12px; border-radius: 8px; cursor: pointer; font-size: 12px; }
.sorttabs button.active { background: #2a2410; border-color: #e0b341; color: #f4d774; }

/* tables */
.grid { width: 100%; border-collapse: collapse; font-size: 13px; }
.grid th { padding: 8px 10px; color: #8b909a; font-weight: 500; border-bottom: 1px solid #23262d; text-align: left; }
.grid th.r { text-align: right; } .grid th.rank { width: 36px; }
.grid td { padding: 9px 10px; border-bottom: 1px solid #14161b; font-variant-numeric: tabular-nums; }
.grid td.r { text-align: right; } .grid td.rank { color: #5c616b; }
.grid tr.clickable { cursor: pointer; }
.grid tr.clickable:hover td { background: #14161b; }
.grid tr.selected td { background: #2a241018; }
.coin { font-weight: 600; }
.vol { color: #8b909a; }
.chip { display: inline-block; padding: 2px 8px; border-radius: 6px; font-weight: 600; font-variant-numeric: tabular-nums; }
.chip.long { background: #103a24; color: #2ec26b; } .chip.short { background: #3a1010; color: #ff5c5c; }
.score { font-weight: 700; }
footer { margin-top: 24px; font-size: 11px; color: #5c616b; line-height: 1.6; }
.loading { color: #8b909a; }

/* drawer */
.overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.5); display: flex; justify-content: flex-end; z-index: 50; }
.drawer { width: 460px; max-width: 92vw; height: 100%; overflow-y: auto; background: #0d0f13; border-left: 1px solid #23262d; padding: 20px 18px 48px; box-sizing: border-box; }
.close { position: sticky; top: 0; float: right; background: #16181d; border: 1px solid #23262d; color: #b8bcc4; width: 30px; height: 30px; border-radius: 8px; cursor: pointer; }
.drawer .card { margin-bottom: 14px; }
.drawer h3 { margin: 0 0 12px; font-size: 14px; font-weight: 600; } .drawer h3 .sub { font-size: 11px; color: #8b909a; }
.rationale.long { border-color: #2ec26b55; } .rationale.short { border-color: #ff5c5c55; }
.rationale-head { display: flex; align-items: center; gap: 8px; margin-bottom: 14px; }
.rationale-head h2 { font-size: 15px; margin: 0; flex: 1; font-weight: 600; }
.rationale-head .dot { width: 9px; height: 9px; border-radius: 50%; }
.dot.long { background: #2ec26b; } .dot.short { background: #ff5c5c; } .dot.neutral { background: #8b909a; }
.badge { font-size: 20px; font-weight: 800; border-radius: 10px; padding: 6px 12px; display: flex; flex-direction: column; align-items: center; line-height: 1; }
.badge small { font-size: 10px; font-weight: 600; margin-top: 2px; }
.badge.long { background: #103a24; color: #2ec26b; } .badge.short { background: #3a1010; color: #ff5c5c; } .badge.neutral { background: #1f2229; color: #b8bcc4; }
.rationale-row { display: grid; grid-template-columns: 64px auto 1fr; gap: 8px; align-items: center; padding: 6px 0; font-size: 12px; }
.rl-label { color: #8b909a; } .rl-text { color: #c8ccd4; }
.tag { font-size: 11px; padding: 2px 8px; border-radius: 6px; justify-self: start; white-space: nowrap; }
.tag.long { background: #103a24; color: #2ec26b; } .tag.short { background: #3a1010; color: #ff5c5c; } .tag.neutral { background: #1f2229; color: #c8ccd4; }
.stats { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; margin-bottom: 14px; }
.stat { background: #14161b; border: 1px solid #23262d; border-radius: 12px; padding: 12px 14px; }
.stat.span2 { grid-column: span 2; }
.stat-k { font-size: 11px; color: #8b909a; margin-bottom: 6px; }
.stat-v { font-size: 18px; font-weight: 700; font-variant-numeric: tabular-nums; }
.dots { display: flex; gap: 4px; }
.seg { flex: 1; height: 12px; border-radius: 3px; background: #23262d; }
.seg.on.long { background: #2ec26b; } .seg.on.short { background: #ff5c5c; } .seg.on.neutral { background: #8b909a; }
.bd-row { display: grid; grid-template-columns: 84px 1fr auto; gap: 8px; align-items: center; padding: 8px 0; border-bottom: 1px solid #1c1f25; font-size: 12px; }
.bd-row:last-child { border-bottom: none; }
.bd-label { font-weight: 600; } .bd-note { color: #8b909a; }
.bd-score { font-weight: 700; font-variant-numeric: tabular-nums; justify-self: end; }
.bd-row.info { opacity: 0.55; }
.bd-score.muted { font-weight: 500; font-size: 11px; color: #8b909a; border: 1px solid #2a2e36; border-radius: 5px; padding: 1px 6px; }
.bd-row.total { margin-top: 4px; border-top: 1px solid #23262d; padding-top: 10px; }
.related { display: grid; grid-template-columns: repeat(auto-fill, minmax(64px, 1fr)); gap: 8px; }
.rc { background: #0d0f13; border: 1px solid #23262d; border-radius: 10px; padding: 8px 4px; cursor: pointer; text-align: center; }
.rc:hover { border-color: #e0b341; }
.rc-coin { font-size: 12px; font-weight: 700; }
.rc-chg { font-size: 11px; margin: 3px 0; font-variant-numeric: tabular-nums; }
.rc-score { font-size: 11px; font-weight: 700; border-radius: 5px; padding: 1px 0; }
.rc-score.long { background: #103a24; } .rc-score.short { background: #3a1010; } .rc-score.neutral { background: #1f2229; }
</style>

<style>
/* ---- JMCH auth gate (login / register) ---- */
.authgate {
  position: fixed; inset: 0; z-index: 9999;
  display: flex; align-items: center; justify-content: center;
  background: radial-gradient(1200px 600px at 50% -10%, #1a1710, #0b0c0f 70%);
  padding: 20px;
}
.authcard {
  width: 100%; max-width: 380px;
  background: #14161c; border: 1px solid #2a2620;
  border-radius: 16px; padding: 28px 24px 24px;
  box-shadow: 0 20px 60px rgba(0,0,0,.5);
  display: flex; flex-direction: column; gap: 12px;
}
.authlogo { width: 190px; margin: 0 auto 2px; display: block; }
.authslogan { text-align: center; margin: -6px 0 8px; font-size: 12px; letter-spacing: 1px;
  color: #b9902f; font-weight: 600; }
.authtabs { display: flex; gap: 8px; margin-bottom: 4px; }
.authtabs button {
  flex: 1; padding: 9px 0; border-radius: 9px; cursor: pointer;
  background: #1b1e25; border: 1px solid #2a2e37; color: #8b909a; font-weight: 700;
}
.authtabs button.on { background: linear-gradient(180deg, #d8ad48, #b8862a); color: #201800; border-color: #d8ad48; }
.authin {
  width: 100%; box-sizing: border-box; padding: 11px 12px; border-radius: 9px;
  background: #0f1116; border: 1px solid #2a2e37; color: #e8e9ec; font-size: 14px;
}
.authin:focus { outline: none; border-color: #d8ad48; }
.authfile {
  display: block; padding: 11px 12px; border-radius: 9px; cursor: pointer; text-align: center;
  background: #0f1116; border: 1px dashed #4a412a; color: #b9902f; font-size: 13px;
}
.authbtn {
  margin-top: 4px; padding: 11px 0; border: none; border-radius: 10px; cursor: pointer;
  background: linear-gradient(180deg, #e6bd54, #c2902e); color: #201800; font-weight: 800; font-size: 15px;
}
.authbtn:hover { filter: brightness(1.05); }
.authmsg { text-align: center; color: #8b909a; padding: 20px 0; }
.authnote { text-align: center; color: #e8b84b; background: #2a2410; border: 1px solid #4a412a;
  border-radius: 8px; padding: 8px; font-size: 13px; }
.autherr { color: #ef6b6b; font-size: 13px; text-align: center; }
.authok { color: #34d399; font-size: 13px; text-align: center; line-height: 1.5; }
.authhint { color: #6b7078; font-size: 11px; text-align: center; margin: 2px 0 0; line-height: 1.5; }
</style>

<style>
/* ---- admin review (Feature 2) ---- */
.reviewgrid { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 12px; }
.reviewcard { display: flex; gap: 10px; background: #0f1116; border: 1px solid #2a2e37; border-radius: 10px; padding: 10px; }
.reviewproof { width: 92px; height: 92px; flex: none; border-radius: 8px; overflow: hidden; cursor: zoom-in; background: #05060a; border: 1px solid #23262d; }
.reviewproof img { width: 100%; height: 100%; object-fit: cover; }
.reviewproof.empty { display: flex; align-items: center; justify-content: center; font-size: 11px; color: #6b7078; cursor: default; }
.reviewinfo { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 3px; }
.ri-name { font-weight: 800; color: #e8e9ec; }
.ri-row { font-size: 12px; color: #9aa0ac; }
.reviewact { margin-top: auto; display: flex; gap: 6px; padding-top: 6px; }
.okbtn { flex: 1; padding: 6px 0; border: none; border-radius: 7px; cursor: pointer; background: #17643c; color: #d4f7e2; font-weight: 700; }
.nobtn { flex: 1; padding: 6px 0; border: none; border-radius: 7px; cursor: pointer; background: #5a1f1f; color: #f7d4d4; font-weight: 700; }
.proofthumb { width: 34px; height: 34px; object-fit: cover; border-radius: 6px; cursor: zoom-in; border: 1px solid #23262d; }
.proofbox { display: flex; align-items: center; justify-content: center; background: rgba(0,0,0,.85); z-index: 9998; }
.cfg-sel { max-width: 200px; }
.prooffull { max-width: 90vw; max-height: 82vh; border-radius: 8px; box-shadow: 0 10px 40px rgba(0,0,0,.6); cursor: zoom-out; }
.proofclose { position: fixed; z-index: 9999; cursor: pointer;
  top: calc(12px + env(safe-area-inset-top)); right: calc(12px + env(safe-area-inset-right));
  width: 44px; height: 44px; border-radius: 50%; border: none;
  background: rgba(255,255,255,.14); color: #fff; font-size: 22px; line-height: 1;
  display: flex; align-items: center; justify-content: center; }
.proofclose:active { background: rgba(255,255,255,.28); }
.qtag.warn { background: #4a3a10; color: #e8b84b; }
.qtag.bad { background: #4a1414; color: #ef8a8a; }
.regbtn.on { background: linear-gradient(180deg, #d8ad48, #b8862a); color: #201800; border-color: #d8ad48; }
/* toggle switch (啟用/停用) */
.switch { position: relative; display: inline-block; width: 42px; height: 22px; cursor: pointer; }
.switch input { opacity: 0; width: 0; height: 0; }
.sw-track { position: absolute; inset: 0; border-radius: 22px; background: #5a1f1f; transition: .2s; }
.sw-track::before { content: ''; position: absolute; width: 16px; height: 16px; left: 3px; top: 3px; border-radius: 50%; background: #fff; transition: .2s; }
.switch input:checked + .sw-track { background: #17643c; }
.switch input:checked + .sw-track::before { transform: translateX(20px); }
</style>

<style>
/* ---- articles (Feature 3) ---- */
.artgrid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 14px; }
.artcard { background: #14161c; border: 1px solid #23262d; border-radius: 12px; overflow: hidden; cursor: pointer; transition: .15s; }
.artcard:hover { border-color: #d8ad48; transform: translateY(-2px); }
.artcard.pinned { border-color: #d8ad48; }
.artcard-cover { position: relative; height: 130px; background: #0b0c0f; display: flex; align-items: center; justify-content: center; }
.artcard-cover img { width: 100%; height: 100%; object-fit: cover; }
.artcard-cover span { color: #3a3222; font-weight: 900; font-size: 24px; letter-spacing: 2px; }
.pinbadge { position: absolute; top: 6px; left: 6px; background: #d8ad48; color: #14161c; font-size: 12px; font-weight: 700; padding: 1px 5px; border-radius: 5px; }
.artcard-body { padding: 10px 12px; }
.artcard-title { font-weight: 800; color: #e8e9ec; line-height: 1.35; overflow-wrap: break-word; }
.artcard-foot { display: flex; align-items: center; justify-content: space-between; margin-top: 6px; }
.artcard-date { font-size: 11px; color: #6b7078; }
.pinbtn { background: transparent; border: 1px solid #3a3d45; color: #9aa0a8; font-size: 11px; padding: 2px 8px; border-radius: 6px; cursor: pointer; transition: .15s; }
.pinbtn:hover { border-color: #d8ad48; color: #d8ad48; }
.pinbtn.on { background: #d8ad48; border-color: #d8ad48; color: #14161c; font-weight: 700; }
.arttags { display: flex; flex-wrap: wrap; gap: 5px; margin: 6px 0; }
.arttag { font-size: 11px; padding: 1px 8px; border-radius: 10px; background: #2a2410; color: #e8b84b; }
.backbtn { background: none; border: none; color: #8b909a; cursor: pointer; font-size: 13px; margin-bottom: 10px; }
.artpost { max-width: 760px; margin: 0 auto; }
.artcover { width: 100%; border-radius: 12px; margin-bottom: 14px; }
.arttitle { font-size: 26px; color: #f0f1f4; margin: 4px 0 8px; overflow-wrap: break-word; }
.artblock { margin: 12px 0; }
.arttext { color: #cdd0d6; line-height: 1.85; white-space: pre-wrap; overflow-wrap: break-word; word-break: break-word; }
.artimg { width: 100%; border-radius: 10px; }
.artadmin { display: flex; gap: 8px; margin-top: 20px; border-top: 1px solid #23262d; padding-top: 14px; }
/* editor */
.arteditor { width: 100%; max-width: 640px; max-height: 90vh; overflow-y: auto; background: #14161c;
  border: 1px solid #2a2e37; border-radius: 14px; padding: 18px; display: flex; flex-direction: column; gap: 8px; }
.ae-head { display: flex; justify-content: space-between; align-items: center; }
.ae-head h3 { margin: 0; }
.ae-label { font-size: 12px; color: #8b909a; margin-top: 6px; }
.ae-cover { display: flex; gap: 10px; align-items: center; }
.ae-cover img { width: 120px; height: 70px; object-fit: cover; border-radius: 8px; }
.ae-block { border: 1px solid #23262d; border-radius: 10px; padding: 8px; background: #0f1116; }
.ae-block-tools { display: flex; align-items: center; gap: 6px; margin-bottom: 6px; }
.ae-btype { font-size: 11px; color: #b9902f; font-weight: 700; margin-right: auto; }
.minibtn { width: 26px; height: 24px; border: 1px solid #2a2e37; background: #1b1e25; color: #cdd0d6; border-radius: 6px; cursor: pointer; }
.minibtn.del { color: #ef8a8a; }
.ae-textarea { width: 100%; box-sizing: border-box; min-height: 80px; resize: vertical; background: #0b0c0f;
  border: 1px solid #2a2e37; border-radius: 8px; color: #e8e9ec; padding: 8px; font: inherit; }
.ae-imgblock { display: flex; gap: 10px; align-items: center; }
.ae-imgblock img { width: 120px; height: 70px; object-fit: cover; border-radius: 8px; }
.ae-addrow { display: flex; gap: 8px; margin-top: 4px; }
.ae-foot { display: flex; gap: 8px; margin-top: 10px; }
.ae-foot .authbtn { flex: 1; }
</style>

<style>
/* ---- social bar + floating QR + admin config (Feature 4) ---- */
footer { padding: 18px 0 30px; text-align: center; }
.socialbar { display: flex; justify-content: center; gap: 12px; margin-bottom: 12px; }
.social { width: 40px; height: 40px; border-radius: 50%; display: flex; align-items: center; justify-content: center;
  color: #fff; font-weight: 900; font-size: 18px; text-decoration: none; transition: .15s; }
.social:hover { transform: translateY(-3px) scale(1.08); }
.foot-note { color: #6b7078; font-size: 12px; margin: 0; }
.qrfloat { position: fixed; right: 18px; bottom: 18px; z-index: 500; background: #fff; border-radius: 12px;
  padding: 8px 8px 4px; box-shadow: 0 8px 30px rgba(0,0,0,.4); text-align: center; }
.qrfloat img { width: 96px; height: 96px; display: block; border-radius: 6px; }
.qrcap { font-size: 11px; color: #333; font-weight: 700; }
.qrclose { position: absolute; top: -8px; right: -8px; width: 22px; height: 22px; border-radius: 50%; border: none;
  background: #333; color: #fff; cursor: pointer; font-size: 12px; line-height: 1; }
.cfg-row { display: flex; align-items: center; gap: 10px; margin: 8px 0; flex-wrap: wrap; }
.cfg-k { width: 100px; color: #8b909a; font-size: 13px; }
.cfg-logo { height: 34px; }
.cfg-qr { height: 54px; border-radius: 6px; }
.cfg-file { display: inline-block; width: auto; padding: 6px 12px; }
.cfg-sub { color: #b9902f; margin: 14px 0 6px; font-size: 14px; }
.cfg-social { display: flex; gap: 8px; margin: 6px 0; align-items: center; }
.cfg-social select { background: #0f1116; border: 1px solid #2a2e37; color: #e8e9ec; border-radius: 8px; padding: 8px; }
.cfg-social .authin { flex: 1; }
</style>

<style>
.delbtn { background: #3a1414; border: 1px solid #5a1f1f; color: #ef8a8a; border-radius: 6px; padding: 4px 9px; cursor: pointer; }
.delbtn:hover { background: #5a1f1f; }
</style>

<style>
/* ---- toast prompt ---- */
.toast { position: fixed; top: 22px; left: 50%; transform: translateX(-50%); z-index: 10000;
  padding: 12px 22px; border-radius: 10px; font-weight: 700; font-size: 14px; color: #fff;
  box-shadow: 0 8px 30px rgba(0,0,0,.45); max-width: 88vw; text-align: center; }
.toast.ok { background: linear-gradient(180deg, #1c8a54, #146b40); }
.toast.err { background: linear-gradient(180deg, #c23b3b, #9a2727); }
.toastfade-enter-active, .toastfade-leave-active { transition: opacity .25s, transform .25s; }
.toastfade-enter-from, .toastfade-leave-to { opacity: 0; transform: translate(-50%, -12px); }
</style>

<style>
/* ---- help bubble (?) ---- */
.help { display: inline-flex; align-items: center; justify-content: center; width: 17px; height: 17px;
  border-radius: 50%; background: #2a2e37; color: #b9902f; font-size: 11px; font-weight: 800;
  cursor: help; margin-left: 6px; position: relative; vertical-align: middle; outline: none; user-select: none; }
.help:hover { background: #3a3f4a; }
.help-pop { display: none; position: absolute; top: 24px; left: 0; z-index: 300; width: min(320px, 80vw);
  background: #1b1e25; border: 1px solid #3a3f4a; border-radius: 8px; padding: 10px 12px;
  font-size: 12px; font-weight: 400; line-height: 1.65; color: #cdd0d6; text-align: left; white-space: normal;
  box-shadow: 0 10px 34px rgba(0,0,0,.55); }
.help-pop b { color: #e8b84b; }
/* desktop mouse: show on hover. touch/click: toggle via .open (JS) so tapping
   elsewhere closes it (mobile :hover would otherwise stick until re-render). */
@media (hover: hover) { .help:hover .help-pop { display: block; } }
.help.open .help-pop { display: block; }
.mk-head .help, h2 .help, h3 .help { font-weight: 800; }
</style>

<style>
/* ---- 大盤方向 (BTC/ETH EMA bias, EMA-strategy page) ---- */
.mkt-bias { display: flex; align-items: stretch; flex-wrap: wrap; gap: 10px; margin: 0 0 14px;
  padding: 12px 14px; background: #14171d; border: 1px solid #262b34; border-radius: 10px; }
.mkt-label { display: inline-flex; align-items: center; align-self: center; font-size: 13px; font-weight: 700;
  color: #8b909a; margin-right: 4px; }
.mkt-chip { display: inline-flex; flex-direction: column; gap: 2px; padding: 8px 14px; border-radius: 8px;
  background: #1b1e25; border: 1px solid #2f3540; min-width: 128px; }
.mkt-chip.long { border-color: #1f7a4d; background: #10261c; }
.mkt-chip.short { border-color: #8a3030; background: #2a1414; }
.mkt-chip.na { opacity: .7; }
.mkt-coin { font-size: 14px; font-weight: 800; color: #e8eaed; }
.mkt-dir { font-size: 15px; font-weight: 800; }
.mkt-chip.long .mkt-dir { color: #35d07f; }
.mkt-chip.short .mkt-dir { color: #ff5c5c; }
.mkt-chip.na .mkt-dir { color: #8b909a; }
.mkt-sub { font-size: 11px; color: #8b909a; }
</style>

<style>
/* ---- admin CSV export button (strategy tabs) ---- */
.csvbtn { margin-left: auto; background: #17321f; border: 1px solid #2f7a4d; color: #7fe0a6;
  padding: 5px 12px; border-radius: 8px; cursor: pointer; font-size: 12px; font-weight: 700; white-space: nowrap; }
.csvbtn:hover { background: #1f4a2c; }
.exitbtn { background: #3a1010; border: 1px solid #7a2f2f; color: #ff9a9a; padding: 4px 10px;
  border-radius: 7px; cursor: pointer; font-size: 12px; font-weight: 700; white-space: nowrap; }
.exitbtn:hover { background: #4a1616; }
</style>

<style>
/* ================= responsive / mobile ================= */

/* tablet: stack the home hero + wide option grids */
@media (max-width: 820px) {
  .cards { grid-template-columns: 1fr; }
  .opt-metrics { grid-template-columns: repeat(2, 1fr); }
  .opt-walls { grid-template-columns: 1fr; }
}

/* phones */
@media (max-width: 640px) {
  html, body { overflow-x: hidden; }

  /* top bar: wrap, drop the decorative search box, let controls flow to row 2 */
  .topbar { flex-wrap: wrap; gap: 8px 10px;
    padding: calc(8px + env(safe-area-inset-top)) calc(12px + env(safe-area-inset-right)) 8px calc(12px + env(safe-area-inset-left)); }
  .topbar .search { display: none; }
  .tickers { gap: 12px; font-size: 12px; }
  .topmeta { margin-left: 0; width: 100%; flex-wrap: wrap; gap: 6px; }
  .topmeta .brand { display: none; }
  .regbtn { padding: 4px 8px; font-size: 11px; }

  /* page gutter */
  .wrap { padding: 12px 12px 56px; }

  /* nav chips a touch tighter */
  .mainnav { gap: 6px; margin: 6px 0 12px; }
  .navrow { gap: 8px; }
  .navrow + .navrow { padding-top: 6px; }
  .navgroup { flex-basis: 30px; font-size: 10.5px; padding-top: 7px; }
  .navbtns { gap: 6px; }
  .mainnav button { padding: 6px 11px; font-size: 12px; }

  /* section headers wrap instead of squashing */
  .mk-head { flex-wrap: wrap; gap: 4px 8px; }
  .csvbtn { margin-left: 0; }

  /* wide tables scroll horizontally inside their own section/card */
  .grid { display: block; overflow-x: auto; white-space: nowrap; -webkit-overflow-scrolling: touch; }
  .grid th, .grid td { padding: 7px 8px; }

  /* stat tiles: 2-up */
  .pstats { grid-template-columns: repeat(2, 1fr); gap: 8px; }

  /* 大盤方向: label on its own line, chips share the row */
  .mkt-label { width: 100%; margin: 0 0 2px; }
  .mkt-chip { flex: 1 1 130px; min-width: 0; }

  /* risk / warning strips: tighter padding */
  .riskbar, .ddbanner { padding: 7px 12px; }

  /* floating QR smaller so it doesn't cover content */
  .qrfloat { right: 10px; bottom: 10px; padding: 6px 6px 3px; }
  .qrfloat img { width: 72px; height: 72px; }

  /* keep the ? popover inside the viewport */
  .help-pop { width: min(280px, 78vw); }

  /* article hero title down a notch */
  .arttitle { font-size: 21px; }
}

/* very narrow */
@media (max-width: 380px) {
  .mainnav button { padding: 5px 9px; font-size: 11.5px; }
  .authcard { padding: 22px 16px 18px; }
  .tickers .tk { font-size: 11px; }
}
</style>

<style>
/* ---- admin user filters ---- */
.userfilter { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; margin: 4px 0 12px; }
.userfilter .tf-label { font-size: 12px; color: #8b909a; margin: 0 2px 0 6px; }
.userfilter .tf-label:first-child { margin-left: 0; }
.userfilter button { background: #16181d; border: 1px solid #23262d; color: #c8cdd6;
  padding: 4px 11px; border-radius: 8px; cursor: pointer; font-size: 12px; }
.userfilter button.on { background: #2a2410; border-color: #e0b341; color: #f4d774; }
</style>

<style>
/* ---- admin user date-range inputs ---- */
.userfilter .datein { background: #0d0f13; border: 1px solid #2a2d35; color: #e8eaed;
  border-radius: 7px; padding: 3px 8px; font-size: 12px; color-scheme: dark; }
.userfilter .tf-sep { color: #8b909a; font-size: 12px; }
</style>

<style>
/* ---- register: conditions note + Bitunix referral CTA ---- */
.regcond { display: flex; flex-direction: column; gap: 4px; background: #2a2410;
  border: 1px solid #4a412a; border-radius: 10px; padding: 10px 12px; font-size: 12.5px;
  color: #cdd0d6; line-height: 1.6; }
.regcond b { color: #e8b84b; font-size: 13px; }
.bitunix-cta { display: block; text-align: center; padding: 12px 10px; border-radius: 10px;
  background: linear-gradient(180deg, #e6bd54, #c2902e); color: #201800; font-weight: 800;
  font-size: 14px; text-decoration: none; box-shadow: 0 4px 16px rgba(216,173,72,.25); }
.bitunix-cta:hover { filter: brightness(1.06); }
</style>

<style>
/* ---- 登入成功 使用須知 modal ---- */
.welcomeov { justify-content: center; align-items: center; }
.welcomebox { width: min(420px, 92vw); max-height: 86vh; overflow-y: auto;
  background: #14161c; border: 1px solid #4a412a; border-radius: 16px;
  padding: 22px 20px; display: flex; flex-direction: column; gap: 12px;
  box-shadow: 0 20px 60px rgba(0,0,0,.55); }
.welcomebox h3 { margin: 0; color: #e8b84b; text-align: center; font-size: 17px; }
.wc-sec { background: #0f1116; border: 1px solid #2a2e37; border-radius: 10px; padding: 10px 12px; }
.wc-title { color: #e8b84b; font-weight: 800; font-size: 14px; margin-bottom: 6px; }
.wc-sec p { margin: 3px 0; font-size: 13px; color: #cdd0d6; line-height: 1.7; }
.wc-sec b { color: #e8b84b; }
</style>

<style>
.wc-link { color: #e8b84b; font-weight: 700; text-decoration: underline; }
.wc-link:hover { filter: brightness(1.15); }
</style>

<style>
/* ---- social brand logos ---- */
.social-svg { display: inline-flex; align-items: center; justify-content: center; }
.social-svg svg { display: block; }
.cfg-ico { flex: none; width: 30px; height: 30px; border-radius: 50%; display: inline-flex;
  align-items: center; justify-content: center; color: #fff; font-weight: 900; font-size: 15px; }
.cfg-ico svg { width: 17px; height: 17px; display: block; }
</style>
