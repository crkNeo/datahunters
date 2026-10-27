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
const emit = defineEmits(['coin', 'exit', 'delete'])

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
const RKEYS = ['r1m', 'r5m', 'r15m', 'r1h', 'r4h']
// 這個時間點還沒補(時間未到,或抓 K 線失敗正在重試):顯示「待」;已處理過但沒資料才顯示「—」
const pending = (e, i) => e.coin && (e.stage || 0) <= i && (e.stage || 0) < 5 && Date.now() - e.ts < 7 * 86400e3

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
// 需要另外設定才會跑的來源(例如鏈上監控沒設 ALCHEMY_URL / INFURA_URL)
const isOff = (s) => !s.every_sec && !s.last_try && (s.note || '').includes('未設定')
const runnable = computed(() => sources.value.filter((s) => s.every_sec > 0 || s.last_try).length)
// 來源篩選:Telegram 各頻道併成一顆
const sourcePills = computed(() => {
  const out = []
  let tg = false
  for (const s of sources.value) {
    if (s.key.startsWith('tg:')) tg = true
    else out.push([s.key, srcName(s.key)])
  }
  if (tg) out.push(['tg', 'Telegram'])
  return out
})
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
// 分類標籤沿用「市場快訊」的配色(.newscat.nc-*)
const CAT_CLS = {
  listing_kr: 'nc-crypto', listing: 'nc-crypto', listing_us: 'nc-crypto', listing_hint: 'nc-crypto', warning_lift: 'nc-crypto', partner: 'nc-crypto',
  delist: 'nc-geo', warning: 'nc-geo', hack: 'nc-hack', unlock: 'nc-trade', etf: 'nc-inst', regulation: 'nc-reg',
  whale: 'nc-whale', governance: 'nc-inst', dev: 'nc-inst', premium: 'nc-cb', prediction: 'nc-cb', macro: 'nc-figure', other: 'nc-misc',
}
const catCls = (c) => CAT_CLS[c] || 'nc-misc'
const dirCls = (d) => (d === 'long' ? 'long' : d === 'short' ? 'short' : '')
const stars = (n) => '●'.repeat(n || 0) + '○'.repeat(Math.max(0, 3 - (n || 0)))
function rv(v) {
  if (v === null || v === undefined) return '—'
  return (v > 0 ? '+' : '') + v.toFixed(2) + '%'
}
const rcls = (v) => (v === null || v === undefined || v === 0 ? 'na-mut' : v > 0 ? 'long' : 'short')
// 照標記方向是否賺(用 1h,沒有就 15m)
function hitOf(e) {
  const r = e.r1h ?? e.r15m
  if (r === null || r === undefined || !e.dir) return ''
  return (e.dir === 'long' ? r > 0 : r < 0) ? '✓' : '✗'
}
const edgeCls = (v, n) => (!n ? 'na-mut' : v > 0 ? 'long' : v < 0 ? 'short' : 'na-mut')
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
      <h2>消息雷達<span class="help" tabindex="0">?<span class="help-pop"><b>一句話:把所有免費消息來源收進同一條流,自動判斷利多利空,並記下之後的價格反應,用數據找出「哪類消息真的有方向」。</b><br>
        <b>【搶的是什麼】</b>拚不過機器人的毫秒速度,要搶的是:<b>語言落差</b>(韓文 / 中文消息傳到英文圈的時間差)、<b>解讀落差</b>(看得懂影響方向)、<b>聚合落差</b>(多個弱訊號同時出現)、<b>注意力落差</b>(小幣消息沒人盯)。<br>
        <b>【來源】</b>幣安 / OKX / Bybit / Bithumb / Upbit 公告、Robinhood 上架、Telegram 快訊頻道(含 Whale Alert)、以太坊交易所熱錢包大額轉帳、GitHub 新版本、Snapshot 治理、SEC 申報、Polymarket 機率急變、韓國溢價、代幣解鎖、總經數據、新聞 RSS。全部免費。<br>
        <b>【標記】</b>先用關鍵字規則判斷分類、方向(偏多 / 偏空)、強度(●),再由 AI 補判斷(每小時有上限)。<br>
        <b>【價格反應】</b>每則有幣種的消息,自動記錄看到當下價格,以及之後 1 分、5 分、15 分、1 小時、4 小時的漲跌,和 1 小時內最大漲 / 跌。<br>
        <b>【怎麼用】</b>先跑 4–8 週只收資料,到「統計」看哪個分類 / 來源照標記方向做是賺的、反應窗口多長;沒有優勢的類別就放棄。<br>
        ⚠️ 只用公開資訊。消息出來常有大滑點、插針,也常「買傳聞賣事實」。非投資建議。</span></span></h2>
      <span class="mk-count" v-if="ov">來源正常 {{ okCount }} / {{ runnable }} · AI 本小時 {{ ov.ai_used }}/{{ ov.ai_limit }}</span>
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
      <div class="timefilter">
        <span class="tf-label">分類</span>
        <button :class="{ on: fCat === '' }" @click="fCat = ''">全部</button>
        <button v-for="[k, n] in today" :key="k" :class="{ on: fCat === k }" @click="fCat = k">{{ CAT[k] || k }} {{ n }}</button>
        <button v-if="fCat && !today.some((t) => t[0] === fCat)" class="on">{{ CAT[fCat] || fCat }}</button>
        <span class="tf-note">數字 = 24 小時則數</span>
      </div>
      <div class="timefilter">
        <span class="tf-label">來源</span>
        <button :class="{ on: fSource === '' }" @click="fSource = ''">全部</button>
        <button v-for="[k, label] in sourcePills" :key="k" :class="{ on: fSource === k }" @click="fSource = k">{{ label }}</button>
      </div>
      <div class="timefilter">
        <span class="tf-label">幣種</span>
        <input v-model="fCoin" class="na-coinin" placeholder="例如 SOL" />
        <span class="tf-note">共 {{ feed.total }} 則</span>
      </div>

      <div class="na-scroll" v-if="feed.rows.length">
        <table class="grid na-feed">
          <thead>
            <tr>
              <th>時間</th><th>分類 / 來源</th><th>幣種 / 方向</th><th>消息 / AI 判斷</th>
              <th class="r">1m</th><th class="r">5m</th><th class="r">15m</th><th class="r">1h</th><th class="r">4h</th><th class="r na-nw">1h 高 / 低</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="e in feed.rows" :key="e.id">
              <td class="tsmall na-nw">{{ clock(e.ts) }}<div v-if="lag(e)" class="na-lag">{{ lag(e) }}</div><div v-if="e.late" class="na-late" title="伺服器關機期間發布、重啟後才補進來;時間與價格反應從來源發布時間起算,不推播、不開單">停機補記</div><button v-if="props.canExit" class="clearbtn na-del" @click="emit('delete', e.id, loadFeed)">刪除</button></td>
              <td class="na-nw">
                <span class="newscat" :class="catCls(e.category)">{{ CAT[e.category] || e.category }}</span>
                <div class="tsmall na-sub">{{ srcName(e.source) }}</div>
              </td>
              <td class="na-nw">
                <a v-if="e.coin" class="na-coin" @click="emit('coin', e.coin)">{{ e.coin }}</a><span v-else class="tsmall">—</span>
                <span v-if="e.dir" class="chip na-dir" :class="e.dir">{{ dirTxt(e.dir) }}</span>
                <div class="tsmall na-sub">
                  <span v-if="e.price0">{{ fmtPrice(e.price0) }}</span>
                  <span class="na-str" :title="'強度 ' + e.strength">{{ stars(e.strength) }}</span>
                </div>
                <div v-if="e.traded" class="tsmall na-traded">已開模擬單</div>
              </td>
              <td class="na-title">
                <a v-if="e.url" :href="e.url" target="_blank" rel="noopener" class="upbit-link">{{ e.title_zh || e.title }}</a>
                <span v-else class="upbit-link">{{ e.title_zh || e.title }}</span>
                <div v-if="e.title_zh && e.title_zh !== e.title" class="upbit-orig">{{ e.title }}</div>
                <div v-if="e.ai_dir" class="na-ai">
                  <span class="tsmall">AI</span>
                  <span v-if="e.ai_dir !== 'neutral'" class="chip na-dir" :class="e.ai_dir">{{ dirTxt(e.ai_dir) }}</span><span v-else class="tsmall">中性</span>
                  <span class="na-str">{{ stars(e.ai_strength) }}</span>
                  <span v-if="e.ai_reason" class="tsmall">{{ e.ai_reason }}</span>
                </div>
              </td>
              <td v-if="e.nopx" colspan="6" class="r tsmall">幣安沒有這個幣的行情,記不到價格反應</td>
              <template v-else>
                <td v-for="(k, i) in RKEYS" :key="k" class="r na-num" :class="rcls(e[k])">
                  <template v-if="e[k] !== null && e[k] !== undefined">{{ rv(e[k]) }}<span v-if="k === 'r1h' && hitOf(e)" class="tsmall"> {{ hitOf(e) }}</span></template>
                  <span v-else-if="pending(e, i)" class="tsmall" title="還沒補:時間未到(到點後約 1 分鐘內補上),或抓價格失敗、稍後自動重試">待</span>
                  <span v-else class="tsmall">—</span>
                </td>
                <td class="r na-num">
                  <template v-if="e.up60 !== null && e.up60 !== undefined"><span :class="rcls(e.up60)">{{ rv(e.up60) }}</span><div :class="rcls(e.dn60)">{{ rv(e.dn60) }}</div></template>
                  <span v-else-if="pending(e, 3)" class="tsmall">待</span>
                  <span v-else class="tsmall">—</span>
                </td>
              </template>
            </tr>
          </tbody>
        </table>
      </div>
      <PageNav :page="feed.page" :pages="feed.pages" :total="feed.total" @go="(p) => (page = p)" />
      <p v-if="!feed.rows.length && !feedLoading" class="empty">此範圍內沒有消息。剛啟動時各來源的第一輪只建基準,之後新出現的消息才會進來。</p>
      <p class="loginhint">價格反應在每個時間點到了之後約 1 分鐘內補上(「待」= 時間還沒到;用歷史 K 線算,伺服器關機錯過的開機後會自動補上;「停機補記」是關機期間發布、重啟後補進來的公告;「—」= 那個時間點幣安沒有成交資料);「晚 N 秒 / 分」是來源標示的發布時間到我們看到的差距;1h 欄的 ✓ / ✗ 是照標記方向做有沒有賺。強度 ●● 以上且有幣種的消息會推播「消息雷達」(帳戶設定可關)。</p>
    </template>

    <!-- 統計 -->
    <template v-else-if="tab === 'stats'">
      <div class="timefilter">
        <span class="tf-label">分組</span>
        <button :class="{ on: statBy === 'category' }" @click="statBy = 'category'">依分類</button>
        <button :class="{ on: statBy === 'source' }" @click="statBy = 'source'">依來源</button>
        <span class="tf-note" v-if="stats">事件 {{ stats.total }} 則 · 已回填 {{ stats.filled }} 則</span>
      </div>
      <div class="na-scroll" v-if="statRows.length">
        <table class="grid na-stats">
          <thead>
            <tr>
              <th>{{ statBy === 'category' ? '分類' : '來源' }}</th><th class="r">則數</th><th class="r">有方向</th>
              <th v-for="h in HZ" :key="h" class="r">{{ h }}</th>
              <th class="r">1h 內平均高 / 低</th><th class="r">AI 1h 勝率</th><th class="r">最佳窗口</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in statRows" :key="r.key">
              <td>
                <span v-if="statBy === 'category'" class="newscat" :class="catCls(r.key)">{{ CAT[r.key] || r.key }}</span>
                <span v-else>{{ srcName(r.key) }}</span>
              </td>
              <td class="r na-num">{{ r.n }}</td>
              <td class="r na-num">{{ r.dirn }}</td>
              <td v-for="(h, i) in HZ" :key="h" class="r na-num">
                <template v-if="r.dirn && r.cnt[i]">
                  <span :class="edgeCls(r.edge[i], r.cnt[i])">{{ rv(r.edge[i]) }}</span>
                  <div class="tsmall">勝 {{ r.hit[i].toFixed(0) }}% · {{ r.cnt[i] }} 則</div>
                </template>
                <template v-else-if="r.cnt[i]"><span class="na-mut">{{ rv(r.avg[i]) }}</span><div class="tsmall">平均 · {{ r.cnt[i] }} 則</div></template>
                <span v-else class="tsmall">—</span>
              </td>
              <td class="r na-num"><span :class="rcls(r.up60)">{{ rv(r.up60) }}</span><div :class="rcls(r.dn60)">{{ rv(r.dn60) }}</div></td>
              <td class="r na-num">{{ r.ai_n ? r.ai_hit.toFixed(0) + '%' : '—' }}<div v-if="r.ai_n" class="tsmall">{{ r.ai_n }} 則</div></td>
              <td class="r"><span v-if="r.best" class="chip long">{{ r.best }}</span><span v-else class="tsmall">—</span></td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="empty">還沒有回填好價格反應的事件(消息出現 1 小時後才會有數字)。</p>
      <p class="loginhint">有方向的分類顯示「<b>照標記方向進場</b>」的平均報酬(偏空的把漲跌取反)與勝率;中性分類只顯示原始平均漲跌。樣本少於 30 則時參考價值有限 —— 文件建議至少累積 4–8 週再判斷,沒有優勢的類別直接放棄。</p>
    </template>

    <!-- 模擬單 -->
    <template v-else-if="tab === 'book'">
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
      <p class="loginhint">只有<b>交易所 / 券商官方公告</b>、可交易的分類、而且有幣安永續合約的幣才開單,以看到消息當下的價格進場:<br>
        韓所上幣 做多 止損 5% / 止盈 15% · 交易所上幣 做多 4% / 10% · 美國券商上架 做多 3% / 8% · 下架 做空 5% / 15% · 警示 做空 4% / 10% · 被駭 做空 4% / 10%。<br>
        最多持有 1 小時,同幣平倉後 1 小時內不再開;來源發布 15 分鐘後才看到的消息不開單。<b>觀察用,不會自動下真單</b>(實盤設定成「全部跟單」也不會帶到)。</p>
    </template>

    <!-- 領先指標:韓國溢價 + Polymarket -->
    <template v-else-if="tab === 'lead'">
      <div class="mk-head">
        <h3 class="psub">韓國溢價</h3>
        <span class="mk-count" v-if="ov && ov.prem_n">BTC {{ rv(ov.prem_btc) }} · 平均 {{ rv(ov.prem_avg) }} · {{ ov.prem_n }} 檔 · {{ ov.prem_at ? ago(Date.parse(ov.prem_at)) : '—' }}</span>
      </div>
      <div class="na-scroll" v-if="ov && ov.premium.length">
        <table class="grid">
          <thead><tr><th>幣種</th><th class="r">溢價</th><th class="r">Upbit(KRW)</th><th class="r">幣安(USD)</th></tr></thead>
          <tbody>
            <tr v-for="p in ov.premium" :key="p.coin" class="clickable" @click="emit('coin', p.coin)">
              <td><b>{{ p.coin }}</b></td>
              <td class="r na-num" :class="rcls(p.premium)">{{ rv(p.premium) }}</td>
              <td class="r na-num">{{ p.krw.toLocaleString() }}</td>
              <td class="r na-num">{{ fmtPrice(p.usd) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="empty">還沒有溢價資料(每分鐘更新)。</p>
      <p class="loginhint">Upbit 韓元價(以 USDT 韓元價換算成美元)對比幣安。某幣溢價 10 分鐘內跳升 ≥ 5 個百分點、且溢價本身 ≥ 8% 時會記一則「韓國溢價」事件 —— 韓國散戶在搶,常是行情前兆。表格列溢價最高的 15 檔與最低的 5 檔。</p>

      <div class="mk-head" style="margin-top:22px">
        <h3 class="psub">Polymarket 機率變化</h3>
        <span class="mk-count">加密類成交量前 40 的市場</span>
      </div>
      <div class="na-scroll" v-if="ov && ov.poly.length">
        <table class="grid">
          <thead><tr><th>市場</th><th class="r">「是」機率</th><th class="r">1 小時變化</th><th class="r">24h 成交</th></tr></thead>
          <tbody>
            <tr v-for="m in ov.poly" :key="m.id">
              <td><a :href="m.url" target="_blank" rel="noopener" class="upbit-link">{{ m.question }}</a></td>
              <td class="r na-num">{{ (m.yes * 100).toFixed(0) }}%</td>
              <td class="r na-num" :class="rcls(m.chg1h)">{{ (m.chg1h >= 0 ? '+' : '') + (m.chg1h * 100).toFixed(1) }} pt</td>
              <td class="r na-num">{{ fmtUsd(m.vol24h) }}</td>
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
          <thead><tr><th>來源</th><th>類別</th><th class="r">間隔</th><th>狀態</th><th class="r">延遲</th><th class="r">最近成功</th><th class="r">最近抓到</th><th class="r">新事件</th><th>備註 / 錯誤</th></tr></thead>
          <tbody>
            <tr v-for="s in sources" :key="s.key">
              <td><b>{{ s.label }}</b></td>
              <td class="tsmall">{{ s.group }}</td>
              <td class="r na-num">{{ s.every_sec ? (s.every_sec >= 60 ? s.every_sec / 60 + ' 分' : s.every_sec + ' 秒') : '沿用' }}</td>
              <td class="na-stat">
                <span v-if="s.fails" class="chip short">失敗 {{ s.fails }} 次</span>
                <span v-else-if="s.last_ok" class="chip long">正常</span>
                <span v-else-if="isOff(s)" class="chip">未設定</span>
                <span v-else class="tsmall">等待第一次更新</span>
                <div v-if="s.fails && s.last_err" class="na-why short">{{ s.last_err }}</div>
                <div v-else-if="isOff(s)" class="na-why tsmall">{{ s.note }}</div>
              </td>
              <td class="r na-num">{{ s.last_try ? s.latency_ms + ' ms' : '—' }}</td>
              <td class="r tsmall">{{ ago(s.last_ok) }}</td>
              <td class="r na-num">{{ s.last_ok ? s.items : '—' }}</td>
              <td class="r na-num">{{ s.events }}</td>
              <td class="na-err"><span v-if="s.last_err" class="short">{{ s.last_err }}</span><span v-else-if="!isOff(s)" class="tsmall">{{ s.note }}</span></td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="loginhint">延遲 = 這台伺服器打一次該來源的往返時間(文件步驟 1:延遲偏高再考慮把收集器搬到東京節點)。連續失敗 3 次會發 Telegram 告警,恢復時再通知一次。<br>
        可用環境變數擴充:<b>NEWS_TG_CHANNELS</b>(Telegram 頻道)、<b>NEWS_GH_REPOS</b> / <b>GITHUB_TOKEN</b>(GitHub)、<b>SEC_UA</b>(SEC 聯絡資訊)、<b>ALCHEMY_URL</b> 或 <b>INFURA_URL</b>(鏈上監控)、<b>NEWS_WALLETS</b>(監控的錢包)。</p>
    </template>
  </section>
</template>

<style scoped>
/* 只補版面;顏色 / 字體一律沿用全站 token 與既有元件(.timefilter / .newscat / .chip / .grid / .upbit-link) */
.na-scroll { overflow-x: auto; -webkit-overflow-scrolling: touch; }
.na-scroll table { min-width: 460px; }
.na-scroll table.na-feed { min-width: 960px; }
.na-scroll table.na-stats { min-width: 920px; }
.na-scroll table.na-src-tbl { min-width: 880px; }
.na-feed td, .na-stats td { vertical-align: top; }
.na-nw { white-space: nowrap; }
.na-num { font-family: var(--f-mono); font-variant-numeric: tabular-nums; white-space: nowrap; }
.na-mut { color: var(--c-mut2); }
.na-lag { color: var(--c-gold); font-size: 11px; margin-top: 2px; }
.na-coin { cursor: pointer; font-family: var(--f-disp); font-weight: 700; color: var(--c-gold-b); }
.na-coin:hover { text-decoration: underline; }
.na-str { color: var(--c-gold); font-size: 9px; letter-spacing: 1px; margin-left: 4px; }
.na-sub { margin-top: 3px; }
.na-dir { font-size: 11px; padding: 1px 6px; margin-left: 6px; }
.na-late { display: block; width: fit-content; margin-top: 3px; font-size: 10.5px; padding: 0 6px; border-radius: 5px; color: var(--c-steel); background: var(--c-steel-bg); }
.na-del { display: block; margin-top: 6px; white-space: nowrap; }
.na-traded { color: var(--c-steel); margin-top: 2px; }
.na-title { min-width: 240px; }
.na-ai { margin-top: 5px; display: flex; flex-wrap: wrap; align-items: center; gap: 4px; }
.na-ai .na-dir { margin-left: 0; }
.na-src-tbl td { white-space: nowrap; }
.na-src-tbl td.na-stat { white-space: normal; min-width: 120px; max-width: 260px; }
.na-why { font-size: 11px; margin-top: 4px; line-height: 1.4; word-break: break-word; }
.na-src-tbl td.na-err { white-space: normal; min-width: 220px; font-size: 12px; }
.na-coinin { width: 140px; background: var(--c-bg2); border: 1px solid var(--c-line); border-radius: var(--r-sm); padding: 4px 10px; color: var(--c-txt); font-size: 12px; outline: none; }
.na-coinin:focus { border-color: var(--c-gold-d); }
.na-coinin::placeholder { color: var(--c-mut2); }
</style>
