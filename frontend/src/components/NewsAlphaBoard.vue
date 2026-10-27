<!--
  消息雷達(資訊落差 · 管理員預設)。
  所有免費消息來源(交易所公告、韓所、Robinhood、Telegram 快訊、鏈上大額、GitHub / Snapshot / SEC、
  Polymarket、韓國溢價、解鎖、總經數據、新聞 RSS)收進同一條事件流,自動標記分類 / 方向 / 強度,
  並記錄之後 1m / 5m / 15m / 1h / 4h 的價格反應。分頁:消息流 / 統計 / 模擬單 / 領先指標 / 來源狀態。
-->
<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { authFetch } from '../lib/api'
import { fmtPrice } from '../lib/format'
import StrategyBook from './StrategyBook.vue'
import PageNav from './PageNav.vue'

const props = defineProps({
  canExit: { type: Boolean, default: false },
  risky: { type: Boolean, default: false },
  tags: { type: Array, default: () => [] },
})
const emit = defineEmits(['coin', 'exit'])

const CAT = {
  listing_kr: '韓所上幣', listing: '交易所上幣', listing_us: '美國券商上架', delist: '下架', warning: '警示 / 監控',
  warning_lift: '警示解除', hack: '被駭 / 漏洞', unlock: '代幣解鎖', etf: 'ETF / 申報', regulation: '監管',
  whale: '巨鯨 / 鏈上', listing_hint: '上幣前兆(鏈上)', governance: '治理提案', dev: '專案升級', premium: '韓國溢價',
  prediction: '預測市場', macro: '總經數據', partner: '合作 / 主網', other: '其他',
}
const SRC = {
  binance: '幣安', okx: 'OKX', bybit: 'Bybit', bithumb: 'Bithumb', upbit: 'Upbit', robinhood: 'Robinhood',
  onchain: '鏈上', github: 'GitHub', snapshot: 'Snapshot', sec: 'SEC', polymarket: 'Polymarket',
  premium: '韓國溢價', unlock: '解鎖', macro: '總經', rss: '新聞',
}
const srcName = (s) => SRC[s] || (s && s.startsWith('tg:') ? 'TG ' + s.slice(3) : s)
const HZ = ['1m', '5m', '15m', '1h', '4h']

const tab = ref('feed')
const TABS = [['feed', '消息流'], ['stats', '統計'], ['book', '模擬單'], ['lead', '領先指標'], ['sources', '來源狀態']]

const timePresets = [
  { label: '全部', ms: 0 }, { label: '1h', ms: 3600e3 }, { label: '24h', ms: 24 * 3600e3 },
  { label: '3日', ms: 3 * 24 * 3600e3 }, { label: '7日', ms: 7 * 24 * 3600e3 }, { label: '30日', ms: 30 * 24 * 3600e3 },
]
const win = ref(24 * 3600e3)

// ── 總覽(來源狀態 / 溢價 / 預測市場)──
const ov = ref(null)
async function loadOverview() {
  try {
    const res = await authFetch('/api/newsalpha')
    if (res.ok) ov.value = await res.json()
  } catch (e) { /* 下輪再試 */ }
}

// ── 消息流(DB 分頁)──
const fSource = ref('')
const fCat = ref('')
const fCoin = ref('')
const page = ref(1)
const feed = ref({ rows: [], total: 0, pages: 1, page: 1 })
const feedLoading = ref(false)
async function loadFeed() {
  feedLoading.value = true
  try {
    const q = new URLSearchParams({ win: win.value || 0, page: page.value, size: 50, source: fSource.value, category: fCat.value, coin: fCoin.value.trim() })
    const res = await authFetch('/api/newsalpha-events?' + q)
    if (res.ok) feed.value = await res.json()
  } catch (e) { /* ignore */ } finally { feedLoading.value = false }
}
watch([win, fSource, fCat], () => { page.value = 1; reload() })
watch(page, loadFeed)
let coinTimer = null
watch(fCoin, () => { clearTimeout(coinTimer); coinTimer = setTimeout(() => { page.value = 1; loadFeed() }, 400) })

// ── 統計 ──
const stats = ref(null)
const statBy = ref('category')
async function loadStats() {
  try {
    const res = await authFetch('/api/newsalpha-stats?win=' + (win.value || 0))
    if (res.ok) stats.value = await res.json()
  } catch (e) { /* ignore */ }
}
const statRows = computed(() => (stats.value ? (statBy.value === 'category' ? stats.value.by_category : stats.value.by_source) : []))

// ── 模擬單 ──
const book = ref(null)
async function loadBook() {
  try {
    const res = await authFetch('/api/newsalpha-book')
    if (res.ok) book.value = await res.json()
  } catch (e) { /* ignore */ }
}

function reload() {
  loadOverview()
  if (tab.value === 'feed') loadFeed()
  if (tab.value === 'stats') loadStats()
  if (tab.value === 'book') loadBook()
}
watch(tab, reload)
let timer = null
onMounted(() => {
  reload()
  timer = setInterval(() => { if (!document.hidden) reload() }, 20000)
})
onUnmounted(() => { clearInterval(timer); clearTimeout(coinTimer) })

// ── 顯示小工具 ──
const sources = computed(() => (ov.value ? ov.value.sources : []))
const okCount = computed(() => sources.value.filter((s) => s.last_ok && !s.fails).length)
const runnable = computed(() => sources.value.filter((s) => s.every_sec > 0 || s.last_try).length)
const sourceKeys = computed(() => sources.value.map((s) => s.key))
const today = computed(() => {
  const t = (ov.value && ov.value.today) || {}
  return Object.keys(t).sort((a, b) => t[b] - t[a]).map((k) => [k, t[k]])
})

function clock(ms) {
  if (!ms) return '—'
  const d = new Date(ms)
  const p = (n) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}
function ago(ms) {
  if (!ms) return '—'
  const s = Math.max(0, Math.round((Date.now() - ms) / 1000))
  if (s < 60) return s + ' 秒前'
  if (s < 3600) return Math.round(s / 60) + ' 分前'
  if (s < 86400) return Math.round(s / 3600) + ' 小時前'
  return Math.round(s / 86400) + ' 天前'
}
// 資訊落差:來源標示的發布時間 → 我們看到的時間
function lag(e) {
  if (!e.pub || e.pub > e.ts) return ''
  const s = Math.round((e.ts - e.pub) / 1000)
  if (s < 60) return `晚 ${s} 秒`
  if (s < 3600) return `晚 ${Math.round(s / 60)} 分`
  return `晚 ${Math.round(s / 3600)} 時`
}
const dirTxt = (d) => (d === 'long' ? '偏多' : d === 'short' ? '偏空' : '中性')
const dirCls = (d) => (d === 'long' ? 'long' : d === 'short' ? 'short' : 'mut')
const stars = (n) => '●'.repeat(n || 0) + '○'.repeat(Math.max(0, 3 - (n || 0)))
function rv(v) {
  if (v === null || v === undefined) return '—'
  return (v > 0 ? '+' : '') + v.toFixed(2) + '%'
}
const rcls = (v) => (v === null || v === undefined || v === 0 ? 'mut' : v > 0 ? 'long' : 'short')
// 照標記方向是否賺(用 1h,沒有就 15m)
function hitOf(e) {
  const r = e.r1h ?? e.r15m
  if (r === null || r === undefined || !e.dir) return ''
  return (e.dir === 'long' ? r > 0 : r < 0) ? '✓' : '✗'
}
const edgeCls = (v, n) => (!n ? 'mut' : v > 0 ? 'long' : v < 0 ? 'short' : 'mut')
function fmtUsd(v) {
  const a = Math.abs(v)
  if (a >= 1e6) return '$' + (v / 1e6).toFixed(1) + 'M'
  if (a >= 1e3) return '$' + (v / 1e3).toFixed(0) + 'K'
  return '$' + v.toFixed(0)
}
</script>

<template>
  <section>
    <div class="mk-head">
      <h2>消息雷達
        <span class="help" tabindex="0">?<span class="help-pop"><b>一句話:把所有免費消息來源收進同一條流,自動判斷利多利空,並記下之後的價格反應,用數據找出「哪類消息真的有方向」。</b><br>
        <b>【搶的是什麼】</b>拚不過機器人的毫秒速度,要搶的是:<b>語言落差</b>(韓文 / 中文消息傳到英文圈的時間差)、<b>解讀落差</b>(看得懂影響方向)、<b>聚合落差</b>(多個弱訊號同時出現)、<b>注意力落差</b>(小幣消息沒人盯)。<br>
        <b>【來源】</b>幣安 / OKX / Bybit / Bithumb / Upbit 公告、Robinhood 上架、Telegram 快訊頻道(含 Whale Alert)、以太坊交易所熱錢包大額轉帳、GitHub 新版本、Snapshot 治理、SEC 申報、Polymarket 機率急變、韓國溢價、代幣解鎖、總經數據、新聞 RSS。全部免費。<br>
        <b>【標記】</b>先用關鍵字規則判斷分類、方向(偏多 / 偏空)、強度(●),再由 AI 補判斷(每小時有上限)。<br>
        <b>【價格反應】</b>每則有幣種的消息,自動記錄看到當下價格,以及之後 1 分、5 分、15 分、1 小時、4 小時的漲跌,和 1 小時內最大漲 / 跌。<br>
        <b>【怎麼用】</b>先跑 4–8 週只收資料,到「統計」看哪個分類 / 來源照標記方向做是賺的、反應窗口多長;沒有優勢的類別就放棄。<br>
        ⚠️ 只用公開資訊。消息出來常有大滑點、插針,也常「買傳聞賣事實」。非投資建議。</span></span>
      </h2>
      <span class="mk-count" v-if="ov">來源正常 <b class="long">{{ okCount }}</b> / {{ runnable }} · AI 本小時 {{ ov.ai_used }}/{{ ov.ai_limit }}</span>
    </div>

    <div class="na-today" v-if="today.length">
      <span class="tf-label">24 小時</span>
      <button v-for="[k, n] in today" :key="k" class="na-chip" :class="{ on: fCat === k }" @click="fCat = fCat === k ? '' : k; tab = 'feed'">{{ CAT[k] || k }} <b>{{ n }}</b></button>
    </div>

    <div class="subtabs">
      <button v-for="t in TABS" :key="t[0]" class="subtab" :class="{ on: tab === t[0] }" @click="tab = t[0]">
        {{ t[1] }}<em v-if="t[0] === 'book' && book && book.open.length" class="navbadge">{{ book.open.length }}</em>
      </button>
    </div>

    <div class="timefilter" v-if="tab === 'feed' || tab === 'stats' || tab === 'book'">
      <span class="tf-label">時間範圍</span>
      <button v-for="p in timePresets" :key="p.ms" :class="{ on: win === p.ms }" @click="win = p.ms">{{ p.label }}</button>
    </div>

    <!-- 消息流 -->
    <template v-if="tab === 'feed'">
      <div class="na-filters">
        <select v-model="fSource">
          <option value="">全部來源</option>
          <option value="tg">Telegram(全部頻道)</option>
          <option v-for="k in sourceKeys" :key="k" :value="k">{{ srcName(k) }}</option>
        </select>
        <select v-model="fCat">
          <option value="">全部分類</option>
          <option v-for="(v, k) in CAT" :key="k" :value="k">{{ v }}</option>
        </select>
        <input v-model="fCoin" placeholder="幣種,例如 SOL" />
        <span class="mk-count">共 {{ feed.total }} 則</span>
      </div>
      <div class="na-scroll" v-if="feed.rows.length">
        <table class="grid na-feed">
          <thead>
            <tr>
              <th>看到時間</th><th>來源</th><th>分類</th><th>幣種</th><th>標記</th><th>消息</th><th>AI</th>
              <th class="r">1m</th><th class="r">5m</th><th class="r">15m</th><th class="r">1h</th><th class="r">4h</th><th class="r">1h 內高 / 低</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="e in feed.rows" :key="e.id" :class="{ 'na-strong': e.strength >= 2 }">
              <td class="na-time">{{ clock(e.ts) }}<small v-if="lag(e)">{{ lag(e) }}</small></td>
              <td><span class="na-src">{{ srcName(e.source) }}</span></td>
              <td><span class="na-cat" :class="'c-' + e.category">{{ CAT[e.category] || e.category }}</span></td>
              <td>
                <a v-if="e.coin" class="na-coin" @click="emit('coin', e.coin)">{{ e.coin }}</a><span v-else class="mut">—</span>
                <small v-if="e.price0" class="mut na-sub">{{ fmtPrice(e.price0) }}</small>
              </td>
              <td>
                <span :class="dirCls(e.dir)">{{ dirTxt(e.dir) }}</span>
                <small class="na-stars">{{ stars(e.strength) }}</small>
                <small v-if="e.traded" class="na-traded">已開模擬單</small>
              </td>
              <td class="na-title">
                <a v-if="e.url" :href="e.url" target="_blank" rel="noopener">{{ e.title_zh || e.title }}</a>
                <span v-else>{{ e.title_zh || e.title }}</span>
                <small v-if="e.title_zh && e.title_zh !== e.title" class="na-orig">{{ e.title }}</small>
              </td>
              <td class="na-ai">
                <template v-if="e.ai_dir"><span :class="dirCls(e.ai_dir)">{{ dirTxt(e.ai_dir) }}</span> <small class="na-stars">{{ stars(e.ai_strength) }}</small><small v-if="e.ai_reason" class="na-orig">{{ e.ai_reason }}</small></template>
                <span v-else class="mut">—</span>
              </td>
              <td class="r" :class="rcls(e.r1m)">{{ rv(e.r1m) }}</td>
              <td class="r" :class="rcls(e.r5m)">{{ rv(e.r5m) }}</td>
              <td class="r" :class="rcls(e.r15m)">{{ rv(e.r15m) }}</td>
              <td class="r" :class="rcls(e.r1h)">{{ rv(e.r1h) }} <small>{{ hitOf(e) }}</small></td>
              <td class="r" :class="rcls(e.r4h)">{{ rv(e.r4h) }}</td>
              <td class="r"><span class="long">{{ rv(e.up60) }}</span> / <span class="short">{{ rv(e.dn60) }}</span></td>
            </tr>
          </tbody>
        </table>
      </div>
      <PageNav :page="feed.page" :pages="feed.pages" :total="feed.total" @go="(p) => (page = p)" />
      <p v-if="!feed.rows.length && !feedLoading" class="empty">此範圍內沒有消息。剛啟動時各來源的第一輪只建基準,之後新出現的消息才會進來。</p>
      <p class="loginhint">價格反應在消息出現 1 小時後回填 1m~1h、4 小時後補 4h;「晚 N 秒 / 分」是來源標示的發布時間到我們看到的差距。強度 ●● 以上且有幣種的消息會推播「消息雷達」(帳戶設定可關)。</p>
    </template>

    <!-- 統計 -->
    <template v-else-if="tab === 'stats'">
      <div class="na-filters">
        <button class="na-chip" :class="{ on: statBy === 'category' }" @click="statBy = 'category'">依分類</button>
        <button class="na-chip" :class="{ on: statBy === 'source' }" @click="statBy = 'source'">依來源</button>
        <span class="mk-count" v-if="stats">事件 {{ stats.total }} 則 · 已回填 {{ stats.filled }} 則</span>
      </div>
      <div class="na-scroll" v-if="statRows.length">
        <table class="grid na-stats">
          <thead>
            <tr>
              <th>{{ statBy === 'category' ? '分類' : '來源' }}</th><th class="r">則數</th><th class="r">有方向</th>
              <th v-for="h in HZ" :key="h" class="r">{{ h }}</th>
              <th class="r">1h 內平均高 / 低</th><th class="r">AI 1h 勝率</th><th>最佳窗口</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in statRows" :key="r.key">
              <td>{{ statBy === 'category' ? (CAT[r.key] || r.key) : srcName(r.key) }}</td>
              <td class="r">{{ r.n }}</td>
              <td class="r">{{ r.dirn }}</td>
              <td v-for="(h, i) in HZ" :key="h" class="r na-edge">
                <template v-if="r.dirn && r.cnt[i]">
                  <span :class="edgeCls(r.edge[i], r.cnt[i])">{{ rv(r.edge[i]) }}</span>
                  <small>勝 {{ r.hit[i].toFixed(0) }}% · {{ r.cnt[i] }}</small>
                </template>
                <template v-else-if="r.cnt[i]"><span class="mut">{{ rv(r.avg[i]) }}</span><small>平均 · {{ r.cnt[i] }}</small></template>
                <span v-else class="mut">—</span>
              </td>
              <td class="r"><span class="long">{{ rv(r.up60) }}</span> / <span class="short">{{ rv(r.dn60) }}</span></td>
              <td class="r">{{ r.ai_n ? r.ai_hit.toFixed(0) + '% · ' + r.ai_n : '—' }}</td>
              <td><b v-if="r.best" class="long">{{ r.best }}</b><span v-else class="mut">—</span></td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="empty">還沒有回填好價格反應的事件(消息出現 1 小時後才會有數字)。</p>
      <p class="loginhint">有方向的分類顯示「照標記方向進場」的平均報酬(偏空的把漲跌取反)與勝率,小字是勝率與樣本數;中性分類只顯示原始平均漲跌。樣本少於 30 則時參考價值有限 —— 文件建議至少累積 4–8 週再判斷,沒有優勢的類別直接放棄。</p>
    </template>

    <!-- 模擬單 -->
    <template v-else-if="tab === 'book'">
      <p class="loginhint" style="margin-top:0">只有<b>交易所 / 券商官方公告</b>、可交易的分類、而且有幣安永續合約的幣才開單,以看到消息當下的價格進場:韓所上幣 多 止損 5% 止盈 15% · 交易所上幣 多 4% / 10% · 美國券商上架 多 3% / 8% · 下架 空 5% / 15% · 警示 空 4% / 10% · 被駭 空 4% / 10%。最多持有 1 小時,同幣平倉後 1 小時內不再開;來源發布 15 分鐘後才看到的消息不開單。<b>觀察用,不會自動下真單</b>(實盤設定成「全部跟單」也不會帶到)。</p>
      <StrategyBook
        :state="book"
        book="newsalpha"
        :win="win"
        :risky="props.risky"
        :tags="props.tags"
        :stats-order="['total', 'win', 'avg', 'type']"
        :can-exit="props.canExit"
        empty-text="尚無模擬單——需等交易所官方公告出現上幣 / 下架 / 警示 / 被駭消息。"
        @coin="(c) => emit('coin', c)"
        @exit="(id) => emit('exit', id, loadBook)"
      />
    </template>

    <!-- 領先指標:韓國溢價 + Polymarket -->
    <template v-else-if="tab === 'lead'">
      <h3 class="psub">韓國溢價 <small class="mut">Upbit 韓元價(以 USDT 韓元價換算)vs 幣安</small></h3>
      <div class="na-prem-sum" v-if="ov && ov.prem_n">
        <span>BTC 溢價 <b :class="rcls(ov.prem_btc)">{{ rv(ov.prem_btc) }}</b></span>
        <span>平均 <b :class="rcls(ov.prem_avg)">{{ rv(ov.prem_avg) }}</b>({{ ov.prem_n }} 檔)</span>
        <span class="mut">更新 {{ ov.prem_at ? ago(Date.parse(ov.prem_at)) : '—' }}</span>
      </div>
      <div class="na-scroll" v-if="ov && ov.premium.length">
        <table class="grid">
          <thead><tr><th>幣種</th><th class="r">溢價</th><th class="r">Upbit(KRW)</th><th class="r">幣安(USD)</th></tr></thead>
          <tbody>
            <tr v-for="p in ov.premium" :key="p.coin" class="clickable" @click="emit('coin', p.coin)">
              <td>{{ p.coin }}</td><td class="r" :class="rcls(p.premium)">{{ rv(p.premium) }}</td>
              <td class="r">{{ p.krw.toLocaleString() }}</td><td class="r">{{ fmtPrice(p.usd) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="empty">還沒有溢價資料(每分鐘更新)。</p>
      <p class="loginhint">某幣溢價 10 分鐘內跳升 ≥ 5 個百分點、且溢價本身 ≥ 8% 時會記一則「韓國溢價」事件 —— 韓國散戶在搶,常是行情前兆。表格列溢價最高的 15 檔與最低的 5 檔。</p>

      <h3 class="psub" style="margin-top:22px">Polymarket 機率變化 <small class="mut">加密類成交量前 40 的市場</small></h3>
      <div class="na-scroll" v-if="ov && ov.poly.length">
        <table class="grid">
          <thead><tr><th>市場</th><th class="r">「是」機率</th><th class="r">1 小時變化</th><th class="r">24h 成交</th></tr></thead>
          <tbody>
            <tr v-for="m in ov.poly" :key="m.id">
              <td class="na-title"><a :href="m.url" target="_blank" rel="noopener">{{ m.question }}</a></td>
              <td class="r">{{ (m.yes * 100).toFixed(0) }}%</td>
              <td class="r" :class="rcls(m.chg1h)">{{ (m.chg1h >= 0 ? '+' : '') + (m.chg1h * 100).toFixed(1) }} pt</td>
              <td class="r">{{ fmtUsd(m.vol24h) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="empty">還沒有 Polymarket 資料(每 5 分鐘更新)。</p>
      <p class="loginhint">1 小時內機率變動 ≥ 10 個百分點會記一則「預測市場」事件。</p>
    </template>

    <!-- 來源狀態 -->
    <template v-else-if="tab === 'sources'">
      <div class="na-scroll">
        <table class="grid na-src-tbl">
          <thead><tr><th>來源</th><th>類別</th><th class="r">間隔</th><th>狀態</th><th class="r">延遲</th><th>最近成功</th><th class="r">最近抓到</th><th class="r">新事件</th><th>備註 / 錯誤</th></tr></thead>
          <tbody>
            <tr v-for="s in sources" :key="s.key">
              <td>{{ s.label }}</td>
              <td class="mut">{{ s.group }}</td>
              <td class="r">{{ s.every_sec ? (s.every_sec >= 60 ? s.every_sec / 60 + ' 分' : s.every_sec + ' 秒') : '掛勾' }}</td>
              <td>
                <span v-if="s.fails" class="short">失敗 {{ s.fails }} 次</span>
                <span v-else-if="s.last_ok" class="long">正常</span>
                <span v-else class="mut">{{ s.every_sec ? '等待中' : '未啟用 / 等待' }}</span>
              </td>
              <td class="r">{{ s.last_try ? s.latency_ms + ' ms' : '—' }}</td>
              <td>{{ ago(s.last_ok) }}</td>
              <td class="r">{{ s.last_ok ? s.items : '—' }}</td>
              <td class="r">{{ s.events }}</td>
              <td class="na-err"><span v-if="s.last_err" class="short">{{ s.last_err }}</span><span v-else class="mut">{{ s.note }}</span></td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="loginhint">延遲 = 這台伺服器打一次該來源的往返時間(文件步驟 1:延遲偏高再考慮把收集器搬到東京節點)。連續失敗 3 次會發 Telegram 告警,恢復時再通知一次。<br>
        可用環境變數擴充:<code>NEWS_TG_CHANNELS</code>(Telegram 頻道)、<code>NEWS_GH_REPOS</code> / <code>GITHUB_TOKEN</code>(GitHub)、<code>SEC_UA</code>(SEC 聯絡資訊)、<code>ALCHEMY_URL</code> 或 <code>INFURA_URL</code>(鏈上監控)、<code>NEWS_WALLETS</code>(監控的錢包)。</p>
    </template>
  </section>
</template>

<style scoped>
.na-today { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin: 4px 0 12px; }
.na-today .tf-label { font-size: 12px; color: #8b909a; margin-right: 2px; }
.na-chip { background: #16181d; border: 1px solid #23262d; color: #c8cdd6; padding: 3px 10px; border-radius: 8px; cursor: pointer; font-size: 12px; }
.na-chip b { color: #e0b341; margin-left: 2px; }
.na-chip.on { background: #1b2942; border-color: #5b8def; color: #cfe0ff; }
.na-filters { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-bottom: 10px; }
.na-filters select, .na-filters input { background: #16181d; border: 1px solid #23262d; color: #c8cdd6; padding: 5px 8px; border-radius: 8px; font-size: 13px; }
.na-filters input { width: 130px; }
.na-scroll { overflow-x: auto; -webkit-overflow-scrolling: touch; }
.na-scroll table { min-width: 460px; }
.na-scroll table.na-feed { min-width: 1150px; }
.na-scroll table.na-stats { min-width: 900px; }
.na-scroll table.na-src-tbl { min-width: 860px; }
.na-src-tbl td { white-space: nowrap; }
.na-src-tbl td.na-err { white-space: normal; min-width: 200px; }
.na-feed td, .na-stats td { vertical-align: top; }
.na-time { white-space: nowrap; font-size: 12px; }
.na-time small { display: block; color: #e0b341; font-size: 11px; }
.na-src { font-size: 12px; color: #9aa3b2; white-space: nowrap; }
.na-cat { font-size: 11px; padding: 2px 6px; border-radius: 6px; background: #1d2028; color: #c8cdd6; white-space: nowrap; }
.na-cat.c-listing_kr, .na-cat.c-listing, .na-cat.c-listing_us, .na-cat.c-listing_hint { background: #12301f; color: #6fe3a1; }
.na-cat.c-delist, .na-cat.c-warning, .na-cat.c-hack { background: #3a1717; color: #ff8a8a; }
.na-cat.c-whale, .na-cat.c-premium, .na-cat.c-prediction { background: #1b2942; color: #9cc1ff; }
.na-coin { cursor: pointer; font-weight: 700; color: #e0b341; }
.na-sub { display: block; font-size: 11px; }
.na-stars { color: #e0b341; letter-spacing: -1px; font-size: 10px; margin-left: 2px; }
.na-traded { display: block; font-size: 11px; color: #9cc1ff; }
.na-title { min-width: 260px; max-width: 480px; }
.na-title a { color: #dfe3ea; text-decoration: none; }
.na-title a:hover { text-decoration: underline; }
.na-orig { display: block; color: #6b7078; font-size: 11px; margin-top: 2px; }
.na-ai { min-width: 120px; max-width: 200px; font-size: 12px; }
.na-strong { background: rgba(224, 179, 65, 0.05); }
.na-edge small { display: block; color: #6b7078; font-size: 10px; white-space: nowrap; }
.na-err { font-size: 12px; max-width: 320px; word-break: break-word; }
.na-prem-sum { display: flex; flex-wrap: wrap; gap: 16px; margin: 6px 0 10px; font-size: 13px; }
.mut { color: #6b7078; }
code { background: #1d2028; padding: 1px 4px; border-radius: 4px; font-size: 11px; }
</style>
