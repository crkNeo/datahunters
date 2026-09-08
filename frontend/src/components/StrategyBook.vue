<!--
  策略表(共用)—— 風控警語 + 統計列 + 止盈漏斗 + 進行中/已結束兩張表。

  冥王星與微策略組(逆勢/布林/乖離/布乖v2/布林EMA)的表格本來是逐字重複的兩份,
  這裡合成一份。三處差異用 props 表達,而不是在元件裡判斷是哪個策略:

    statsOrder  統計列的欄位順序(兩邊原本不同,保留各自的順序)
    canExit     是否顯示「手動出場」欄(一律限管理員 —— 由呼叫端傳 can('admin'))
    emptyText   沒有任何單時的提示文字(各策略的進場條件不同)

  ⚠️ 雷達三本(星軌/超新星/銀河)沒有併進來:它多了時間範圍篩選與 CSV 匯出,
  且統計列欄位不同,硬合會變成一堆 flag。那是使用者實際下單的畫面,維持原狀。
-->
<script setup>
import { ref, computed, watch, onMounted } from 'vue'
import { fmtPct, fmtPrice, fmtClock, lvlPct, pctOf, outcomeCN, outcomeCls } from '../lib/format'
import { authFetch } from '../lib/api'
import PageNav from './PageNav.vue'

const props = defineProps({
  // PaperState: { open: [], ... }。進行中(open)仍走即時 state;已結束/統計改後端 DB 分頁。
  state: { type: Object, default: null },
  book: { type: String, default: '' }, // 策略 key → 後端歷史查詢
  win: { type: Number, default: 0 },   // 時間範圍(ms),進 SQL 篩選
  // 風控警語(由後台「顯示風控建議」控制)
  risky: { type: Boolean, default: false },
  // 策略類型標籤,例如 ['激進','高頻']
  tags: { type: Array, default: () => [] },
  // 統計列順序:type | win | avg | total
  statsOrder: { type: Array, default: () => ['type', 'win', 'avg', 'total'] },
  // 顯示「手動出場」欄
  canExit: { type: Boolean, default: false },
  emptyText: { type: String, default: '尚無訊號。' },
})

defineEmits(['coin', 'exit'])

// 已結束歷史:後端 DB 分頁(全歷史、依時間窗、統計於全篩選集聚合)。每頁 50 筆。
const hist = ref({ rows: [], total: 0, pages: 1, page: 1, stats: { closed: 0, win_rate: 0, avg_pnl: 0, total_pnl: 0, tp1: 0, tp2: 0, tp3: 0, multi_tp: false } })
const page = ref(1)
async function fetchHist() {
  if (!props.book) return
  try {
    const res = await authFetch(`/api/strat-history?book=${encodeURIComponent(props.book)}&win=${props.win || 0}&page=${page.value}&size=50`)
    if (res.ok) hist.value = await res.json()
  } catch (e) { /* secondary */ }
}
watch(() => [props.book, props.win], () => { page.value = 1; fetchHist() })
watch(page, fetchHist)
onMounted(fetchHist)
defineExpose({ refresh: () => { page.value = 1; fetchHist() } }) // 清單/手動出場後父層可呼叫

const stats = computed(() => hist.value.stats || {})

// SMC_V2 把「待觸發掛單」(status=pending)也放進 open;它們還沒成交,不是持倉,
// 不能顯示 TP 進度/損益。以下把兩者分開計數,列上再逐筆用徽章區分。
const openFilled = computed(() => (props.state?.open || []).filter((t) => t.status !== 'pending').length)
const openPending = computed(() => (props.state?.open || []).filter((t) => t.status === 'pending').length)

// 多週期策略(如 訂單塊:1h/4h 同頁)才帶 tf 欄位;有才顯示「週期」欄,其餘策略維持原樣。
const hasTf = computed(() =>
  [...(props.state?.open || []), ...(hist.value.rows || [])].some((t) => t.tf)
)
const tfLabel = (tf) => (tf || '').toUpperCase()

// 進行中「狀態」欄(依 strategy.html mock:達 TP1 / 達 TP2 / 達最終 / 持倉中 / 待觸發)
function tpStatus(t) {
  if (t.status === 'pending') return '待觸發'
  const legs = t.legs || 0
  if (legs >= 3) return '達最終'
  if (legs >= 2) return '達 TP2'
  if (legs >= 1) return '達 TP1'
  return '持倉中'
}
function tpStatusCls(t) {
  if (t.status === 'pending') return 'pend'
  return (t.legs || 0) >= 1 ? 'tp' : 'run'
}
</script>

<template>
  <div>
    <p v-if="risky" class="riskwarn">⚠️ 目前盤面使用此策略風險較大,請謹慎操作</p>

    <div v-if="book || state" class="pstats">
      <template v-for="k in statsOrder" :key="k">
        <div v-if="k === 'type'" class="pstat">
          <div class="stat-k">策略類型<span class="help" tabindex="0">?<span class="help-pop">此策略的操作屬性:激進/保守(風險)、高頻/低頻(開單頻率)、長線/短線(持倉時間)。由管理端設定。</span></span></div>
          <div class="stat-v stat-tags">{{ tags.join('・') || '—' }}</div>
        </div>
        <div v-else-if="k === 'win'" class="pstat">
          <div class="stat-k">勝率</div>
          <div class="stat-v" :class="stats.win_rate >= 50 ? 'long' : 'short'">{{ stats.win_rate }}%</div>
        </div>
        <div v-else-if="k === 'avg'" class="pstat">
          <div class="stat-k">平均損益</div>
          <div class="stat-v" :class="stats.avg_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(stats.avg_pnl) }}</div>
        </div>
        <div v-else-if="k === 'total'" class="pstat">
          <div class="stat-k">累計損益</div>
          <div class="stat-v" :class="stats.total_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(stats.total_pnl) }}</div>
        </div>
      </template>
    </div>

    <div v-if="stats.closed && (stats.multi_tp || stats.tp1)" class="tpfunnel">
      <div class="tpf-title">止盈達成漏斗 · 共 {{ stats.closed }} 筆已結束</div>
      <div v-for="lv in [1, 2, 3]" :key="lv" class="tpf-row">
        <span class="tpf-lbl">TP{{ lv }} 達成</span>
        <span class="tpf-bar"><i :style="{ width: pctOf(stats['tp' + lv], stats.closed) + '%' }"></i></span>
        <span class="tpf-val">{{ stats['tp' + lv] }} 筆 · <b>{{ pctOf(stats['tp' + lv], stats.closed) }}%</b></span>
      </div>
    </div>

    <h3 class="psub" v-if="state && state.open.length">進行中 ({{ openFilled }})<span v-if="openPending" class="pendcount"> · 待觸發 {{ openPending }}</span></h3>
    <p v-if="state && state.open.length" class="tp-legend">綠色 = 已觸及止盈</p>
    <div v-if="state && state.open.length" class="tblwrap">
    <table class="grid">
      <thead><tr><th>幣種</th><th v-if="hasTf">週期</th><th>方向</th><th class="r">進場/觸發</th><th class="r">現價</th><th class="r">未實現%</th><th class="r">最大獲利</th><th class="r">止損</th><th class="r">TP1</th><th class="r">TP2</th><th class="r">最終</th><th>狀態</th><th class="r">時間</th><th v-if="canExit" class="r">操作</th></tr></thead>
      <tbody>
        <tr v-for="t in state.open" :key="t.coin + t.open_time" class="clickable" @click="$emit('coin', t.coin)">
          <td class="coin">{{ t.coin }}</td>
          <td v-if="hasTf"><span class="tfbadge">{{ tfLabel(t.tf) }}</span></td>
          <td><span class="dir" :class="t.dir === 'long' ? 'long' : 'short'">{{ t.dir === 'long' ? '做多' : '做空' }}</span></td>
          <td class="r">{{ fmtPrice(t.entry) }}</td>
          <td class="r">{{ fmtPrice(t.cur) }}</td>
          <td class="r" :class="t.status === 'pending' ? '' : (t.pnl_pct >= 0 ? 'long' : 'short')">
            <b v-if="t.status !== 'pending'">{{ fmtPct(t.pnl_pct) }}</b><span v-else class="tsmall">—</span>
          </td>
          <td class="r long"><b v-if="t.status !== 'pending' && t.max_gain">{{ fmtPct(t.max_gain) }}</b><span v-else class="tsmall">—</span></td>
          <td class="r short">{{ fmtPrice(t.sl) }}<small v-if="t.status !== 'pending' && t.legs >= 2" class="vtag"> 鎖利</small><small v-else-if="t.status !== 'pending' && t.legs >= 1" class="vtag"> 保本</small></td>
          <td class="r tp-cell" :class="{ hit: t.status !== 'pending' && t.legs >= 1 }">{{ t.tp1 ? fmtPrice(t.tp1) : '—' }}</td>
          <td class="r tp-cell" :class="{ hit: t.status !== 'pending' && t.legs >= 2 }">{{ t.tp2 ? fmtPrice(t.tp2) : '—' }}</td>
          <td class="r tp-cell" :class="{ hit: t.status !== 'pending' && t.legs >= 3 }">{{ fmtPrice(t.tp) }}</td>
          <td><span class="stag" :class="tpStatusCls(t)">{{ tpStatus(t) }}</span></td>
          <td class="r tsmall">{{ fmtClock(t.open_time) }}</td>
          <td v-if="canExit" class="r"><button v-if="t.status !== 'pending'" class="exitbtn" @click.stop="$emit('exit', t.id)">手動出場</button><small v-else class="tsmall">—</small></td>
        </tr>
      </tbody>
    </table>
    </div>

    <h3 class="psub" v-if="hist.total">已結束 ({{ hist.total }})</h3>
    <table v-if="hist.rows.length" class="grid">
      <thead><tr><th>幣種</th><th v-if="hasTf">週期</th><th>方向</th><th class="r">進場</th><th class="r">出場</th><th>結果</th><th class="r">損益%</th><th class="r">最大漲幅</th><th class="r">出場時間</th></tr></thead>
      <tbody>
        <tr v-for="(t, i) in hist.rows" :key="i" class="clickable" @click="$emit('coin', t.coin)">
          <td class="coin">{{ t.coin }}</td>
          <td v-if="hasTf"><span class="tfbadge">{{ tfLabel(t.tf) }}</span></td>
          <td><span class="dir" :class="t.dir === 'long' ? 'long' : 'short'">{{ t.dir === 'long' ? '做多' : '做空' }}</span></td>
          <td class="r">{{ fmtPrice(t.entry) }}</td>
          <td class="r">{{ t.outcome === 'cancel' ? '—' : fmtPrice(t.cur) }}</td>
          <td><span class="otag" :class="outcomeCls(t.outcome, t.pnl_pct)">{{ outcomeCN(t.outcome, t.pnl_pct) }}</span></td>
          <td class="r" :class="t.outcome === 'cancel' ? '' : (t.pnl_pct >= 0 ? 'long' : 'short')">
            <b v-if="t.outcome !== 'cancel'">{{ fmtPct(t.pnl_pct) }}</b><span v-else class="tsmall">—</span>
          </td>
          <td class="r long"><b v-if="t.max_gain">{{ fmtPct(t.max_gain) }}</b><span v-else class="tsmall">—</span></td>
          <td class="r tsmall">{{ fmtClock(t.close_time) }}</td>
        </tr>
      </tbody>
    </table>
    <PageNav :page="hist.page" :pages="hist.pages" :total="hist.total" @go="(p) => (page = p)" />

    <p v-if="state && !state.open.length && !hist.total" class="loading">{{ emptyText }}</p>
    <p v-else-if="!state" class="loading">載入中…</p>
  </div>
</template>

<style scoped>
.pendtag { margin-left: 6px; font-size: 10px; color: var(--c-gold-b); background: var(--c-gold-soft); border-radius: 6px; padding: 1px 5px; white-space: nowrap; font-family: var(--f-mono); }
.pendcount { color: var(--c-gold-b); font-weight: 600; font-size: 13px; font-family: var(--f-mono); }
.tfbadge { display: inline-block; font-size: 10px; font-weight: 700; letter-spacing: .5px; padding: 1px 6px; border-radius: 5px; background: var(--c-steel-bg); color: var(--c-steel); font-family: var(--f-mono); }
/* 進行中止盈欄(依 strategy.html mock:止盈價為主,達成該段變綠)*/
.tp-legend { font-size: 11px; color: var(--c-mut2); margin: 0 0 8px; }
.tp-cell { font-family: var(--f-mono); color: var(--c-mut); }
.tp-cell.hit { color: var(--c-up); background: var(--c-up-bg); font-weight: 600; }
/* 狀態 tag */
.stag { font-size: 11px; font-weight: 600; border-radius: 6px; padding: 2px 8px; font-family: var(--f-mono); white-space: nowrap; }
.stag.tp { background: var(--c-up-bg); color: var(--c-up); }
.stag.run { background: var(--c-surf2); color: var(--c-mut); }
.stag.pend { background: var(--c-gold-soft); color: var(--c-gold-b); }
</style>
