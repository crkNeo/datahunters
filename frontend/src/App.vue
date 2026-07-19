<script setup>
import { ref, onMounted, onUnmounted, computed, watch } from 'vue'
import BattleField from './components/BattleField.vue'
import TabPermissions from './components/admin/TabPermissions.vue'
import StrategySettings from './components/admin/StrategySettings.vue'
import LoginNotice from './components/admin/LoginNotice.vue'
import PushBroadcast from './components/admin/PushBroadcast.vue'
import SiteSettings from './components/admin/SiteSettings.vue'
import UserManagement from './components/admin/UserManagement.vue'
import ReferralRules from './components/admin/ReferralRules.vue'
import { useRoute, useRouter } from 'vue-router'
import { token, setToken, authFetch, setUnauthorizedHandler } from './lib/api'
import { validImage, uploadImage } from './lib/upload'
import { fmtPct, fmtPrice, fmtNum, fundClock, fmtClock, lvlPct, pctOf, outcomeCls, outcomeCN as convOutcome } from './lib/format'
import SectorBoard from './components/SectorBoard.vue'
import StrategyBook from './components/StrategyBook.vue'
import FundingBoard from './components/FundingBoard.vue'
import UnlockBoard from './components/UnlockBoard.vue'
import LiquidationBoard from './components/LiquidationBoard.vue'
import EventsBoard from './components/EventsBoard.vue'
import UpbitBoard from './components/UpbitBoard.vue'
import NewsBoard from './components/NewsBoard.vue'
import RobinhoodBoard from './components/RobinhoodBoard.vue'
import { ROUTE_TABS } from './router'

// ---- shared data ----
const home = ref(null)
const board = ref({})
const boardUpdated = ref('')
const error = ref('')
let timer = null

const mainTab = ref('ranking')
const marketSort = ref('vol') // vol | gainers | losers

// ---- auth (public web build) ----
// token 與 authFetch 已移到 src/lib/api.js(axios 實作),這裡只保留 UI 狀態。
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
  setToken('')
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
// 關閉登入/註冊彈窗並清掉殘留訊息,免得下次打開還看到上次的錯誤
function closeAuthModal() {
  loginOpen.value = false
  loginErr.value = ''
  regErr.value = ''
  regDone.value = ''
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
    setToken(d.token)
    role.value = d.role
    status.value = 'active'
    username.value = d.username
    authMsg.value = ''
    loginOpen.value = false
    loginForm.value = { u: '', p: '' }
    showToast('登入成功,歡迎回來!', 'ok')
    welcomeOpen.value = true // 使用須知 modal(續用資格 / 加入主畫面 / 訊號提醒)
    loadAll()
    loadNotice().then(maybeShowNotice) // 登入公告彈窗(若有且未關閉過)
  } catch (e) {
    loginErr.value = '登入失敗'
    showToast('登入失敗,請稍後再試', 'err')
  }
}
const welcomeOpen = ref(false)
function onRegFile(e) {
  const f = (e.target.files && e.target.files[0]) || null
  if (f && !validImage(f, (m) => showToast(m, 'err'))) { e.target.value = ''; regFile.value = null; return }
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
  if (pendingRef.value) fd.append('referralCode', pendingRef.value) // 註冊是唯一的綁定時機
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
async function loadUsers() {
  if (!can('admin')) return
  try {
    const res = await authFetch('/api/admin/users')
    if (res.ok) users.value = (await res.json()) || []
  } catch (e) {
    /* ignore */
  }
}
const proofView = ref('')

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
  if (f) artEdit.value.cover = await uploadImage(f, 'articles', (m) => showToast(m, 'err'))
}
async function onBlockImg(e, i) {
  const f = e.target.files && e.target.files[0]
  if (f) artEdit.value.blocks[i].image = await uploadImage(f, 'articles', (m) => showToast(m, 'err'))
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

// ---- login notice popup (公告彈窗) ----
const notice = ref(null)
// ---- 推薦系統 ----
// ⚠️ pendingRef 刻意「只放記憶體」:用戶要求關閉頁面與重新整理都要清除。
// localStorage 跨分頁存活、sessionStorage 撐過重新整理 —— 兩者都不符合,所以用純 ref,
// 並在讀取後把 URL 參數清掉(見 onMounted)。代價:使用者若在註冊前重新整理,推薦歸屬會消失。
const pendingRef = ref('')
const refShow = ref(false)
const refData = ref(null)
const refBusy = ref(false)
const refUrl = computed(() => refData.value && refData.value.code ? location.origin + '/?referralCode=' + refData.value.code : '')
async function loadReferral() {
  try {
    const res = await authFetch('/api/referral')
    if (res.ok) refData.value = await res.json()
  } catch (e) { /* secondary */ }
}
function openReferral() { refShow.value = true; loadReferral(); loadRefRules() }
async function copyText(t) {
  try { await navigator.clipboard.writeText(t); showToast('已複製') }
  catch (e) { showToast('複製失敗,請長按選取', 'err') }
}
// 推廣規則。後端未發佈時回空的,所以 text 是空字串就代表「不要顯示入口」——
// 前端不用另外判斷 published。
const refRules = ref({ title: '', text: '', published: false })
const refRulesShow = ref(false)
// 空行分段;段落內的單一換行由 CSS 的 white-space: pre-line 保留。
const refRuleParas = computed(() => (refRules.value.text || '').split(/\n{2,}/).filter(p => p.trim()))
async function loadRefRules() {
  try {
    const res = await authFetch('/api/referral/rules')
    if (res.ok) refRules.value = await res.json()
  } catch (e) { /* secondary */ }
}

// kind: 'usdt'(30U) 或 'merch'(BITUNIX 周邊)。兩者共用兌換額度,各消耗一次。
async function applyReward(kind = 'usdt') {
  if (refBusy.value) return
  refBusy.value = true
  try {
    const body = new URLSearchParams({ kind })
    const res = await authFetch('/api/referral/apply', { method: 'POST', body })
    if (res.ok) { showToast('已送出申請,待管理員審核'); await loadReferral() }
    else showToast(await res.text(), 'err')
  } catch (e) { showToast('申請失敗', 'err') }
  refBusy.value = false
}

// ---- admin: 推廣管理 ----
const refAdmin = ref(null)
const refOfShow = ref(false)
const refOfData = ref(null)
const refOfUser = ref('')
// 周邊庫存。total 是後台設定的總量,used/left 由後端從申請數算出來(不另存,
// 免得庫存和實際申請對不起來)。
const merchStock = ref({ total: 0, used: 0, left: 0 })
const merchInput = ref(0)
const merchBusy = ref(false)
async function loadMerchStock() {
  try {
    const res = await authFetch('/api/admin/merch-stock')
    if (res.ok) { merchStock.value = await res.json(); merchInput.value = merchStock.value.total }
  } catch (e) { /* secondary */ }
}
async function saveMerchStock() {
  if (merchBusy.value) return
  const n = Number(merchInput.value)
  if (!Number.isInteger(n) || n < 0) { showToast('庫存總量必須是 0 或正整數', 'err'); return }
  merchBusy.value = true
  try {
    const res = await authFetch('/api/admin/merch-stock', { method: 'POST', body: new URLSearchParams({ total: String(n) }) })
    if (res.ok) { merchStock.value = await res.json(); merchInput.value = merchStock.value.total; showToast('庫存已更新') }
    else showToast(await res.text(), 'err')
  } catch (e) { showToast('更新失敗', 'err') }
  merchBusy.value = false
}
async function loadRefAdmin() {
  try {
    const res = await authFetch('/api/admin/referrals')
    if (res.ok) refAdmin.value = await res.json()
  } catch (e) { /* secondary */ }
  await loadMerchStock()
}
// 使用者管理點名字 → 該用戶的推廣名單(全名,不遮罩)
async function openRefOf(u) {
  refOfUser.value = u
  refOfData.value = null
  refOfShow.value = true
  try {
    const res = await authFetch('/api/admin/referral-of?user=' + encodeURIComponent(u))
    if (res.ok) refOfData.value = await res.json()
  } catch (e) { /* secondary */ }
}
async function setRefOK(name, ok) {
  const res = await authFetch('/api/admin/referral-ok', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: name, ok }),
  })
  if (!res.ok) { showToast('更新失敗', 'err'); return }
  showToast(ok ? '已標記合格' : '已改為未達成')
  loadRefAdmin()
  if (refOfShow.value) openRefOf(refOfUser.value)
}
async function approveReward(id) {
  const res = await authFetch('/api/admin/referral-approve', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id }),
  })
  if (!res.ok) { showToast('審核失敗', 'err'); return }
  showToast('已通過'); loadRefAdmin()
}

const noticeShow = ref(false)
const noticeDont = ref(false)
async function loadNotice() {
  try {
    const res = await authFetch('/api/notice')
    if (res.ok) notice.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
function maybeShowNotice() {
  const n = notice.value
  if (!n || !n.active) return
  if (localStorage.getItem('notice_seen') === String(n.ver)) return // dismissed this version
  noticeDont.value = false
  noticeShow.value = true
}
function closeNotice() {
  if (noticeDont.value && notice.value) localStorage.setItem('notice_seen', String(notice.value.ver))
  noticeShow.value = false
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
const emaOnly = ref(null)

// ---- VIP: 冥王星 (動態ATR 4H 均線收斂) strategy ----
const conv = ref(null)
async function loadConv() {
  try {
    const res = await authFetch('/api/conv')
    if (res.ok) conv.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
// admin: force-close one open trade in any strategy (recorded as 動能衰弱)
async function manualExitStrat(book, id, reload) {
  if (!confirm('確定手動出場此單?將以現價結算並標記「動能衰弱」。')) return
  const res = await authFetch('/api/admin/manual-exit', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ book, id }),
  })
  if (!res.ok) { showToast((await res.text()).trim() || '出場失敗', 'err'); return }
  showToast('已手動出場(動能衰弱)', 'ok')
  if (reload) reload()
}
// 後台的功能分頁。標籤權限與策略設定已抽成獨立元件,各自載自己的資料。
const adminTab = ref('users')
const ADMIN_TABS = [
  ['users', '使用者'],
  ['perms', '標籤權限'],
  ['strat', '策略設定'],
  ['site', '站台設定'],
  ['notice', '登入公告'],
  ['refrules', '推廣規則'],
  ['push', '即時推播'],
]

// public: 策略類型標籤 + 風控警語旗標(給各策略頁用)
const stratMeta = ref({})
async function loadStratMeta() {
  try {
    const res = await authFetch('/api/strat-meta')
    if (res.ok) stratMeta.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
function stratTagsOf(name) { const m = stratMeta.value[name]; return (m && m.tags) || [] }
function stratRisky(name) { const m = stratMeta.value[name]; return !!(m && m.show_risk) }
// 分頁 → 策略 key。微策略分頁名本身就是 key(bollfade/meanrev/bgv2…),
// 只有雷達三本的分頁名與 book 名不同。
const STRAT_KEY_BY_TAB = { paper: 'main', gamble: 'gamble', emaonly: 'emaonly', conv: 'conv' }
const curStrat = computed(() => STRAT_KEY_BY_TAB[mainTab.value] || mainTab.value)

// ---- admin: mean-reversion strategies (布林重回 / 乖離回歸 / 布乖v2 / 布林EMA) ----
const bollfade = ref(null)
const meanrev = ref(null)
const bgv2 = ref(null)
const bollema = ref(null)
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
async function loadBgv2() {
  try {
    const res = await authFetch('/api/admin/bgv2')
    if (res.ok) bgv2.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
async function loadBollema() {
  try {
    const res = await authFetch('/api/admin/bollema')
    if (res.ok) bollema.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
// admin: wipe a strategy book's simulated trades (memory + DB), then reload it.
async function clearStrat(book, loader, closedOnly) {
  const msg = closedOnly
    ? '確定清除此策略「已結束」的單?進行中的開倉單會保留(並沿用新規則)。'
    : '確定清空此策略的全部模擬單(含進行中)?此動作無法復原。'
  if (!confirm(msg)) return
  const url = '/api/admin/strat-clear?book=' + book + (closedOnly ? '&scope=closed' : '')
  const res = await authFetch(url, { method: 'POST' })
  if (res.ok && loader) loader()
}
// current book name for the shared strategy section (星軌/超新星/銀河)
const curPaperBook = computed(() => ({ paper: 'main', gamble: 'gamble', emaonly: 'emaonly' }[mainTab.value]))
// the three mean-reversion tabs share one layout; meta drives the unified section.
const microMeta = {
  bollfade: {
    title: '布林重回 · 1h', load: loadBollfade, get: () => bollfade.value,
    help: '<b>進場</b>:前一根收盤在布林(20, 2σ)<b>外</b>、本根收<b>回</b>通道內,且方向與 EMA200 同側 → 朝中軌交易。<br><b>止損</b> 2.5×ATR,<b>最終止盈 TP3</b>=中軌(SMA20),盈虧比需 0.4–3.0。<br>多空雙向、最多 24 根、冷卻 4 根;進場以 1h 收盤判定。<br><b>分批止盈</b>(即時價執行):TP1/TP2 位在進場→TP3 的 <b>30% / 60%</b>;TP1 平 <b>50%</b>→止損移保本(進場+0.05%)、TP2 平 <b>30%</b>→止損移 TP1、TP3 平剩餘 <b>20%</b>。目標太近時自動不分批。<br><br>管理員專屬模擬單,⚠️ 非投資建議。',
  },
  bollema: {
    title: '布林EMA · 4H 突破蓄勢 · 多空', load: loadBollema, get: () => bollema.value,
    help: "<b>4H 突破蓄勢</b>,多空雙向。賭的是<b>突破後盤整、再啟動</b>,不是追突破。<br><br><b>【指標】</b>全部 4H、收盤判斷:布林(20, 2σ)中軌 = SMA20 ｜ EMA50(趨勢過濾)｜ ATR(14)<br><br><b>【進場・做多】</b>(空單完全鏡像)依序四條全成立:<br>① <b>順大勢</b>:K2 收盤 &gt; 4H EMA50<br>② <b>突破K</b>:某根收盤由下往上站上中軌(前一根收盤 ≤ 中軌、這根 &gt; 中軌)<br>③ <b>蓄勢K1</b>:下一根收盤守在中軌上方,且 ≤ 突破K收盤 × 1.02<br>④ <b>蓄勢K2</b>:再下一根收盤守在中軌上方,且 ≤ 突破K收盤 × 1.02(<b>累計漲幅 ≤ 2%</b>)<br>→ <b>K2 收盤市價進場</b><br><br><b>【出場】</b>(先到先出)<br>・<b>止損</b>:中軌 − 1.5 × ATR<br>・<b>止盈</b>:進場價 + 3 ×(進場價 − 止損價),即 <b>1:3 盈虧比</b><br>・<b>時間出場</b>:持滿 <b>180 根</b>(30 天)未解決 → 收盤平倉(極少觸發)<br><br><b>【保本位・僅通知】</b>價格首次觸及「進場 + 0.3 ×(止盈 − 進場)」時,標記 <b>🛡 已達保本位</b> 並發通知。<br>⚠️ <b>止盈止損不會被修改</b> —— 這只是提示這筆單已覆蓋風險,倉位仍照原本的 TP/SL 運作。<br><br><b>【節流】</b>同幣冷卻 3 根(4H)。<br><br>單段止盈,不分批。管理員專屬模擬單,⚠️ 非投資建議。",
  },
  bgv2: {
    title: '布乖v2 · 1h乖離 + 4h布林 · 只做空', load: loadBgv2, get: () => bgv2.value,
    help: "<b>只做空的雙腿策略</b>——1h「乖離腿」與 4h「布林腿」共用一個分頁、一個開關。<br><br><b>【通用設定】</b><br>・<b>市場過濾</b>:收盤 &lt; EMA200(各腿用自己週期的 EMA200)<br>・<b>止損</b>:進場 + <b>4.0×ATR(14)</b>(寬止損,扛插針)<br>・<b>時間出場</b>:持滿 <b>64 根</b>收盤平倉<br>・<b>盈虧比閘門</b>:RR 落在 0.4–3.0 才進場<br>・<b>冷卻</b>:訊號後 4 根內不重複進場<br>・<b>同幣互斥</b>:同一幣種同時只允許本家族<b>一個</b>倉位(兩腿誰先觸發誰佔位,另一腿跳過)<br>・所有條件以 <b>K 棒收盤</b>判斷,收盤市價進場;同根同觸止損與目標 → 一律算<b>止損</b>(保守)<br><br><b>【腿 1:乖離回歸 v2 · 1h】</b><br><b>進場</b>(同時成立):① 收盤 &lt; EMA200 ② <b>(收盤 − EMA50) / ATR &gt; +2.0</b>(反彈高出 EMA50 兩個 ATR)③ 以「目標 EMA50、止損 4 ATR」算的 RR 在 0.4–3.0<br><b>止盈</b>:進場時的 <b>EMA50</b> 值 ｜ <b>時間</b>:64 根 ≈ <b>2.7 天</b><br><br><b>【腿 2:布林重回 v2 · 4h】</b><br><b>進場</b>(同時成立):① 收盤 &lt; EMA200 ② 前一根收盤 <b>&gt; 布林(50, 2σ) 上軌</b>(衝出通道)③ 本根收盤<b>跌回上軌內、且仍在中軌(SMA50)上方</b>(收回但未跌過頭)④ 以「目標中軌、止損 4 ATR」算的 RR 在 0.4–3.0<br><b>止盈</b>:進場時的<b>中軌(SMA50)</b>值 ｜ <b>時間</b>:64 根 ≈ <b>10.7 天</b><br><br><b>【倉位】</b>每筆風險 ≤ <b>0.5%</b> 資金(歷史 maxDD 約 20–35R)。<br><br><b>單段止盈,不分批</b> — 照回測規格原樣上線。<br><br>管理員專屬模擬單,⚠️ 非投資建議。",
  },
  meanrev: {
    title: '乖離回歸 · 1h', load: loadMeanrev, get: () => meanrev.value,
    help: '<b>進場</b>:收盤價偏離 EMA20 超過 2.0×ATR,且與 EMA200 趨勢同側(上方接多、下方接空)→ 朝 EMA20 回歸。<br><b>止損</b> 3.0×ATR,<b>最終止盈 TP3</b>=EMA20。<br>多空雙向、最多 24 根、冷卻 4 根;進場以 1h 收盤判定。<br><b>分批止盈</b>(即時價執行):TP1/TP2 位在進場→TP3 的 <b>30% / 60%</b>;TP1 平 <b>50%</b>→止損移保本(進場+0.05%)、TP2 平 <b>30%</b>→止損移 TP1、TP3 平剩餘 <b>20%</b>。目標太近時自動不分批。<br><br>管理員專屬模擬單,⚠️ 非投資建議。',
  },
}
const micro = computed(() => microMeta[mainTab.value] || null)
const microState = computed(() => (micro.value ? micro.value.get() : null))
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
  const map = { paper: 'main', gamble: 'gamble', emaonly: 'emaonly' }
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
  let wins = 0, sum = 0, gW = 0, gL = 0, tp1 = 0, tp2 = 0, tp3 = 0
  for (const t of closed) {
    if (t.pnl_pct > 0) { wins++; gW += t.pnl_pct } else { gL += -t.pnl_pct }
    sum += t.pnl_pct
    if (t.legs >= 1) tp1++
    if (t.legs >= 2) tp2++
    if (t.legs >= 3) tp3++
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
      multi_tp: !!(b.stats && b.stats.multi_tp),
      profit_factor: gL > 0 ? +(gW / gL).toFixed(2) : (gW > 0 ? 99.99 : 0),
      tp1, tp2, tp3,
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


const upbitNotices = ref([])
async function loadUpbit() {
  try {
    const res = await authFetch('/api/upbit')
    if (res.ok) upbitNotices.value = await res.json()
  } catch (e) {
    /* secondary */
  }
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

// Robinhood 上架 (public)
const robinhood = ref(null)
async function loadRobinhood() {
  try {
    const res = await authFetch('/api/robinhood')
    if (res.ok) robinhood.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
const robinhoodNew = computed(() => (robinhood.value ? robinhood.value.coins.filter((c) => c.new).length : 0))

// 大盤 AI 分析 (public, hourly)
const marketAI = ref(null)
async function loadMarketAI() {
  try {
    const res = await authFetch('/api/market-ai')
    if (res.ok) marketAI.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
const maiBody = computed(() => {
  if (!marketAI.value || !marketAI.value.text) return ''
  const i = marketAI.value.text.indexOf('\n')
  return i > 0 ? marketAI.value.text.slice(i + 1).trim() : marketAI.value.text
})

// 板塊強弱/輪動 (public, hourly)


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
// 止盈位相對進場的幅度。順著單子方向算,所以空單的止盈(價格更低)一樣是正數。
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
//
// ⚠️ 這裡以前有一行 `if (!authed.value) return` —— 那是全站還鎖在登入牆後面時的寫法。
// 首頁改成公開瀏覽之後,那行會讓「未登入訪客什麼資料都載不到」:排行、快訊、Upbit、
// 財經事件、Robinhood、文章全部空白,而且畫面停在「載入中…」看起來像還在載。
// 公開端點本來就允許匿名存取,受限的端點會自己回 403,不需要在這裡先擋一次。
function loadAll() {
  loadRanking()
  loadHome()
  loadRisk()
  loadEvents()
  loadUpbit()
  loadNews()
  loadRobinhood()
  loadMarketAI()
  loadArticles()
  if (can('member')) {
    loadBoard()
    loadRadar()
    loadScoreLog()
  }
  if (can('vip')) {
    loadPaper()
    loadSR()
    loadConv()
  }
  if (can('admin')) {
    loadUsers()
    loadRefAdmin()
    // load every strategy each cycle so the nav badges show open counts without
    // having to open each tab first
    loadBollfade()
    loadMeanrev()
    loadBgv2()
    loadBollema()
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
const NAV_TABS = ['paper', 'gamble', 'emaonly', 'ranking', 'radar', 'signals', 'scorelog', 'sr', 'upbit', 'news', 'funding', 'unlock', 'robinhood', 'sectors', 'articles', 'conv', 'bollfade', 'meanrev', 'bgv2', 'bollema', 'referral']
function gotoTab(t) { if (NAV_TABS.includes(t)) mainTab.value = t }

// ---- 網址 ↔ 分頁 雙向同步 ----
// mainTab 仍是畫面的唯一依據(27 個 `mainTab = 'x'` 的呼叫點都不用改),
// router 只負責把它反映到網址上,並讓深層連結/上一頁能還原分頁。
const route = useRoute()
const router = useRouter()
let syncing = false // 防止兩個 watch 互相觸發成迴圈

watch(() => route.params.tab, (t) => {
  const next = ROUTE_TABS.includes(t) ? t : 'ranking'
  if (next === mainTab.value) return
  syncing = true
  mainTab.value = next
  syncing = false
}, { immediate: true })

watch(mainTab, (t) => {
  if (syncing) return
  const path = t === 'ranking' ? '/' : '/' + t
  if (route.path !== path) router.push(path)
})
let onVisibility = null
let onPageShow = null
let onDocClick = null
// keep an opened help bubble inside the viewport (mobile PWA: it can otherwise
// overflow off the right edge or bottom). Shift left / flip up as needed.
function positionHelpPop(help) {
  const pop = help.querySelector('.help-pop')
  if (!pop) return
  pop.style.left = '0'
  pop.style.right = 'auto'
  pop.style.top = '24px'
  pop.style.bottom = 'auto'
  requestAnimationFrame(() => {
    const m = 8
    const vw = window.innerWidth, vh = window.innerHeight
    const icon = help.getBoundingClientRect()
    let pr = pop.getBoundingClientRect()
    if (pr.right > vw - m) pop.style.left = -(pr.right - (vw - m)) + 'px' // overflow right → shift left
    pr = pop.getBoundingClientRect()
    if (pr.left < m) pop.style.left = (parseFloat(pop.style.left || '0') + (m - pr.left)) + 'px' // overflow left → nudge right
    if (icon.bottom + pr.height > vh - m && icon.top - pr.height > m) { // overflow bottom → flip above
      pop.style.top = 'auto'
      pop.style.bottom = '24px'
    }
  })
}

let hiddenAt = 0
onMounted(async () => {
  // help popovers: click the ? to toggle, click anywhere else (or another ?) to
  // close — mobile :hover would otherwise stick open until a re-render.
  onDocClick = (e) => {
    if (e.target.closest('.help-pop')) return // tap inside the tooltip → keep open
    const help = e.target.closest('.help')
    document.querySelectorAll('.help.open').forEach((el) => { if (el !== help) el.classList.remove('open') })
    if (help) {
      help.classList.toggle('open')
      if (help.classList.contains('open')) positionHelpPop(help) // keep the bubble on-screen
    }
  }
  document.addEventListener('click', onDocClick)
  loadConfig()
  loadStratMeta() // 各策略類型標籤 + 風控警語
  loadTabPerms()  // 各分頁所需最低身分(後台可調)
  await loadMe()
  loadAll()
  if (authed.value) loadNotice().then(maybeShowNotice) // 返回用戶(帶 token)登入時的公告彈窗
  timer = setInterval(tick, 15000)
  // deep-link:分頁本身(/conv 或舊格式 /?tab=conv)已由 router 處理,這裡只管
  // 附加參數。注意不能再用 history.replaceState 清網址 —— 那會蓋掉 router 的狀態。
  const qs = new URLSearchParams(location.search)
  const qart = qs.get('article')
  if (qart) openArticleById(qart)
  // 推薦連結 /?referralCode=XXXX → 記在記憶體、自動切到註冊頁。URL 隨即清掉,所以
  // 重新整理/關閉頁面推薦碼就消失(用戶指定的行為)。
  const qref = qs.get('referralCode')
  if (qref && !authed.value) {
    pendingRef.value = qref.trim().toUpperCase()
    authTab.value = 'register'
  }
  if (qart || qref) router.replace({ path: route.path }) // 只清 query,保留分頁路徑
  // 戰場的 WS/動畫由 BattleField 元件自己起停,這裡不再插手
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
    if (document.visibilityState === 'hidden') { hiddenAt = Date.now(); return } // 戰場自己會停
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
// 這份只是「後端還沒回來之前」的備援值,必須與後端 tabMeta 的預設一致。
// 真正生效的是 /api/tab-perms(後台可調),而且後端自己也會擋 —— 前端這層只決定畫面。
const TAB_MIN_ROLE_FALLBACK = {
  oi: 'member', signals: 'member', scorelog: 'member', radar: 'member',
  paper: 'vip', gamble: 'vip', emaonly: 'vip',
  sr: 'vip',
  admin: 'admin', referral: 'admin', conv: 'vip',
  bollfade: 'admin', meanrev: 'admin', bgv2: 'admin', bollema: 'admin',
}
const tabPerms = ref({})
async function loadTabPerms() {
  try {
    const res = await authFetch('/api/tab-perms')
    if (res.ok) tabPerms.value = await res.json()
  } catch (e) {
    /* 拿不到就沿用備援值 */
  }
}
// 某分頁需要的最低身分
function tabNeed(tab) {
  return tabPerms.value[tab] || TAB_MIN_ROLE_FALLBACK[tab] || 'public'
}
// 目前身分能不能看某分頁
function canTab(tab) {
  return can(tabNeed(tab))
}
// 這一列有沒有任何看得到的分頁(整列都看不到就不顯示標題)
function anyTab(tabs) {
  return tabs.some(canTab)
}
// 身分或權限設定變動時,若目前停留在看不到的分頁就退回公開首頁,
// 否則內容區會落在一個都不成立的 v-else-if 分支上、整片空白。
// 身分或權限設定變動時重新評估目前分頁。
//
// 這裡要以「網址」為準重新推導,不能只是把看不到的分頁踢回首頁 —— 因為登入狀態是
// 非同步解析的:深連 /admin 時,route 會先把 mainTab 設成 admin,但那一刻 role 還是
// public,若直接踢回 ranking,等 loadMe() 回來變成 admin 後也沒有東西會把它救回去,
// 使用者就莫名其妙停在首頁。
watch([role, tabPerms, authReady], () => {
  // 登入狀態還沒解析完就先不要動:深連 /admin 時 route 會先把 mainTab 設成 admin,
  // 但那一刻 role 仍是 public。若此時就踢回首頁,會連帶把網址改成 /,route 參數消失,
  // 等 loadMe() 回來也救不回去 —— 使用者就莫名其妙停在首頁。
  if (!authReady.value) return
  if (!canTab(mainTab.value)) mainTab.value = 'ranking'
})
</script>

<template>
  <!-- toast prompt -->
  <transition name="toastfade">
    <div v-if="toastMsg" class="toast" :class="toastType">{{ toastMsg }}</div>
  </transition>


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
      <span v-if="role !== 'public'" class="userchip"><button class="namebtn" @click="openReferral" title="我的推廣">{{ username }}</button> <em>{{ role }}</em>
        <button v-if="canInstall" class="regbtn" @click="installApp" title="安裝為 App">📲 安裝</button>
        <button v-if="notifState !== 'on'" class="regbtn" @click="enableNotifications" title="開啟推播通知">🔔 通知</button>
        <span v-else class="qtag good" title="推播已開啟">🔔 已開</span>
        <button class="regbtn" @click="logout">登出</button>
      </span>
      <button v-else class="regbtn login" @click="loginOpen = true">登入</button>
      <span class="brand">數據看板</span>
    </div>
  </header>

  <!-- 登入 / 註冊彈窗。首頁改為公開瀏覽後,這裡是進入會員/VIP 的唯一入口 ——
       原本這兩份表單住在全屏登入牆裡,牆拿掉後整組搬進來,不能只留登入。 -->
  <div v-if="loginOpen" class="overlay overlay-center" @click="closeAuthModal">
    <div class="authcard authmodal" @click.stop>
      <button class="xbtn authx" @click="closeAuthModal">✕</button>
      <img src="/logo.png" class="authlogo" alt="JMCH" />
      <p class="authslogan">Just MONEY Come Here</p>

      <div class="authtabs">
        <button :class="{ on: authTab === 'login' }" @click="authTab = 'login'; regErr = ''; regDone = ''">登入</button>
        <button :class="{ on: authTab === 'register' }" @click="authTab = 'register'; loginErr = ''">註冊</button>
      </div>

      <div v-if="authMsg" class="authnote">{{ authMsg }}</div>

      <template v-if="authTab === 'login'">
        <input :value="loginForm.u" @input="loginForm.u = sanitizeAcct($event.target.value)" class="authin" placeholder="帳號(4–16 英數)" autocomplete="username" @keyup.enter="doLogin" />
        <input :value="loginForm.p" @input="loginForm.p = sanitizePw($event.target.value)" class="authin" type="password" placeholder="密碼" autocomplete="current-password" @keyup.enter="doLogin" />
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
        <!-- 好友推薦碼:刻意不叫「推薦碼」,上面的 Bitunix vipCode 也叫推薦碼,兩者不同 -->
        <div v-if="pendingRef" class="refnote">🎁 已套用好友推薦碼 <b>{{ pendingRef }}</b><span>註冊完成後將自動綁定</span></div>
        <button class="authbtn" @click="doRegister">註冊</button>
        <div v-if="regErr" class="autherr">{{ regErr }}</div>
        <div v-if="regDone" class="authok">{{ regDone }}</div>
        <p class="authhint">註冊後為「審核中」狀態,需管理員審核通過才能登入。</p>
      </template>
    </div>
  </div>

  <!-- admin:某用戶的推廣名單(使用者管理/推廣管理 點名字開啟;帳號不遮罩) -->
  <div v-if="refOfShow" class="overlay" @click="refOfShow = false">
    <div class="refbox" @click.stop>
      <div class="refhead"><h3>👥 {{ refOfUser }} 的推廣名單</h3><button class="xbtn" @click="refOfShow = false">✕</button></div>
      <template v-if="refOfData">
        <div class="refstats">
          <div class="refstat"><div class="refsk">總推薦人數</div><div class="refsv">{{ refOfData.total }}</div></div>
          <div class="refstat"><div class="refsk">合格人數</div><div class="refsv ok">{{ refOfData.qualified }}</div></div>
          <div class="refstat"><div class="refsk">申請獎勵</div><div class="refsv">{{ refOfData.applied }} 次</div></div>
        </div>
        <div class="refcode"><span class="refk">推薦碼</span><b class="refcodev">{{ refOfData.code || '—' }}</b></div>
        <h4 class="refh4">推廣名單</h4>
        <div v-if="!refOfData.records.length" class="refempty">此用戶尚未推薦任何人</div>
        <!-- 唯讀:合格審核改到「推廣管理 → 會員推廣統計」最右邊(那裡看得到推薦人是誰) -->
        <div v-else class="reftable">
          <div v-for="(r, i) in refOfData.records" :key="i" class="refrow">
            <span class="refname">{{ r.username }}</span>
            <span class="tsmall">{{ r.status }}</span>
            <span :class="r.ok ? 'refok' : 'refpend'">{{ r.ok ? '✅ 合格' : '未達成' }}</span>
          </div>
        </div>
        <p class="refhint">合格審核請至「推廣管理 → 會員推廣統計」最右欄切換。</p>
      </template>
      <div v-else class="refempty">載入中…</div>
    </div>
  </div>

  <!-- 我的推廣 modal(點名字開啟) -->
  <div v-if="refShow" class="overlay" @click="refShow = false">
    <div class="refbox" @click.stop>
      <div class="refhead"><h3>🎁 我的推廣</h3><button class="xbtn" @click="refShow = false">✕</button></div>
      <!-- 規則入口:後端未發佈時 text 是空的,整個按鈕就不出現 -->
      <button v-if="refRules.text" class="rulesbtn" @click="refRulesShow = true">
        📜 推廣規則與獎勵制度<span class="rulesgo">查看 ›</span>
      </button>
      <template v-if="refData">
        <!-- 1. 推薦碼 + 2. 網址 -->
        <div class="refcode">
          <span class="refk">我的推薦碼</span>
          <b class="refcodev">{{ refData.code || '—' }}</b>
          <button class="refcopy" @click="copyText(refData.code)">複製</button>
        </div>
        <div class="refurl">
          <span class="refk">推廣連結</span>
          <input class="refurlin" :value="refUrl" readonly @focus="$event.target.select()" />
          <button class="refcopy" @click="copyText(refUrl)">複製</button>
        </div>

        <!-- 4. 統計表(在推薦紀錄上方) -->
        <div class="refstats">
          <div class="refstat"><div class="refsk">總推薦人數</div><div class="refsv">{{ refData.total }}</div></div>
          <div class="refstat"><div class="refsk">合格人數</div><div class="refsv ok">{{ refData.qualified }}</div></div>
          <div class="refstat"><div class="refsk">已申請獎勵</div><div class="refsv">{{ refData.applied }} 次</div></div>
        </div>
        <div class="refapply">
          <!-- 兩種獎勵共用兌換額度,各消耗一次。按鈕開關與失敗原因都由後端算好帶下來,
               前端不重算規則,免得跟伺服器的判斷不一致。 -->
          <div class="refbtns">
            <button class="authbtn" :disabled="!refData.can_usdt || refBusy" @click="applyReward('usdt')">
              申請 {{ refData.usdt_amt }} USDT
            </button>
            <button class="authbtn merch" :disabled="!refData.can_merch || refBusy" @click="applyReward('merch')">
              申請 BITUNIX 周邊
              <span class="refstock" v-if="refData.merch_total > 0">剩 {{ refData.merch_left }}</span>
            </button>
          </div>
          <p v-if="refData.usdt_why" class="refwhy">30 USDT:{{ refData.usdt_why }}</p>
          <p v-if="refData.merch_why" class="refwhy">周邊:{{ refData.merch_why }}</p>
          <p class="refhint">
            每 {{ refData.per_tier }} 位合格受邀戶累積 1 次兌換額度(累計計算,不扣除)。
            額度可換 {{ refData.usdt_amt }} USDT,或在累積滿 {{ refData.merch_at }} 積分後換 1 組 BITUNIX 限量周邊(每人限一組,贈完為止)。
            本月已兌換 {{ refData.month_used }} / {{ refData.month_cap }} 次。合格與否由管理員審核。
          </p>
        </div>

        <!-- 我的申請紀錄 -->
        <template v-if="refData.rewards.length">
          <h4 class="refh4">申請紀錄</h4>
          <div class="reftable">
            <div v-for="w in refData.rewards" :key="w.id" class="refrow">
              <span>{{ w.kind === 'merch' ? '🎁 BITUNIX 周邊' : ('💵 ' + refData.usdt_amt + ' USDT') }}</span>
              <span class="tsmall">第 {{ w.tier }} 次 · 合格 {{ w.qualified }} 位</span>
              <span :class="w.status === 'approved' ? 'refok' : 'refpend'">{{ w.status === 'approved' ? '✅ 已通過' : '⏳ 審核中' }}</span>
            </div>
          </div>
        </template>

        <!-- 3. 我的推薦紀錄(帳號已於伺服器端遮罩) -->
        <h4 class="refh4">我的推薦紀錄</h4>
        <div v-if="!refData.records.length" class="refempty">還沒有人使用你的推薦碼</div>
        <div v-else class="reftable">
          <div v-for="(r, i) in refData.records" :key="i" class="refrow">
            <span class="refname">{{ r.username }}</span>
            <span class="tsmall">{{ fmtClock(r.created) }}</span>
            <span :class="r.ok ? 'refok' : 'refpend'">{{ r.ok ? '✅ 合格' : '未達成' }}</span>
          </div>
        </div>
      </template>
      <div v-else class="refempty">載入中…</div>
    </div>
  </div>

  <!-- 推廣規則 modal(疊在我的推廣之上;關掉它會回到推廣頁,不是整個關掉) -->
  <div v-if="refRulesShow" class="overlay rulesover" @click="refRulesShow = false">
    <div class="refbox" @click.stop>
      <div class="refhead">
        <h3>📜 {{ refRules.title || '推廣規則與獎勵制度' }}</h3>
        <button class="xbtn" @click="refRulesShow = false">✕</button>
      </div>
      <div class="rulesbody">
        <p v-for="(para, i) in refRuleParas" :key="i" class="rulespara">{{ para }}</p>
      </div>
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

  <!-- 登入公告彈窗 (admin-set announcement) -->
  <div v-if="noticeShow && notice" class="overlay" @click="closeNotice">
    <div class="noticebox" @click.stop>
      <h3 class="nb-title">📢 {{ notice.title || '公告' }}</h3>
      <div class="nb-text">{{ notice.text }}</div>
      <label class="nb-dont"><input type="checkbox" v-model="noticeDont" /> 不再顯示此則</label>
      <button class="authbtn" @click="closeNotice">我知道了</button>
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
    <!-- 戰場: BTC 多空交戰(自給自足元件,自己連 WS、自己起停動畫) -->
    <BattleField />

    <!-- 整點大盤分析 (live message, above the recs) -->
    <div v-if="marketAI && marketAI.text" class="mai-live">
      <div class="mai-live-top">
        <span class="mai-live-title"><span class="mai-dot"></span>整點大盤分析</span>
        <span class="mai-live-time" v-if="marketAI.updated_at">{{ fundClock(new Date(marketAI.updated_at).getTime()) }} 更新 · 每小時更新</span>
      </div>
      <div class="mai-live-summary">{{ marketAI.summary }}</div>
      <div class="mai-live-body">{{ maiBody }}</div>
    </div>

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
      <!-- 每顆分頁鈕各自依「標籤權限」設定顯示;整列都看不到時連列標題一起收掉。
           分組只是視覺標籤 —— 後台把某頁調給別的身分組時,是按鈕自己出現/消失。 -->
      <div class="navrow" v-if="anyTab(['ranking', 'list', 'events', 'flow', 'upbit', 'news', 'funding', 'unlock', 'sectors', 'robinhood', 'articles'])">
        <span class="navgroup">公開</span>
        <div class="navbtns">
          <button v-if="canTab('ranking')" :class="{ active: mainTab === 'ranking' }" @click="mainTab = 'ranking'">綜合排行</button>
          <button v-if="canTab('list')" :class="{ active: mainTab === 'list' }" @click="mainTab = 'list'">幣種一覽</button>
          <button v-if="canTab('events')" :class="{ active: mainTab === 'events' }" @click="mainTab = 'events'">
            財經事件<em v-if="eventList.filter((e) => !e.released).length" class="navbadge">{{ eventList.filter((e) => !e.released).length }}</em>
          </button>
          <button v-if="canTab('flow')" :class="{ active: mainTab === 'flow' }" @click="mainTab = 'flow'">清算</button>
          <button v-if="canTab('upbit')" :class="{ active: mainTab === 'upbit' }" @click="mainTab = 'upbit'">
            Upbit 公告<em v-if="upbitNotices.length" class="navbadge">{{ upbitNotices.length }}</em>
          </button>
          <button v-if="canTab('news')" :class="{ active: mainTab === 'news' }" @click="mainTab = 'news'; loadNews()">
            市場快訊<em v-if="news.length" class="navbadge">{{ news.length }}</em>
          </button>
          <button v-if="canTab('funding')" :class="{ active: mainTab === 'funding' }" @click="mainTab = 'funding'">資金費率</button>
          <button v-if="canTab('unlock')" :class="{ active: mainTab === 'unlock' }" @click="mainTab = 'unlock'">代幣解鎖</button>
          <button v-if="canTab('sectors')" :class="{ active: mainTab === 'sectors' }" @click="mainTab = 'sectors'">板塊強弱</button>
          <button v-if="canTab('robinhood')" :class="{ active: mainTab === 'robinhood' }" @click="mainTab = 'robinhood'; loadRobinhood()">
            Robinhood<em v-if="robinhoodNew" class="navbadge">{{ robinhoodNew }}</em>
          </button>
          <button v-if="canTab('articles')" :class="{ active: mainTab === 'articles' }" @click="mainTab = 'articles'; articleView = null">
            文章專欄<em v-if="articles.length" class="navbadge">{{ articles.length }}</em>
          </button>
        </div>
      </div>
      <div class="navrow" v-if="anyTab(['oi', 'signals', 'scorelog', 'radar'])">
        <span class="navgroup">會員</span>
        <div class="navbtns">
          <button v-if="canTab('oi')" :class="{ active: mainTab === 'oi' }" @click="mainTab = 'oi'">OI 儀表板</button>
          <button v-if="canTab('signals')" :class="{ active: mainTab === 'signals' }" @click="mainTab = 'signals'">
            數據訊號<em v-if="signals.length" class="navbadge">{{ signals.length }}</em>
          </button>
          <button v-if="canTab('scorelog')" :class="{ active: mainTab === 'scorelog' }" @click="mainTab = 'scorelog'">
            訊號紀錄<em v-if="scoreLog.length" class="navbadge">{{ scoreLog.length }}</em>
          </button>
          <button v-if="canTab('radar')" :class="{ active: mainTab === 'radar' }" @click="mainTab = 'radar'">爆發雷達</button>
        </div>
      </div>
      <div class="navrow" v-if="anyTab(['paper', 'gamble', 'emaonly', 'conv', 'sr'])">
        <span class="navgroup">VIP</span>
        <div class="navbtns">
          <button v-if="canTab('paper')" :class="{ active: mainTab === 'paper' }" @click="mainTab = 'paper'">
            星軌<em v-if="paper && paper.open.length" class="navbadge">{{ paper.open.length }}</em>
          </button>
          <button v-if="canTab('gamble')" :class="{ active: mainTab === 'gamble' }" @click="mainTab = 'gamble'">
            超新星<em v-if="gamble && gamble.open.length" class="navbadge">{{ gamble.open.length }}</em>
          </button>
          <button v-if="canTab('emaonly')" :class="{ active: mainTab === 'emaonly' }" @click="mainTab = 'emaonly'">
            銀河<em v-if="emaOnly && emaOnly.open.length" class="navbadge">{{ emaOnly.open.length }}</em>
          </button>
          <button v-if="canTab('conv')" :class="{ active: mainTab === 'conv' }" @click="mainTab = 'conv'; loadConv()">
            冥王星<em v-if="conv && conv.open.length" class="navbadge">{{ conv.open.length }}</em>
          </button>
          <button v-if="canTab('sr')" :class="{ active: mainTab === 'sr' }" @click="mainTab = 'sr'; loadSR()">支撐壓力</button>
        </div>
      </div>
      <!-- 後台/推廣管理永遠鎖在管理員(後端也拒絕降級);策略觀察書則可調給 VIP。 -->
      <div class="navrow" v-if="anyTab(['admin', 'referral', 'bollfade', 'meanrev', 'bgv2', 'bollema'])">
        <span class="navgroup">管理</span>
        <div class="navbtns">
          <button v-if="canTab('admin')" :class="{ active: mainTab === 'admin' }" @click="mainTab = 'admin'; loadUsers(); loadNotice()">
            後台<em v-if="users.length" class="navbadge">{{ users.length }}</em>
          </button>
          <button v-if="canTab('referral')" :class="{ active: mainTab === 'referral' }" @click="mainTab = 'referral'; loadRefAdmin()">
            推廣管理<em v-if="refAdmin && refAdmin.pending" class="navbadge">{{ refAdmin.pending }}</em>
          </button>
          <button v-if="canTab('bollfade')" :class="{ active: mainTab === 'bollfade' }" @click="mainTab = 'bollfade'; loadBollfade()">
            布林重回<em v-if="bollfade && bollfade.open.length" class="navbadge">{{ bollfade.open.length }}</em>
          </button>
          <button v-if="canTab('meanrev')" :class="{ active: mainTab === 'meanrev' }" @click="mainTab = 'meanrev'; loadMeanrev()">
            乖離回歸<em v-if="meanrev && meanrev.open.length" class="navbadge">{{ meanrev.open.length }}</em>
          </button>
          <button v-if="canTab('bgv2')" :class="{ active: mainTab === 'bgv2' }" @click="mainTab = 'bgv2'; loadBgv2()">
            布乖v2<em v-if="bgv2 && bgv2.open.length" class="navbadge">{{ bgv2.open.length }}</em>
          </button>
          <button v-if="canTab('bollema')" :class="{ active: mainTab === 'bollema' }" @click="mainTab = 'bollema'; loadBollema()">
            布林EMA<em v-if="bollema && bollema.open.length" class="navbadge">{{ bollema.open.length }}</em>
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
    <section v-else-if="mainTab === 'sr' && canTab('sr')">
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

    <!-- 冥王星 (動態ATR 4H 均線收斂) · VIP -->
    <section v-else-if="mainTab === 'conv' && canTab('conv')">
      <div class="mk-head">
        <h2>冥王星<span class="help" tabindex="0">?<span class="help-pop">‼️此訊號為保守策略‼️<br>波動較低，<br>但有機會在行情出來後延續下去。<br><b>分批止盈</b>:TP1/TP2 位在進場→最終止盈的 40%/70%,分三批出場,TP1 後止損移保本、TP2 後移 TP1。<br>下單前務必確認倉位使用總本金「2%」<br>槓桿不超過「25-40x」<br>🌟若遇到盤整行情，可往其他策略觀察更好的交易機會。<br><br>「此為幣種策略分享，不構成任何投資建議。」</span></span></h2>
        <span class="mk-actions"><span class="mk-count" v-if="conv">進行中 {{ conv.open.length }} · 已結束 {{ conv.stats.closed }}</span><button v-if="can('admin')" class="clearbtn" @click="clearStrat('conv', loadConv, true)">清已結束</button><button v-if="can('admin')" class="clearbtn" @click="clearStrat('conv', loadConv, false)">全部</button></span>
      </div>
      <StrategyBook
        :state="conv"
        :risky="stratRisky('conv')"
        :tags="stratTagsOf('conv')"
        :stats-order="['type', 'win', 'avg', 'total']"
        :can-exit="can('admin')"
        empty-text="尚無訊號——需等 4H 收盤出現橫盤收斂 + 盈虧比達標(首次啟動需抓取歷史 K 線)。"
        @coin="openDetail"
        @exit="(id) => manualExitStrat('conv', id, loadConv)"
      />
    </section>

    <!-- 均值回歸策略(逆勢超買空 / 布林重回 / 乖離回歸)· 分批止盈 · admin only -->
    <section v-else-if="micro && canTab(mainTab)">
      <div class="mk-head">
        <h2>{{ micro.title }}<span class="help" tabindex="0">?<span class="help-pop" v-html="micro.help"></span></span></h2>
        <span class="mk-actions"><span class="mk-count" v-if="microState">進行中 {{ microState.open.length }} · 已結束 {{ microState.stats.closed }}</span><button class="clearbtn" @click="clearStrat(mainTab, micro.load, true)">清已結束</button><button class="clearbtn" @click="clearStrat(mainTab, micro.load, false)">全部</button></span>
      </div>

      <StrategyBook
        :state="microState"
        :risky="stratRisky(curStrat)"
        :tags="stratTagsOf(curStrat)"
        :stats-order="['total', 'win', 'avg', 'type']"
        :can-exit="true"
        empty-text="尚無訊號——需等收盤觸發進場訊號(首次啟動需抓取歷史 K 線)。"
        @coin="openDetail"
        @exit="(id) => manualExitStrat(mainTab, id, micro.load)"
      />
    </section>

    <!-- 後台管理 (admin only) -->
    <!-- 推廣管理 (admin) -->
    <section v-else-if="mainTab === 'referral' && can('admin')">
      <!-- 0. 周邊庫存:放最上面,因為庫存歸零會直接關掉所有人的周邊按鈕 -->
      <section class="card adminbox">
        <div class="mk-head"><h3 class="psub">📦 周邊庫存管理</h3></div>
        <div class="merchbox">
          <div class="merchstats">
            <div class="refstat"><div class="refsk">庫存總量</div><div class="refsv">{{ merchStock.total }}</div></div>
            <div class="refstat"><div class="refsk">已申請</div><div class="refsv">{{ merchStock.used }}</div></div>
            <div class="refstat"><div class="refsk">剩餘</div><div class="refsv" :class="merchStock.left > 0 ? 'ok' : 'zero'">{{ merchStock.left }}</div></div>
          </div>
          <div class="merchset">
            <label class="refk">設定總量</label>
            <input class="merchin" type="number" min="0" step="1" v-model.number="merchInput" />
            <button class="okbtn" :disabled="merchBusy" @click="saveMerchStock">儲存</button>
          </div>
        </div>
        <p class="refhint">
          剩餘 = 總量 − 已申請(送出申請就佔用庫存,不等審核通過,否則會超發)。
          剩餘歸零時所有人的「申請周邊」按鈕會自動關閉。總量設成 0 等於停止發放。
        </p>
      </section>

      <!-- 2. 審核獎勵發放(有待審核就置頂,3. 申請時管理員已收到推播) -->
      <section class="card adminbox">
        <div class="mk-head">
          <h3 class="psub">🎁 審核獎勵發放<em v-if="refAdmin && refAdmin.pending" class="navbadge">{{ refAdmin.pending }}</em></h3>
          <span class="mk-count" v-if="refAdmin">共 {{ refAdmin.rewards.length }} 筆申請</span>
        </div>
        <p class="refhint">實際獎勵發放為人工作業,「通過」僅記錄管理員已核可。</p>
        <div v-if="!refAdmin || !refAdmin.rewards.length" class="refempty">目前沒有獎勵申請</div>
        <table v-else class="grid reftbl">
          <!-- 狀態與操作合併:待審核時「按鈕就是狀態」,通過後原地換成已通過+時間 -->
          <thead><tr><th>帳號</th><th>獎勵品項</th><th class="r">申請時合格數</th><th class="r">申請時間</th><th class="r">審核</th></tr></thead>
          <tbody>
            <tr v-for="w in refAdmin.rewards" :key="w.id" :class="{ rowpend: w.status !== 'approved' }">
              <td class="coin"><button class="namebtn" @click="openRefOf(w.username)">{{ w.username }}</button></td>
              <td>
                <span class="tierchip" :class="w.kind === 'merch' ? 'merch' : ''">
                  {{ w.kind === 'merch' ? '🎁 BITUNIX 周邊' : '💵 30 USDT' }}
                </span>
                <small class="tsmall"> 第 {{ w.tier }} 次</small>
              </td>
              <td class="r refnum">{{ w.qualified }}</td>
              <td class="r tsmall">{{ fmtClock(w.applied) }}</td>
              <td class="r">
                <button v-if="w.status !== 'approved'" class="okbtn" @click="approveReward(w.id)">通過</button>
                <span v-else class="doneby">✅ 已通過<small>{{ fmtClock(w.reviewed) }}</small></span>
              </td>
            </tr>
          </tbody>
        </table>
      </section>

      <!-- 1. 每個會員的推廣統計 -->
      <section class="card adminbox">
        <div class="mk-head">
          <h3 class="psub">👥 會員推廣統計</h3>
          <span class="mk-count" v-if="refAdmin">{{ refAdmin.rows.length }} 位</span>
        </div>
        <p class="refhint">
          左半是「他推了誰」的統計,右半是「他自己」被誰推薦、是否完成指定任務。
          <b>待審核的會自動排最上面。</b>點帳號可看該用戶推薦了哪些人。
        </p>
        <table class="grid reftbl">
          <thead><tr>
            <th>帳號</th><th>推薦碼</th><th>角色</th>
            <th class="r">總推薦人數</th><th class="r">合格人數</th><th class="r">申請獎勵次數</th>
            <th>推薦人</th><th class="r">合格審核</th>
          </tr></thead>
          <tbody>
            <tr v-for="r in (refAdmin ? refAdmin.rows : [])" :key="r.username" :class="{ rowpend: r.ref_by && !r.ok }">
              <td class="coin"><button class="namebtn" @click="openRefOf(r.username)">{{ r.username }}</button></td>
              <td class="refcodecell">{{ r.code || '—' }}</td>
              <td><span class="rolechip" :class="r.role">{{ r.role }}</span></td>
              <td class="r refnum" :class="{ zero: !r.total }">{{ r.total }}</td>
              <td class="r refnum" :class="r.qualified ? 'long' : 'zero'">{{ r.qualified }}</td>
              <td class="r refnum" :class="{ zero: !r.applied }">{{ r.applied }}</td>
              <!-- 推薦人:切合格前必須看得到這一票加給誰 -->
              <td class="tsmall">
                <button v-if="r.ref_by" class="namebtn" @click="openRefOf(r.ref_by)">{{ r.ref_by }}</button>
                <span v-else class="refnone">自然註冊</span>
              </td>
              <!-- 合格審核:只有「被推薦的人」才有意義 -->
              <td class="r">
                <div v-if="r.ref_by" class="okcell">
                  <label class="switch" :title="r.ok ? '點擊改為未達成' : '點擊標記合格'">
                    <input type="checkbox" :checked="r.ok" @change="setRefOK(r.username, !r.ok)" />
                    <span class="sw-track"></span>
                  </label>
                  <span class="reflabel" :class="r.ok ? 'refok' : 'refpend'">{{ r.ok ? '合格' : '未達成' }}</span>
                </div>
                <span v-else class="refnone">—</span>
              </td>
            </tr>
          </tbody>
        </table>
      </section>
    </section>

    <section v-else-if="mainTab === 'admin' && can('admin')">
      <div class="mk-head"><h2>後台</h2><span class="mk-count">{{ users.length }} 位使用者</span></div>
      <p v-if="adminMsg" class="admin-msg">{{ adminMsg }}</p>

      <!-- 後台各功能管理標籤 -->
      <nav class="adminnav">
        <button v-for="t in ADMIN_TABS" :key="t[0]" :class="{ on: adminTab === t[0] }" @click="adminTab = t[0]">{{ t[1] }}</button>
      </nav>

      <UserManagement v-if="adminTab === 'users'" :users="users" :current-user="username"
        @reload="loadUsers" @msg="(m) => (adminMsg = m)" @proof="(p) => (proofView = p)" @refof="openRefOf" />

      <!-- 標籤權限 / 策略設定:各自獨立元件,自己載資料 -->
      <TabPermissions v-else-if="adminTab === 'perms'" @changed="loadTabPerms" @msg="(m) => (adminMsg = m)" />
      <StrategySettings v-else-if="adminTab === 'strat'" @changed="loadStratMeta" @msg="(m) => (adminMsg = m)" />
      <SiteSettings v-else-if="adminTab === 'site'" :config="config" :social-links="socialLinks"
        @saved="loadConfig" @msg="(m) => (adminMsg = m)" @toast="(t, k) => showToast(t, k)" />

      <LoginNotice v-else-if="adminTab === 'notice'" :notice="notice"
        @saved="loadNotice" @msg="(m) => (adminMsg = m)" @toast="(t, k) => showToast(t, k)" />
      <!-- 存檔後重新載入會員視角那份,後台改完立刻反映到「我的推廣」的入口 -->
      <ReferralRules v-else-if="adminTab === 'refrules'"
        @msg="(m) => { adminMsg = m; loadRefRules() }" @toast="(t, k) => showToast(t, k)" />
      <PushBroadcast v-else-if="adminTab === 'push'" :articles="articles"
        @toast="(t, k) => showToast(t, k)" />

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

    <section v-else-if="mainTab === 'paper' || mainTab === 'gamble' || mainTab === 'emaonly'">
      <div class="mk-head">
        <h2>{{ mainTab === 'gamble' ? '超新星' : mainTab === 'emaonly' ? '銀河' : '星軌' }}<span class="help" tabindex="0">?<span class="help-pop"><template v-if="mainTab === 'gamble'">‼️此訊號為動能策略‼️<br>波動較大風險較高<br>止損概率較大，但止盈較遠。<br><b>分批止盈</b>:TP1/TP2 位在進場→最終止盈的 40%/70%。TP1 平 40%→止損移保本、TP2 平 30%→止損移 TP1、TP3(最終)平剩餘。<br>下單前務必確認倉位使用總本金「1%」<br>槓桿不超過「25%」<br>🌟若遇到洗盤行情風險更高，可往其他策略觀察更好的交易機會。<br><br>「此為幣種策略分享，不構成任何投資建議。」</template><template v-else-if="mainTab === 'emaonly'">‼️此訊號為順勢策略‼️<br>波動較低，<br>但有機會在行情出來後延續下去。<br><b>分批止盈</b>:TP1/TP2 位在進場→最終止盈的 40%/70%,分三批出場,TP1 後止損移保本、TP2 後移 TP1。<br>下單前務必確認倉位使用總本金「2%」<br>槓桿不超過「25-40%」<br>🌟若遇到盤整行情，可往其他策略觀察更好的交易機會。<br><br>「此為幣種策略分享，不構成任何投資建議。」</template><template v-else>‼️此訊號為動能策略‼️<br>波動較大風險較高<br>止損概率較大，但止盈較遠。<br>有機會在行情出來時延續下去。<br><b>分批止盈</b>:TP1/TP2 位在進場→最終止盈的 40%/70%,分三批出場,TP1 後止損移保本、TP2 後移 TP1。<br>下單前務必確認倉位使用總本金「1%」<br>槓桿不超過「25-30%」<br>🌟若遇到洗盤行情風險更高，可往其他策略觀察更好的交易機會。<br><br>「此為幣種策略分享，不構成任何投資建議。」</template></span></span></h2>
        <span class="mk-count" v-if="book">每 60 秒監控 · 自動止盈止損</span>
        <button v-if="can('admin')" class="csvbtn" @click="exportCSV">⬇ 匯出 CSV</button>
        <button v-if="can('admin')" class="clearbtn" @click="clearStrat(curPaperBook, loadPaper, true)">清已結束</button>
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
      <p v-if="stratRisky(curStrat)" class="riskwarn">⚠️ 目前盤面使用此策略風險較大,請謹慎操作</p>
      <div v-if="bookF" class="pstats">
        <div class="pstat"><div class="stat-k">策略類型</div><div class="stat-v stat-tags">{{ stratTagsOf(curStrat).join('・') || '—' }}</div></div>
        <div class="pstat"><div class="stat-k">勝率</div><div class="stat-v" :class="bookF.stats.win_rate >= 50 ? 'long' : 'short'">{{ bookF.stats.win_rate }}%</div></div>
        <div class="pstat"><div class="stat-k">平均損益</div><div class="stat-v" :class="bookF.stats.avg_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(bookF.stats.avg_pnl) }}</div></div>
        <div class="pstat"><div class="stat-k">累計損益</div><div class="stat-v" :class="bookF.stats.total_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(bookF.stats.total_pnl) }}</div></div>
      </div>
      <div v-if="bookF && bookF.stats.multi_tp && bookF.stats.closed" class="tpfunnel">
        <div class="tpf-title">止盈達成漏斗 · 共 {{ bookF.stats.closed }} 筆已結束</div>
        <div class="tpf-row"><span class="tpf-lbl">TP1 達成</span><span class="tpf-bar"><i :style="{ width: pctOf(bookF.stats.tp1, bookF.stats.closed) + '%' }"></i></span><span class="tpf-val">{{ bookF.stats.tp1 }} 筆 · <b>{{ pctOf(bookF.stats.tp1, bookF.stats.closed) }}%</b></span></div>
        <div class="tpf-row"><span class="tpf-lbl">TP2 達成</span><span class="tpf-bar"><i :style="{ width: pctOf(bookF.stats.tp2, bookF.stats.closed) + '%' }"></i></span><span class="tpf-val">{{ bookF.stats.tp2 }} 筆 · <b>{{ pctOf(bookF.stats.tp2, bookF.stats.closed) }}%</b></span></div>
        <div class="tpf-row"><span class="tpf-lbl">TP3 達成</span><span class="tpf-bar"><i :style="{ width: pctOf(bookF.stats.tp3, bookF.stats.closed) + '%' }"></i></span><span class="tpf-val">{{ bookF.stats.tp3 }} 筆 · <b>{{ pctOf(bookF.stats.tp3, bookF.stats.closed) }}%</b></span></div>
      </div>

      <h3 class="psub" v-if="bookF">進行中 ({{ bookF.open.length }})</h3>
      <table v-if="bookF && bookF.open.length" class="grid">
        <thead><tr><th>幣種</th><th>方向</th><th class="r">進場</th><th class="r">現價</th><th class="r">損益%</th><th v-if="mainTab !== 'emaonly'" title="動能是否還在(雷達分數+CVD);⚠️贏單常因已漲一段而顯示轉弱,僅供參考">動能</th><th v-if="bookF && bookF.stats.multi_tp">進度</th><th class="r" title="當前資金費率">費率</th><th class="r">止盈</th><th class="r">止損</th><th class="r">進場時間</th><th class="r">持倉</th><th v-if="can('admin')" class="r">操作</th></tr></thead>
        <tbody>
          <tr v-for="t in bookF.open" :key="t.coin + t.open_time" class="clickable" @click="openDetail(t.coin)">
            <td class="coin">{{ t.coin }}</td>
            <td><span class="dir" :class="t.dir === 'long' ? 'long' : 'short'">{{ t.dir === 'long' ? '做多' : '做空' }}</span></td>
            <td class="r">{{ fmtPrice(t.entry) }}</td>
            <td class="r">{{ fmtPrice(t.cur) }}</td>
            <td class="r" :class="t.pnl_pct >= 0 ? 'long' : 'short'"><b>{{ fmtPct(t.pnl_pct) }}</b></td>
            <td v-if="mainTab !== 'emaonly'"><span class="momlight" :class="momClass(t.momentum)">{{ momText(t.momentum) }}</span></td>
            <td v-if="bookF && bookF.stats.multi_tp" class="tsmall" :title="t.tp1 ? ('TP1 ' + fmtPrice(t.tp1) + ' · TP2 ' + fmtPrice(t.tp2) + ' · TP3 ' + fmtPrice(t.tp)) : ''">
              <template v-if="t.tp1"><span class="tppill" :class="{ hit: t.legs >= 1 }">TP1 {{ fmtPrice(t.tp1) }}</span><span class="tppill" :class="{ hit: t.legs >= 2 }">TP2 {{ fmtPrice(t.tp2) }}</span><span class="tsmall"> 剩{{ Math.round((1 - (t.filled || 0)) * 100) }}%</span></template>
              <span v-else class="tsmall">單一</span>
            </td>
            <td class="r tsmall">{{ fmtFund(t.cur_funding) }}</td>
            <td class="r long">{{ fmtPrice(t.tp) }} <small>({{ fmtPct(pnlAt(t, t.tp)) }})</small></td>
            <td class="r short">{{ fmtPrice(t.sl) }}<small v-if="t.legs >= 2" class="vtag"> 鎖利</small><small v-else-if="t.legs >= 1" class="vtag"> 保本</small></td>
            <td class="r tsmall">{{ fmtClock(t.open_time) }}</td>
            <td class="r">{{ fmtDur(holdMs(t)) }}</td>
            <td v-if="can('admin')" class="r"><button class="exitbtn" @click.stop="mainTab === 'emaonly' ? manualExit(t) : manualExitStrat(curPaperBook, t.id, loadPaper)">手動出場</button></td>
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
            <td><span class="otag" :class="outcomeCls(t.outcome, t.pnl_pct)">{{ convOutcome(t.outcome, t.pnl_pct) }}</span></td>
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
    <EventsBoard v-else-if="mainTab === 'events'" :event-list="eventList" />

    <!-- 清算 (liquidation feed, OKX) -->
    <LiquidationBoard v-else-if="mainTab === 'flow'" @coin="openDetail" />

    <!-- Upbit 公告 (韓文原文自動翻譯為繁體中文) -->
    <UpbitBoard v-else-if="mainTab === 'upbit'" :upbit-notices="upbitNotices" />

    <!-- 市場快訊 (全球新聞事件) -->
    <NewsBoard v-else-if="mainTab === 'news'" :news="news" />

    <!-- 資金費率 (OKX) -->
    <FundingBoard v-else-if="mainTab === 'funding'" @coin="openDetail" />

    <!-- 代幣解鎖 (DefiLlama) -->
    <UnlockBoard v-else-if="mainTab === 'unlock'" />

    <!-- Robinhood 上架 (currency-pair diff) -->
    <RobinhoodBoard v-else-if="mainTab === 'robinhood'" :robinhood="robinhood" />

    <!-- 板塊強弱/輪動 (hourly) -->
    <SectorBoard v-else-if="mainTab === 'sectors'" ref="sectorBoard" />

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
/* 推薦系統 */
.namebtn { background: none; border: none; color: #c8cdd6; font-size: 12px; font-weight: 600; cursor: pointer; padding: 0; text-decoration: underline dotted rgba(216,173,72,.6); text-underline-offset: 3px; }
.namebtn:hover { color: #d8ad48; }
.refnote { background: rgba(216,173,72,.1); border: 1px solid #3a3320; border-radius: 8px; padding: 8px 10px; font-size: 12px; color: #d8ad48; display: flex; flex-direction: column; gap: 2px; }
.refnote span { color: #8b909a; font-size: 11px; }
.refbox { background: #14161c; border: 1px solid #23262f; border-radius: 14px; padding: 16px; width: min(560px, 94vw); max-height: 86vh; overflow-y: auto; }
.refhead { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; }
.refhead h3 { margin: 0; font-size: 16px; color: #d8ad48; }
.xbtn { background: none; border: none; color: #8b909a; font-size: 18px; cursor: pointer; line-height: 1; }
.refcode, .refurl { display: flex; align-items: center; gap: 8px; background: #1b1e26; border: 1px solid #2b2f3a; border-radius: 9px; padding: 9px 11px; margin-bottom: 8px; }
.refk { font-size: 11px; color: #8b909a; white-space: nowrap; }
.refcodev { font-size: 18px; letter-spacing: 2px; color: #d8ad48; font-family: ui-monospace, monospace; flex: 1; }
.refurlin { flex: 1; background: #0f1116; border: 1px solid #2b2f3a; border-radius: 6px; color: #b9bdc4; font-size: 11px; padding: 5px 7px; min-width: 0; }
.refcopy { background: #2a2f3a; border: 1px solid #3a3f4a; color: #d8ad48; border-radius: 6px; padding: 4px 10px; font-size: 12px; cursor: pointer; white-space: nowrap; }
.refstats { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; margin: 12px 0 10px; }
.refstat { background: #1b1e26; border: 1px solid #2b2f3a; border-radius: 9px; padding: 10px; text-align: center; }
.refsk { font-size: 11px; color: #8b909a; margin-bottom: 4px; }
.refsv { font-size: 20px; font-weight: 700; color: #e8e9ec; }
.refsv.ok { color: #2ec26b; }
.refapply .authbtn { width: 100%; }
.refapply .authbtn:disabled { opacity: .45; cursor: not-allowed; }
/* 兩顆兌換按鈕:窄螢幕自動疊成上下,不會擠成兩個沒人按得到的小方塊 */
.refbtns { display: flex; gap: 8px; flex-wrap: wrap; }
.refbtns .authbtn { flex: 1 1 160px; width: auto; }
.authbtn.merch { background: #2a2410; color: #f4d774; border-color: #4a3f18; }
.authbtn.merch:hover:not(:disabled) { background: #3a3216; }
.refstock { font-size: 11px; opacity: .8; margin-left: 6px; }
.refwhy { font-size: 11px; color: #f4d774; margin: 6px 0 0; }
.refhint { font-size: 11px; color: #8b909a; margin: 6px 0 0; line-height: 1.6; }
.refh4 { font-size: 13px; color: #c8cdd6; margin: 14px 0 6px; }
.reftable { display: flex; flex-direction: column; gap: 4px; }
.refrow { display: grid; grid-template-columns: 1fr auto auto; gap: 10px; align-items: center; background: #1b1e26; border-radius: 7px; padding: 7px 10px; font-size: 12px; color: #e8e9ec; }
.refname { font-family: ui-monospace, monospace; }
.refok { color: #2ec26b; font-weight: 600; }
.refpend { color: #8b909a; }
/* 推廣規則入口:放在「我的推廣」標題下方,要一眼看到所以用金色高亮 */
.rulesbtn {
  display: flex; align-items: center; justify-content: space-between; gap: 8px;
  width: 100%; margin: 0 0 14px; padding: 11px 14px; cursor: pointer;
  background: linear-gradient(90deg, #2a2410, #1e1c14);
  border: 1px solid #4a3f18; border-radius: 10px;
  color: #f4d774; font-size: 13px; font-weight: 600;
}
.rulesbtn:hover { background: linear-gradient(90deg, #3a3216, #2a2618); border-color: #6a5a20; }
.rulesgo { font-size: 12px; opacity: .75; font-weight: 400; }
/* 疊在我的推廣 modal 之上 —— 同樣是 .overlay,不加這行會被蓋住 */
.rulesover { z-index: 60; }
.rulesbody { padding: 2px 0 8px; }
/* pre-line:段落內的單一換行保留(規則常是條列),段落間距靠 <p> */
.rulespara { margin: 0 0 12px; font-size: 13px; line-height: 1.85; color: #c8ccd4; white-space: pre-line; }
.rulespara:last-child { margin-bottom: 0; }
/* 周邊庫存管理 */
.merchbox { display: flex; gap: 16px; flex-wrap: wrap; align-items: flex-end; }
.merchstats { display: flex; gap: 10px; flex: 1 1 260px; }
.merchset { display: flex; gap: 8px; align-items: center; }
.merchin { width: 90px; padding: 7px 10px; border-radius: 8px; border: 1px solid #2a2f3a; background: #14171d; color: #e8eaee; }
.refsv.zero { color: #ff5c5c; }
.tierchip.merch { background: #2a2410; color: #f4d774; }
/* 推廣管理表:欄寬固定,標題不換行(數字欄本來會被中文標題擠爛) */
.reftbl th { white-space: nowrap; font-size: 12px; }
.reftbl td { vertical-align: middle; }
.reftbl .refcodecell { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; letter-spacing: 1px; color: #d8ad48; white-space: nowrap; }
.reftbl .refnum { font-size: 15px; font-weight: 700; }
.reftbl .refnum.zero { color: #5c616b; font-weight: 500; } /* 0 不用搶眼 */
.reftbl tr:hover td { background: #14161b; }
.rolechip { display: inline-block; padding: 1px 7px; border-radius: 5px; font-size: 11px; font-weight: 600; background: #1f2229; color: #b8bcc4; }
.rolechip.admin { background: #2a2410; color: #f4d774; }
.rolechip.vip { background: #0e2a44; color: #6db5ff; }
.reftbl .tierchip { display: inline-block; padding: 1px 7px; border-radius: 5px; font-size: 11px; font-weight: 600; background: rgba(216,173,72,.14); color: #d8ad48; white-space: nowrap; }
@media (max-width: 700px) { .reftbl { font-size: 12px; } .reftbl th, .reftbl td { padding: 7px 5px; } .reftbl .refnum { font-size: 14px; } }
/* 每列各自是獨立 grid,欄寬用 auto 會讓各列對不齊 → 一律寫死 */
.reflabel { font-size: 11px; font-weight: 600; text-align: left; }
/* 同理:我的推廣的推薦紀錄也是每列獨立 grid */
.refrow { grid-template-columns: 1fr 108px 56px !important; }
/* 合格審核欄:開關 + 標籤靠右成組(標籤寬度固定,否則各列開關會左右跳) */
.okcell { display: inline-flex; align-items: center; gap: 7px; }
.okcell .reflabel { width: 38px; text-align: left; }
.refnone { color: #5c616b; font-size: 12px; }
.doneby { display: inline-flex; flex-direction: column; align-items: flex-end; gap: 1px; color: #2ec26b; font-size: 12px; font-weight: 600; }
.doneby small { color: #8b909a; font-weight: 400; font-size: 10.5px; }
.reftbl tr.rowpend td { background: rgba(216,173,72,.05); } /* 待審核的列淡淡標一下 */
.refempty { text-align: center; color: #8b909a; font-size: 12px; padding: 14px; }
@media (max-width: 560px) { .refstats { gap: 6px; } .refsv { font-size: 17px; } .refcodev { font-size: 15px; } }
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
.betag { display: inline-block; margin-left: 6px; font-size: 11px; padding: 1px 6px; border-radius: 5px; font-weight: 600; background: rgba(74,163,255,0.16); color: #6db5ff; white-space: nowrap; }
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
.rh-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(120px, 1fr)); gap: 10px; }
.rh-card { background: #14161c; border: 1px solid #23262d; border-radius: 10px; padding: 10px 12px; }
.rh-card.isnew { border-color: #d8ad48; background: #1a1710; }
.rh-code { font-weight: 700; color: #e8e9ec; font-size: 15px; display: flex; align-items: center; gap: 6px; }
.rh-new, .new-dot { background: #d8ad48; color: #14161c; font-size: 10px; font-weight: 700; padding: 1px 5px; border-radius: 5px; }
.rh-name { font-size: 12px; color: #9aa0a8; margin-top: 3px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rh-sym { font-size: 11px; color: #6b7078; margin-top: 2px; }
.sec-summary { background: #14161c; border: 1px solid #23262d; border-radius: 10px; padding: 9px 13px; margin-bottom: 10px; font-size: 13px; color: #c9cdd4; line-height: 1.6; }
.sec-detail td { background: #101218; padding: 8px 12px 10px; }
.sec-detail-lbl { font-size: 11px; color: #8b909a; margin-right: 8px; }
.sec-chip { display: inline-block; font-size: 11.5px; padding: 2px 7px; margin: 2px 4px 2px 0; border-radius: 5px; }
.sec-chip.up { background: #133027; color: #5fd39a; }
.sec-chip.down { background: #2f1a1a; color: #e0857f; }
.strat-toggles { display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: 8px 14px; margin: 4px 0 6px; }
.stratcfg { border: 1px solid #2b2e36; border-radius: 8px; padding: 9px 10px; display: flex; flex-direction: column; gap: 7px; }
.stratcfg-line { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; }
.stratcfg-k { font-size: 11px; color: #8b909a; min-width: 56px; }
.stratcfg-num { width: 66px; background: #1b1d23; border: 1px solid #363943; border-radius: 6px; color: #e6e8ec; padding: 3px 6px; font-size: 12px; }
.stratcfg-num.sm { width: 52px; }
.stratcfg-mini { display: inline-flex; align-items: center; gap: 3px; font-size: 11px; color: #8b909a; }
.stratcfg .minibtn, .stratcfg .roleopt { white-space: nowrap; } /* 窄螢幕下按鈕文字不要斷行 */
.stratcfg-hint.warn { color: #d8ad48; }
.stratcfg-hint { font-size: 10px; color: #71767f; }
.stratcfg-chk { display: flex; align-items: center; gap: 4px; font-size: 11px; color: #cdd0d6; cursor: pointer; }
.stratcfg-chk.dim { color: #62666e; cursor: not-allowed; }
.stratcfg-chk.dim input { cursor: not-allowed; }
.stratcfg-dep { font-size: 10px; color: #b4642a; margin: -3px 0 0 4px; }
/* 標籤權限 */
.tabperms { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 6px 14px; margin: 4px 0 8px; }
/* 後台功能分頁 */
.adminnav { display: flex; flex-wrap: wrap; gap: 6px; margin: 10px 0 12px; }
.adminnav button { background: #1b1e26; border: 1px solid #2b2f3a; color: #b9bdc4; border-radius: 8px; padding: 5px 12px; font-size: 13px; cursor: pointer; }
.adminnav button.on { background: #2f4a7a33; border-color: #4a7ad4; color: #9dc0ff; font-weight: 700; }
.tabperm-row { display: flex; align-items: center; gap: 8px; }
.tabperm-name { flex: 1; font-size: 13px; color: #cdd0d6; display: inline-flex; align-items: center; gap: 4px; }
.tabperm-lock { font-style: normal; font-size: 11px; opacity: .7; }
.tabperm-opts { display: inline-flex; gap: 3px; }
.roleopt { background: #24262c; border: 1px solid #363943; color: #8b909a; border-radius: 6px; padding: 3px 8px; font-size: 11px; cursor: pointer; }
.roleopt.on { background: #2f4a7a33; border-color: #4a7ad4; color: #9dc0ff; font-weight: 700; }
.roleopt.dim { opacity: .45; cursor: not-allowed; }
.tagchip { background: #24262c; border: 1px solid #363943; color: #8b909a; border-radius: 999px; padding: 2px 9px; font-size: 11px; cursor: pointer; transition: all .12s; }
.tagchip.on { background: #2ea86a22; border-color: #2ea86a; color: #57d495; }
.stat-tags { font-size: 12px !important; color: #cdd0d6 !important; line-height: 1.35; }
.riskwarn { background: #4a2b1a; border: 1px solid #b4642a; color: #ffbf7d; border-radius: 8px; padding: 9px 12px; font-size: 13px; font-weight: 600; margin: 8px 0; line-height: 1.5; }
.strat-row { display: flex; align-items: center; gap: 9px; }
.strat-name { flex: 1; font-size: 13px; color: #cdd0d6; }
.strat-status { font-size: 11px; width: 28px; }
.toggle { width: 40px; height: 22px; border-radius: 11px; background: #3a3d45; border: none; padding: 0; position: relative; cursor: pointer; transition: background .15s; }
.toggle.on { background: #2ea86a; }
.toggle-knob { position: absolute; top: 2px; left: 2px; width: 18px; height: 18px; border-radius: 50%; background: #fff; transition: left .15s; }
.toggle.on .toggle-knob { left: 20px; }

.mai-live { background: #14161c; border: 1px solid #3a3320; border-radius: 12px; padding: 13px 16px; margin-bottom: 14px; }
.mai-live-top { display: flex; align-items: baseline; justify-content: space-between; gap: 10px; margin-bottom: 8px; flex-wrap: wrap; }
.mai-live-title { font-size: 14px; font-weight: 700; color: #d8ad48; display: inline-flex; align-items: center; gap: 7px; }
.mai-dot { width: 8px; height: 8px; border-radius: 50%; background: #2ec26b; box-shadow: 0 0 0 0 rgba(46,194,107,.6); animation: maipulse 1.8s infinite; }
@keyframes maipulse { 0% { box-shadow: 0 0 0 0 rgba(46,194,107,.5); } 70% { box-shadow: 0 0 0 6px rgba(46,194,107,0); } 100% { box-shadow: 0 0 0 0 rgba(46,194,107,0); } }
.mai-live-time { font-size: 11px; color: #8b909a; }
.mai-live-summary { font-size: 15px; font-weight: 600; color: #e8e9ec; line-height: 1.5; margin-bottom: 6px; }
.mai-live-body { font-size: 13px; color: #b9bdc4; line-height: 1.85; white-space: pre-wrap; word-break: break-word; }
.mk-actions { display: flex; align-items: center; gap: 10px; }
.clearbtn { background: transparent; border: 1px solid #4a2c2c; color: #c56a6a; font-size: 11px; padding: 3px 9px; border-radius: 6px; cursor: pointer; transition: .15s; }
.clearbtn:hover { border-color: #e05555; color: #e05555; background: rgba(224, 85, 85, 0.08); }
.tpfunnel { background: #14161c; border-radius: 10px; padding: 12px 14px 13px; margin: 12px 0 4px; }
.tpf-title { font-size: 12px; color: #8b909a; margin-bottom: 10px; }
.tpf-row { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
.tpf-row:last-child { margin-bottom: 0; }
.tpf-lbl { width: 62px; font-size: 12px; color: #c9cdd4; }
.tpf-bar { flex: 1; height: 18px; background: #1c1f27; border-radius: 5px; overflow: hidden; }
.tpf-bar i { display: block; height: 100%; background: #2e9d68; border-radius: 5px; }
.tpf-val { width: 112px; text-align: right; font-size: 12px; color: #e8e9ec; }
.tpf-val b { color: #4ec77e; }
.tppill { display: inline-block; font-size: 10px; color: #6b7078; border: 1px solid #2a2e37; border-radius: 4px; padding: 1px 5px; margin-right: 3px; }
.tppill.hit { background: #183a2a; color: #5fd39a; border-color: #1f5238; }
.tppct { font-style: normal; opacity: .72; }
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
/* ---- JMCH 登入 / 註冊 ----
   首頁已改為公開瀏覽,原本的全屏登入牆(.authgate)已移除;這組樣式現在
   改用於登入/註冊彈窗(.authmodal),掛在既有的 .overlay 底下。 */
/* .overlay 原本是給右側抽屜用的(justify-content:flex-end),彈窗要置中 */
.overlay-center { justify-content: center; align-items: center; padding: 16px; }
.authmodal { position: relative; max-height: 88vh; overflow-y: auto; }
.authx { position: absolute; top: 8px; right: 10px; }
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
.noticebox { width: min(440px, 92vw); max-height: 86vh; overflow-y: auto; background: #14161c; border: 1px solid #4a412a; border-radius: 16px; padding: 22px 20px; display: flex; flex-direction: column; gap: 14px; box-shadow: 0 20px 60px rgba(0,0,0,.55); }
.nb-title { margin: 0; color: #e8b84b; text-align: center; font-size: 17px; }
.nb-text { background: #0f1116; border: 1px solid #2a2e37; border-radius: 10px; padding: 12px 14px; font-size: 14px; color: #cdd0d6; line-height: 1.75; white-space: pre-wrap; word-break: break-word; }
.nb-dont { display: flex; align-items: center; gap: 7px; font-size: 12.5px; color: #8b909a; cursor: pointer; }
.nb-dont input { width: 15px; height: 15px; }
.nb-edit { min-height: 120px; resize: vertical; line-height: 1.6; padding: 8px 10px; font-family: inherit; }
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
