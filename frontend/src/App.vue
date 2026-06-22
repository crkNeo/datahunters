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

const boardRows = computed(() =>
  Object.entries(board.value)
    .map(([coin, v]) => ({ coin, ...v }))
    .sort((a, b) => Math.abs(b.score) - Math.abs(a.score))
)

// actionable entry signals: coins the scorer actually rates long/short
// (|score| >= 20, same bar as the recommendation cards).
const signals = computed(() => boardRows.value.filter((r) => r.bias === 'long' || r.bias === 'short'))

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
  timer = setInterval(() => {
    loadHome()
    loadBoard()
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
        <button v-for="(r, i) in (home.long_recs || [])" :key="r.coin" class="rec-row" :class="{ featured: r.featured }" @click="openDetail(r.coin)">
          <span class="rec-coin">
            <i class="medal">{{ medal(i) }}</i>{{ r.coin }}
            <em v-if="r.featured" class="hot">★ 強力</em>
          </span>
          <span class="rec-price">{{ fmtPrice(r.price) }}</span>
          <span class="bars">
            <i v-for="n in 5" :key="n" class="bar" :class="{ on: n <= r.strength, long: n <= r.strength }"></i>
          </span>
          <span class="r" :class="r.chg >= 0 ? 'long' : 'short'">{{ fmtPct(r.chg) }}</span>
        </button>
        <p v-if="!(home.long_recs || []).length" class="empty">目前無做多訊號</p>
      </section>

      <!-- 做空推薦 -->
      <section class="card rec">
        <div class="rec-head"><span class="led short"></span>做空推薦</div>
        <div class="rec-cols"><span>幣種</span><span>價格</span><span>推薦指數</span><span class="r">漲跌幅</span></div>
        <button v-for="(r, i) in (home.short_recs || [])" :key="r.coin" class="rec-row" :class="{ 'featured short-feat': r.featured }" @click="openDetail(r.coin)">
          <span class="rec-coin">
            <i class="medal">{{ medal(i) }}</i>{{ r.coin }}
            <em v-if="r.featured" class="hot short-hot">★ 強力</em>
          </span>
          <span class="rec-price">{{ fmtPrice(r.price) }}</span>
          <span class="bars">
            <i v-for="n in 5" :key="n" class="bar" :class="{ on: n <= r.strength, short: n <= r.strength }"></i>
          </span>
          <span class="r" :class="r.chg >= 0 ? 'long' : 'short'">{{ fmtPct(r.chg) }}</span>
        </button>
        <p v-if="!(home.short_recs || []).length" class="empty">目前無做空訊號</p>
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
        <span class="mk-count">{{ signals.length }} 個可進場訊號（評分 ≥ 20 / ≤ −20）</span>
      </div>
      <table v-if="signals.length" class="grid">
        <thead>
          <tr><th>幣種</th><th>方向</th><th class="r">評分</th><th>推薦指數</th><th>品質</th><th class="r">OI 1h%</th><th class="r">CVD%</th><th class="r">資金費率</th></tr>
        </thead>
        <tbody>
          <tr v-for="r in signals" :key="r.coin" class="clickable" :class="{ selected: r.coin === detailCoin }" @click="openDetail(r.coin)">
            <td class="coin">{{ r.coin }}</td>
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
