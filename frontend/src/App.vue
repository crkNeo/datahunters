<script setup>
import { ref, onMounted, onUnmounted, computed } from 'vue'

// ---- shared data ----
const home = ref(null)
const board = ref({})
const boardUpdated = ref('')
const error = ref('')
let timer = null

const mainTab = ref('list') // list | oi | signals
const marketSort = ref('vol') // vol | gainers | losers

async function loadHome() {
  try {
    const res = await fetch('/api/home')
    if (!res.ok) throw new Error('HTTP ' + res.status)
    home.value = await res.json()
    error.value = ''
  } catch (e) {
    error.value = String(e)
  }
}

async function loadBoard() {
  try {
    const res = await fetch('/api/oi-cache')
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
    const res = await fetch('/api/radar')
    if (!res.ok) return
    radar.value = await res.json()
  } catch (e) {
    /* radar is secondary */
  }
}

const paper = ref(null)
const gamble = ref(null)
async function loadPaper() {
  try {
    const [p, g] = await Promise.all([fetch('/api/paper'), fetch('/api/gamble')])
    if (p.ok) paper.value = await p.json()
    if (g.ok) gamble.value = await g.json()
  } catch (e) {
    /* paper is secondary */
  }
}
const book = computed(() => (mainTab.value === 'gamble' ? gamble.value : paper.value))

const scoreLog = ref([])
async function loadScoreLog() {
  try {
    const res = await fetch('/api/scorelog')
    if (res.ok) scoreLog.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}

const options = ref(null)
async function loadOptions() {
  try {
    const res = await fetch('/api/options')
    if (res.ok) options.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}
// interpretation helpers for the options dashboard
function skewLabel(rr) {
  if (rr > 1) return { txt: '偏多(call 較貴)', cls: 'long' }
  if (rr < -1) return { txt: '偏空(put 較貴/避險)', cls: 'short' }
  return { txt: '中性', cls: 'neutral' }
}
function pcLabel(pc) {
  if (pc > 1.1) return { txt: '避險偏空', cls: 'short' }
  if (pc < 0.9) return { txt: '偏多', cls: 'long' }
  return { txt: '均衡', cls: 'neutral' }
}
function painLabel(co) {
  if (!co.spot || !co.max_pain) return { txt: '-', cls: 'neutral' }
  const diff = ((co.max_pain - co.spot) / co.spot) * 100
  if (diff > 0.5) return { txt: `磁吸偏上 +${diff.toFixed(1)}%`, cls: 'long' }
  if (diff < -0.5) return { txt: `磁吸偏下 ${diff.toFixed(1)}%`, cls: 'short' }
  return { txt: '貼近現價', cls: 'neutral' }
}
const termMaxIV = (term) => Math.max(1, ...(term || []).map((t) => t.atm_iv))

// optBias: combine the options metrics into one bull/bear read + the reasons.
// Skew (RR) is the primary directional signal; Put/Call is positioning;
// Max Pain and term structure are minor/risk modifiers.
function optBias(c) {
  let score = 0
  const reasons = []
  // 1) 25Δ 偏斜 RR — 主訊號(權重最大)
  if (c.rr25 >= 2) {
    score += 2
    reasons.push(`偏斜 RR +${c.rr25}:call 較貴 → 搶漲,偏多`)
  } else if (c.rr25 <= -2) {
    score -= 2
    reasons.push(`偏斜 RR ${c.rr25}:put 較貴 → 避險,偏空${c.rr25 <= -8 ? '(已近極端,留意反轉)' : ''}`)
  } else {
    reasons.push(`偏斜 RR ${c.rr25}:中性`)
  }
  // 2) Put/Call(OI)— 倉位
  if (c.pc_ratio_oi <= 0.7) {
    score += 1
    reasons.push(`Put/Call ${c.pc_ratio_oi}:call 為主 → 倉位偏多`)
  } else if (c.pc_ratio_oi >= 1.3) {
    score -= 1
    reasons.push(`Put/Call ${c.pc_ratio_oi}:put 為主 → 避險偏空`)
  } else {
    reasons.push(`Put/Call ${c.pc_ratio_oi}:均衡`)
  }
  // 3) Max Pain vs 現價 — 僅近到期(<=10天)才計
  if (c.spot && c.max_pain && c.near_days <= 10) {
    const d = ((c.max_pain - c.spot) / c.spot) * 100
    if (d >= 1) {
      score += 0.5
      reasons.push(`Max Pain 在現價上方 +${d.toFixed(1)}%:到期磁吸偏上`)
    } else if (d <= -1) {
      score -= 0.5
      reasons.push(`Max Pain 在現價下方 ${d.toFixed(1)}%:到期磁吸偏下`)
    }
  }
  // 4) 期限結構倒掛 — 近期恐慌的風險旗標
  if (c.term && c.term.length >= 2 && c.atm_iv) {
    const far = c.term[c.term.length - 1].atm_iv
    if (c.atm_iv > far * 1.05) {
      score -= 0.5
      reasons.push(`期限倒掛(近月 ${c.atm_iv}% > 遠月 ${far}%):近期有急事/恐慌`)
    }
  }
  let label = '中性',
    cls = 'neutral'
  if (score >= 1.5) (label = '偏多'), (cls = 'long')
  else if (score <= -1.5) (label = '偏空'), (cls = 'short')
  else if (score > 0) (label = '中性偏多'), (cls = 'long')
  else if (score < 0) (label = '中性偏空'), (cls = 'short')
  return { label, cls, reasons }
}
const optReads = computed(() => {
  const m = {}
  if (options.value) for (const c of options.value.coins) m[c.coin] = optBias(c)
  return m
})


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
    const res = await fetch('/api/coin/' + coin)
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

onMounted(() => {
  loadHome()
  loadBoard()
  loadRadar()
  loadPaper()
  loadScoreLog()
  loadOptions()
  timer = setInterval(() => {
    loadHome()
    loadBoard()
    loadRadar()
    loadPaper()
    loadScoreLog()
    loadOptions()
  }, 15000)
})
onUnmounted(() => clearInterval(timer))
</script>

<template>
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
      <span class="brand">數據看板 · self-hosted</span>
    </div>
  </header>

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
      <span class="navgroup">選幣專區</span>
      <button :class="{ active: mainTab === 'oi' }" @click="mainTab = 'oi'">OI 儀表板</button>
      <button :class="{ active: mainTab === 'list' }" @click="mainTab = 'list'">幣種一覽</button>
      <span class="navgroup sep">訊號專區</span>
      <button :class="{ active: mainTab === 'signals' }" @click="mainTab = 'signals'">
        數據訊號<em v-if="signals.length" class="navbadge">{{ signals.length }}</em>
      </button>
      <button :class="{ active: mainTab === 'scorelog' }" @click="mainTab = 'scorelog'">
        訊號紀錄<em v-if="scoreLog.length" class="navbadge">{{ scoreLog.length }}</em>
      </button>
      <button :class="{ active: mainTab === 'radar' }" @click="mainTab = 'radar'">爆發雷達</button>
      <button :class="{ active: mainTab === 'paper' }" @click="mainTab = 'paper'">
        訊號追蹤<em v-if="paper && paper.open.length" class="navbadge">{{ paper.open.length }}</em>
      </button>
      <button :class="{ active: mainTab === 'gamble' }" @click="mainTab = 'gamble'">
        賭博單<em v-if="gamble && gamble.open.length" class="navbadge">{{ gamble.open.length }}</em>
      </button>
      <span class="navgroup sep">主力專區</span>
      <button :class="{ active: mainTab === 'majors' }" @click="mainTab = 'majors'">BTC/ETH 選擇權</button>
      <span class="navgroup sep">美股專區</span>
      <button :class="{ active: mainTab === 'stocks' }" @click="mainTab = 'stocks'">
        美股代幣<em v-if="radar && (radar.stocks || []).length" class="navbadge">{{ (radar.stocks || []).length }}</em>
      </button>
    </nav>

    <!-- 合約市場 (幣種一覽) -->
    <section v-if="mainTab === 'list' && home">
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
          <tr><th>幣種</th><th class="r">評分</th><th>方向</th><th>品質</th><th class="r">OKX%</th><th class="r">OI 1h%</th><th class="r">CVD%</th><th class="r">資金費率</th></tr>
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
            <td class="r">{{ (r.funding_rate * 100)?.toFixed(4) }}%</td>
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
          <tr><th>幣種</th><th>方向</th><th class="r">評分</th><th>推薦指數</th><th>品質</th><th class="r">OI 1h%</th><th class="r">CVD%</th><th class="r">資金費率</th></tr>
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
            <td class="r">{{ (r.funding_rate * 100)?.toFixed(4) }}%</td>
          </tr>
        </tbody>
      </table>
      <p v-else class="empty">目前無確定可進場的訊號（沒有任何幣種評分達到 ±20）</p>
    </section>

    <!-- 爆發雷達 (breakout radar, small coins included) -->
    <section v-else-if="mainTab === 'radar'">
      <div class="mk-head">
        <h2>爆發雷達</h2>
        <span class="mk-count" v-if="radar">掃描 {{ radar.scanned }} 個合約（全市場・含小幣）· 早期優先</span>
      </div>
      <p class="radar-note">
        <b>點火分數(0–100)</b>：回測驗證的暴噴前兆——「<b>OI 堆積</b>(最強)＋<b>成交量突增</b>＋<b>鯨魚單量</b>＋剛開始微動」，
        並以 24h 漲幅做「早晚」加權——<b>已經噴一大段的會被降權</b>，讓雷達偏向「<b>剛要發動</b>」而非追高。
        欄位：<b>量×</b>=近 3h 均量 ÷ 近 48h 均量；<b>OI</b>=未平倉近 12h 變化(堆積)；<b>3H</b>=近 3 小時漲跌。
        <br>⚠️ 發掘用途、高風險、誤報多,非回測驗證的精準進場訊號。
      </p>
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
        <h2>訊號紀錄</h2>
        <span class="mk-count">每次評分跨過 ±20(進入做多/做空)的時間點 · 共 {{ scoreLog.length }} 筆</span>
      </div>
      <p class="radar-note">每當追蹤幣種的評分**從 &lt;20 跨到 ≥20**(或 ≤−20)就記錄當下的時間與價格,方便你回去對照那個時間點的線圖。最多保留最近 300 筆。</p>
      <table v-if="scoreLog.length" class="grid">
        <thead><tr><th>時間</th><th>幣種</th><th>方向</th><th class="r">評分</th><th class="r">當時價格</th></tr></thead>
        <tbody>
          <tr v-for="(e, i) in scoreLog" :key="i" class="clickable" @click="openDetail(e.coin)">
            <td class="tsmall">{{ fmtClock(e.time) }}</td>
            <td class="coin">{{ e.coin }}</td>
            <td><span class="dir" :class="e.bias === 'long' ? 'long' : 'short'">{{ e.bias === 'long' ? '做多' : '做空' }}</span></td>
            <td class="r" :class="['score', e.bias === 'long' ? 'long' : 'short']">{{ e.score }}</td>
            <td class="r">{{ fmtPrice(e.price) }}</td>
          </tr>
        </tbody>
      </table>
      <p v-else class="empty">尚無紀錄（剛啟動需等有幣種評分跨過 ±20）</p>
    </section>

    <section v-else-if="mainTab === 'paper' || mainTab === 'gamble'">
      <div class="mk-head">
        <h2>{{ mainTab === 'gamble' ? '賭博單（模擬）' : '訊號追蹤（模擬）' }}</h2>
        <span class="mk-count" v-if="book">每 30 秒監控 · 止盈 +0.618R / 止損 −0.5R</span>
      </div>
      <p v-if="mainTab === 'gamble'" class="radar-note">
        <b>賭博版</b>:門檻放低(點火 <b>≥45</b>)、<b>不要求突破觸發</b>(連已經在半山腰、已噴的也照追)、冷卻只 1 小時。
        刻意「沒紀律地 FOMO」——拿來跟左邊「訊號追蹤」對照,親眼看<b>有紀律 vs 賭博</b>的實際績效差多少。⚠️ 回測顯示這種追法期望值較差,純供觀察。
      </p>
      <p v-else class="radar-note">
        只在分數<b>從 &lt;55 向上突破 ≥55 的當下</b>才以現價進場(不追半山腰、重啟也不追已高分的);
        <b>止盈 +0.618R、止損 −0.5R</b>(回測最佳)。轉強烈反向訊號→反向出場並反手;4h 冷卻只擋同方向。⚠️ 純模擬、未計手續費。
      </p>
      <div v-if="book" class="pstats">
        <div class="pstat"><div class="stat-k">已結束</div><div class="stat-v">{{ book.stats.closed }}</div></div>
        <div class="pstat"><div class="stat-k">勝率</div><div class="stat-v" :class="book.stats.win_rate >= 50 ? 'long' : 'short'">{{ book.stats.win_rate }}%</div></div>
        <div class="pstat"><div class="stat-k">平均損益</div><div class="stat-v" :class="book.stats.avg_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(book.stats.avg_pnl) }}</div></div>
        <div class="pstat"><div class="stat-k">累計損益</div><div class="stat-v" :class="book.stats.total_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(book.stats.total_pnl) }}</div></div>
      </div>

      <h3 class="psub" v-if="book">進行中 ({{ book.open.length }})</h3>
      <table v-if="book && book.open.length" class="grid">
        <thead><tr><th>幣種</th><th>方向</th><th class="r">進場</th><th class="r">現價</th><th class="r">損益%</th><th class="r">止盈</th><th class="r">止損</th><th class="r">進場時間</th><th class="r">持倉</th></tr></thead>
        <tbody>
          <tr v-for="t in book.open" :key="t.coin + t.open_time" class="clickable" @click="openDetail(t.coin)">
            <td class="coin">{{ t.coin }}</td>
            <td><span class="dir" :class="t.dir === 'long' ? 'long' : 'short'">{{ t.dir === 'long' ? '做多' : '做空' }}</span></td>
            <td class="r">{{ fmtPrice(t.entry) }}</td>
            <td class="r">{{ fmtPrice(t.cur) }}</td>
            <td class="r" :class="t.pnl_pct >= 0 ? 'long' : 'short'"><b>{{ fmtPct(t.pnl_pct) }}</b></td>
            <td class="r long">{{ fmtPrice(t.tp) }}</td>
            <td class="r short">{{ fmtPrice(t.sl) }}</td>
            <td class="r tsmall">{{ fmtClock(t.open_time) }}</td>
            <td class="r">{{ fmtDur(holdMs(t)) }}</td>
          </tr>
        </tbody>
      </table>
      <p v-else-if="book" class="empty">目前無進行中的模擬部位</p>

      <h3 class="psub" v-if="book && book.closed.length">已結束 ({{ book.closed.length }})</h3>
      <table v-if="book && book.closed.length" class="grid">
        <thead><tr><th>幣種</th><th>方向</th><th class="r">進場</th><th class="r">出場</th><th>結果</th><th class="r">損益%</th><th class="r">進場時間</th><th class="r">出場時間</th><th class="r">持倉</th></tr></thead>
        <tbody>
          <tr v-for="(t, i) in book.closed" :key="t.coin + i" class="clickable" @click="openDetail(t.coin)">
            <td class="coin">{{ t.coin }}</td>
            <td><span class="dir" :class="t.dir === 'long' ? 'long' : 'short'">{{ t.dir === 'long' ? '做多' : '做空' }}</span></td>
            <td class="r">{{ fmtPrice(t.entry) }}</td>
            <td class="r">{{ fmtPrice(t.cur) }}</td>
            <td><span class="otag" :class="t.outcome">{{ t.outcome === 'tp' ? '止盈 TP' : t.outcome === 'sl' ? '止損 SL' : t.outcome === 'reversed' ? '反向出場' : '逾時' }}</span></td>
            <td class="r" :class="t.pnl_pct >= 0 ? 'long' : 'short'"><b>{{ fmtPct(t.pnl_pct) }}</b></td>
            <td class="r tsmall">{{ fmtClock(t.open_time) }}</td>
            <td class="r tsmall">{{ fmtClock(t.close_time) }}</td>
            <td class="r">{{ fmtDur(holdMs(t)) }}</td>
          </tr>
        </tbody>
      </table>
      <p v-else-if="book" class="empty">尚無已結束的模擬交易（剛啟動需等訊號觸發 TP/SL）</p>
    </section>

    <!-- 美股代幣 (tokenized US stocks/ETFs, same ignition radar) -->
    <section v-else-if="mainTab === 'stocks'">
      <div class="mk-head">
        <h2>美股代幣</h2>
        <span class="mk-count" v-if="radar">代幣化美股/ETF 永續 · 同一套爆發雷達</span>
      </div>
      <p class="radar-note">Binance 上**非加密**的代幣化永續(個股 / ETF / 商品,如 AAPL、TSLA、GLD…,依 underlyingType 自動分流)。用與爆發雷達相同的點火分數排序,但**不納入加密雷達與模擬交易**。⚠️ 高風險、誤報多。</p>
      <div v-if="radar && (radar.stocks || []).length" class="card">
        <div class="radar-row rhead">
          <span>代幣</span>
          <span class="r" title="點火分數 0–100：量增+OI堆積+鯨魚單量 的綜合強度">點火</span>
          <span class="r">24H</span><span class="r" title="近 3h 均量 ÷ 近 48h 均量">量×</span>
          <span class="r" title="未平倉量近 12h 變化(堆積)">OI</span><span class="r" title="近 3 小時漲跌">3H</span>
        </div>
        <div v-for="x in radar.stocks" :key="x.coin" class="radar-item clickable" @click="openDetail(x.coin)">
          <div class="radar-row">
            <span class="coin">{{ x.coin }}<small class="vtag">${{ fmtNum(x.vol_24h) }}</small></span>
            <span class="r"><b class="ignite" :class="x.accel >= 0 ? 'long' : 'short'">{{ x.score }}</b></span>
            <span class="r" :class="x.chg_24h >= 0 ? 'long' : 'short'">{{ fmtPct(x.chg_24h) }}</span>
            <span class="r">{{ x.vol_spike }}×</span>
            <span class="r" :class="x.oi_chg >= 0 ? 'long' : 'short'">{{ x.oi_chg >= 0 ? '+' : '' }}{{ x.oi_chg }}%</span>
            <span class="r" :class="x.accel >= 0 ? 'long' : 'short'">{{ fmtPct(x.accel) }}</span>
          </div>
          <div class="radar-entry">現價 <b>{{ fmtPrice(x.price) }}</b> · 止盈 <b class="long">{{ fmtPrice(x.tp) }}</b> · 止損 <b class="short">{{ fmtPrice(x.sl) }}</b></div>
        </div>
      </div>
      <p v-else-if="radar" class="empty">目前無顯著異動的美股代幣</p>
      <p v-else class="loading">掃描中…</p>
    </section>

    <!-- BTC/ETH 選擇權深度分析 -->
    <section v-else-if="mainTab === 'majors'">
      <div class="mk-head">
        <h2>BTC / ETH 選擇權深度分析</h2>
        <span class="mk-count" v-if="options && options.updated_at">更新：{{ new Date(options.updated_at).toLocaleTimeString() }}</span>
      </div>
      <p class="radar-note">選擇權監控儀表(Deribit 即時)。⚠️ 這是「盤勢判讀儀表、非回測訊號」——選擇權無免費歷史、無法回測,僅供主觀判斷主力幣的方向與波動。RR &gt; 0 偏多、&lt; 0 偏空避險;Put/Call &gt; 1 避險偏空;Max Pain 為到期日價格「磁吸位」。</p>
      <div v-if="options && options.coins.length" class="opt-grid">
        <section v-for="c in options.coins" :key="c.coin" class="card opt-card">
          <div class="opt-head">
            <span class="opt-coin">{{ c.coin }}
              <em v-if="optReads[c.coin]" class="opt-bias" :class="optReads[c.coin].cls">{{ optReads[c.coin].label }}
                <i class="info">i<span class="bubble wide"><b>選擇權綜合判讀依據</b>
                  <span v-for="(r, i) in optReads[c.coin].reasons" :key="i" class="reason">• {{ r }}</span>
                  <span class="reason dim">(以偏斜 RR 為主、Put/Call 為輔;非回測訊號)</span>
                </span></i>
              </em>
            </span>
            <span class="opt-spot">{{ fmtPrice(c.spot) }}</span>
          </div>
          <div class="opt-metrics">
            <div class="om">
              <div class="om-k">DVOL 隱含波動
                <i class="info">i<span class="bubble">30 天前瞻波動預期(年化%)。<b>看相對自身高低</b>:低檔=市場鬆懈、短線訊號偏廢;急拉=恐慌進場。</span></i>
              </div>
              <div class="om-v">{{ c.dvol }}</div>
            </div>
            <div class="om">
              <div class="om-k">ATM IV(近月)
                <i class="info">i<span class="bubble">近月平價隱含波動。<b>預期日波動 ≈ IV ÷ 19</b>。IV 高→停損放寬;低→收窄。比 DVOL 高=近期有事件。</span></i>
              </div>
              <div class="om-v">{{ c.atm_iv }}%</div>
            </div>
            <div class="om">
              <div class="om-k">25Δ 偏斜(RR)
                <i class="info">i<span class="bubble">call IV − put IV,方向情緒。<b>&gt;0 偏多</b>(搶漲)、<b>&lt;0 偏空</b>(買跌保險)。極端負常見於恐慌底→反轉。與現貨方向背離最有訊息。</span></i>
              </div>
              <div class="om-v" :class="skewLabel(c.rr25).cls">{{ c.rr25 }}</div>
              <div class="om-sub" :class="skewLabel(c.rr25).cls">{{ skewLabel(c.rr25).txt }}</div>
            </div>
            <div class="om">
              <div class="om-k">Put/Call(OI)
                <i class="info">i<span class="bubble">put OI ÷ call OI。<b>&gt;1 避險偏空</b>、<b>&lt;1 偏多</b>。太極端常當反指標。與 RR 打架時=「持多單同時買保險」。</span></i>
              </div>
              <div class="om-v" :class="pcLabel(c.pc_ratio_oi).cls">{{ c.pc_ratio_oi }}</div>
              <div class="om-sub" :class="pcLabel(c.pc_ratio_oi).cls">{{ pcLabel(c.pc_ratio_oi).txt }}</div>
            </div>
            <div class="om">
              <div class="om-k">Max Pain(近月)
                <i class="info">i<span class="bubble">讓選擇權買方整體賠最多的到期價=價格「磁吸位」。<b>越接近到期越有參考</b>(尤其月底週五);離現價越遠=潛在引力方向。</span></i>
              </div>
              <div class="om-v">{{ fmtPrice(c.max_pain) }}</div>
              <div class="om-sub" :class="painLabel(c).cls">{{ painLabel(c).txt }}</div>
            </div>
            <div class="om"><div class="om-k">近月到期</div><div class="om-v">{{ c.near_expiry }}</div><div class="om-sub">{{ c.near_days }} 天</div></div>
          </div>

          <div class="opt-sub-h">IV 期限結構(ATM)
            <i class="info">i<span class="bubble">各到期的平價 IV。<b>遠 &gt; 近 = 平靜常態</b>;<b>近 &gt; 遠(倒掛)= 近期有急事/恐慌</b>,強警訊。盯「倒掛」就對了。</span></i>
          </div>
          <div class="term">
            <div v-for="t in c.term" :key="t.expiry" class="term-bar">
              <span class="term-lab">{{ t.expiry }}</span>
              <span class="term-track"><i :style="{ width: (t.atm_iv / termMaxIV(c.term)) * 100 + '%' }"></i></span>
              <span class="term-iv">{{ t.atm_iv }}%</span>
            </div>
          </div>

          <div class="opt-walls">
            <div class="wall-col">
              <div class="opt-sub-h long">Call 牆(壓力)
                <i class="info">i<span class="bubble">OI 最集中的 call 履約價=上方<b>壓力位</b>。價格上去常被壓/釘住;<b>放量突破=真突破</b>。貼近現價(黃字)最有效。</span></i>
              </div>
              <div v-for="s in c.top_calls" :key="'c' + s.strike" class="wall-row">
                <span :class="{ near: Math.abs(s.strike - c.spot) / c.spot < 0.02 }">{{ fmtPrice(s.strike) }}</span>
                <span class="r">{{ s.oi }}</span>
              </div>
            </div>
            <div class="wall-col">
              <div class="opt-sub-h short">Put 牆(支撐)
                <i class="info">i<span class="bubble">OI 最集中的 put 履約價=下方<b>支撐位</b>。價格下去常有撐/反彈;<b>放量跌破=真破位</b>。貼近現價(黃字)最有效。</span></i>
              </div>
              <div v-for="s in c.top_puts" :key="'p' + s.strike" class="wall-row">
                <span :class="{ near: Math.abs(s.strike - c.spot) / c.spot < 0.02 }">{{ fmtPrice(s.strike) }}</span>
                <span class="r">{{ s.oi }}</span>
              </div>
            </div>
          </div>
        </section>
      </div>
      <p v-else class="loading">載入 Deribit 選擇權資料中…</p>
    </section>

    <footer>所有數據來自交易所公開 API，僅供研究。評分權重為自訂，請以自己的回測為準。非投資建議。</footer>
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
body { margin: 0; background: #0a0b0e; color: #e8eaed; font-family: system-ui, -apple-system, "PingFang TC", sans-serif; }
.long { color: #2ec26b; } .short { color: #ff5c5c; } .neutral { color: #b8bcc4; }
.err { color: #ff6b6b; font-size: 12px; }
.r { text-align: right; }

/* top bar */
.topbar { display: flex; align-items: center; gap: 16px; padding: 10px 20px; border-bottom: 1px solid #1c1f25; background: #0c0e12; position: sticky; top: 0; z-index: 10; }
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
.gauge-val { font-size: 34px; font-weight: 800; line-height: 1; margin-top: -18px; }
.gauge-label { font-size: 13px; margin-top: 4px; }
.gauge-prev { font-size: 11px; color: #8b909a; margin-top: 4px; }
.gauge-prev em { font-style: normal; }
.gauge-zones { display: flex; gap: 8px; font-size: 10px; color: #5c616b; margin-top: 10px; }

/* nav */
.mainnav { display: flex; align-items: center; gap: 8px; margin: 8px 0 16px; flex-wrap: wrap; }
.navgroup { font-size: 11px; color: #5c616b; margin-right: 2px; }
.navgroup.sep { margin-left: 14px; }
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
.otag.tp { background: #103a24; color: #2ec26b; } .otag.sl { background: #3a1010; color: #ff5c5c; } .otag.expired { background: #1f2229; color: #b8bcc4; } .otag.reversed { background: #2a2410; color: #f4d774; }
.tsmall { font-size: 11px; color: #8b909a; }
.navbadge { font-style: normal; font-size: 10px; font-weight: 700; background: #e0b341; color: #1a1407; border-radius: 8px; padding: 0 6px; margin-left: 6px; }
.dir { display: inline-block; font-size: 12px; font-weight: 700; padding: 2px 8px; border-radius: 6px; }
.dir.long { background: #103a24; color: #2ec26b; } .dir.short { background: #3a1010; color: #ff5c5c; }

/* market head + sort */
.mk-head { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: 10px; }
.mk-head h2 { font-size: 16px; margin: 0; }
.mk-count { font-size: 12px; color: #8b909a; }
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
