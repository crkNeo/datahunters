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
// 週期篩選('all'|'1h'|'4h'):進行中在前端過濾,已結束帶 &tf= 交給後端篩(分頁/統計才正確)。
const tfFilter = ref('all')
async function fetchHist() {
  if (!props.book) return
  try {
    const tfq = tfFilter.value !== 'all' ? `&tf=${tfFilter.value}` : ''
    const res = await authFetch(`/api/strat-history?book=${encodeURIComponent(props.book)}&win=${props.win || 0}&page=${page.value}&size=50${tfq}`)
    if (res.ok) hist.value = await res.json()
  } catch (e) { /* secondary */ }
}
watch(() => [props.book, props.win], () => { page.value = 1; tfFilter.value = 'all'; fetchHist() })
watch(page, fetchHist)
watch(tfFilter, () => { page.value = 1; fetchHist() })
onMounted(fetchHist)
defineExpose({ refresh: () => { page.value = 1; fetchHist() } }) // 清單/手動出場後父層可呼叫

const stats = computed(() => hist.value.stats || {})

// 進行中(open)套週期篩選;沒篩時就是全部。
const openRows = computed(() => {
  const rows = props.state?.open || []
  return tfFilter.value === 'all' ? rows : rows.filter((t) => (t.tf || '') === tfFilter.value)
})
// SMC_V2 把「待觸發掛單」(status=pending)也放進 open;它們還沒成交,不是持倉,
// 不能顯示 TP 進度/損益。以下把兩者分開計數,列上再逐筆用徽章區分。
const openFilled = computed(() => openRows.value.filter((t) => t.status !== 'pending').length)
const openPending = computed(() => openRows.value.filter((t) => t.status === 'pending').length)

// 多週期策略(如 訂單塊:1h/4h 同頁)才帶 tf 欄位;有才顯示「週期」欄與週期篩選,其餘維持原樣。
const hasTf = computed(() =>
  [...(props.state?.open || []), ...(hist.value.rows || [])].some((t) => t.tf)
)
const tfLabel = (tf) => (tf || '').toUpperCase()

// 脈衝星v3 專屬:進場 AI 參考信心分數(後端 pulsarai.go)。只有該策略、且有單已算出分數才顯示欄。
const hasAI = computed(() => props.book === 'pulsarv3' && (props.state?.open || []).some((t) => t.ai_score))
const aiCls = (s) => (s >= 70 ? 'hi' : s >= 40 ? 'mid' : 'lo')
// AI 信心浮框:掛在 .tblwrap(overflow:auto)裡的 absolute 浮框會撐大滾動區、又被裁掉。
// 改成單一浮框 Teleport 到 body + fixed 定位到 chip 下方,才能真的浮在策略上、不影響表格滾動。
const aiPop = ref({ show: false, top: 0, left: 0, score: 0, tag: '', reasons: [] })
function showAiPop(e, t) {
  const r = e.currentTarget.getBoundingClientRect()
  const w = 240
  const left = Math.min(r.left, window.innerWidth - w - 8) // 靠右邊時夾回視窗內
  aiPop.value = { show: true, top: r.bottom + 6, left: Math.max(8, left), score: t.ai_score, tag: t.ai_tag || '', reasons: t.ai_reasons || [] }
}
function hideAiPop() { aiPop.value.show = false }

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
        <div v-else-if="k === 'avg' || k === 'payoff'" class="pstat">
          <div class="stat-k">賺賠比<span class="help" tabindex="0">?<span class="help-pop">平均每次<b>賺</b>的 ÷ 平均每次<b>賠</b>的。&gt;1 代表贏的單平均比輸的單大;和勝率一起看更準(低勝率+高賺賠比也能賺)。</span></span></div>
          <div class="stat-v" :class="(stats.payoff || 0) >= 1 ? 'long' : 'short'">{{ stats.payoff >= 99.99 ? '∞' : (stats.payoff || 0).toFixed(2) }}</div>
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

    <!-- 多週期策略(訂單塊)可篩週期:進行中即時過濾、已結束重查 -->
    <div v-if="hasTf" class="tffilter">
      <button v-for="f in [['all', '全部'], ['1h', '1H'], ['4h', '4H']]" :key="f[0]" :class="{ on: tfFilter === f[0] }" @click="tfFilter = f[0]">{{ f[1] }}</button>
    </div>

    <h3 class="psub" v-if="openRows.length">進行中 ({{ openFilled }})<span v-if="openPending" class="pendcount"> · 待觸發 {{ openPending }}</span></h3>
    <p v-if="openRows.length" class="tp-legend">綠色 = 已觸及止盈</p>
    <div v-if="openRows.length" class="tblwrap">
    <table class="grid">
      <thead><tr><th>幣種</th><th v-if="hasTf">週期</th><th>方向</th><th v-if="hasAI">AI信心</th><th class="r">進場/觸發</th><th class="r">現價</th><th class="r">未實現%</th><th class="r">最大獲利</th><th class="r">止損</th><th class="r">TP1</th><th class="r">TP2</th><th class="r">最終</th><th>狀態</th><th class="r">時間</th><th v-if="canExit" class="r">操作</th></tr></thead>
      <tbody>
        <tr v-for="t in openRows" :key="t.coin + t.open_time" class="clickable" @click="$emit('coin', t.coin)">
          <td class="coin">{{ t.coin }}</td>
          <td v-if="hasTf"><span class="tfbadge">{{ tfLabel(t.tf) }}</span></td>
          <td><span class="dir" :class="t.dir === 'long' ? 'long' : 'short'">{{ t.dir === 'long' ? '做多' : '做空' }}</span></td>
          <td v-if="hasAI">
            <span v-if="t.ai_score" class="aichip" :class="aiCls(t.ai_score)" tabindex="0" @click.stop
              @mouseenter="showAiPop($event, t)" @mouseleave="hideAiPop" @focus="showAiPop($event, t)" @blur="hideAiPop">
              {{ t.ai_score }}
            </span>
            <span v-else class="tsmall">…</span>
          </td>
          <td class="r">{{ fmtPrice(t.entry) }}</td>
          <td class="r">{{ fmtPrice(t.cur) }}</td>
          <td class="r" :class="t.status === 'pending' ? '' : (t.pnl_pct >= 0 ? 'long' : 'short')">
            <b v-if="t.status !== 'pending'">{{ fmtPct(t.pnl_pct) }}</b><span v-else class="tsmall">—</span>
          </td>
          <td class="r long"><b v-if="t.status !== 'pending' && t.max_gain">{{ fmtPct(t.max_gain) }}</b><span v-else class="tsmall">—</span></td>
          <td class="r short">{{ fmtPrice(t.sl) }}<small v-if="t.status !== 'pending' && t.legs >= 2" class="vtag"> 鎖利</small><small v-else-if="t.status !== 'pending' && t.legs >= 1" class="vtag"> 保本</small></td>
          <td class="r tp-cell" :class="{ hit: t.status !== 'pending' && t.legs >= 1 }"><template v-if="t.tp1">{{ fmtPrice(t.tp1) }}<small class="tppct">{{ lvlPct(t, t.tp1) }}</small></template><template v-else>—</template></td>
          <td class="r tp-cell" :class="{ hit: t.status !== 'pending' && t.legs >= 2 }"><template v-if="t.tp2">{{ fmtPrice(t.tp2) }}<small class="tppct">{{ lvlPct(t, t.tp2) }}</small></template><template v-else>—</template></td>
          <td class="r tp-cell" :class="{ hit: t.status !== 'pending' && t.legs >= 3 }"><template v-if="t.tp">{{ fmtPrice(t.tp) }}<small class="tppct">{{ lvlPct(t, t.tp) }}</small></template><template v-else>—</template></td>
          <td><span class="stag" :class="tpStatusCls(t)">{{ tpStatus(t) }}</span></td>
          <td class="r tsmall">{{ fmtClock(t.open_time) }}</td>
          <td v-if="canExit" class="r"><button v-if="t.status !== 'pending'" class="exitbtn" @click.stop="$emit('exit', t.id)">手動出場</button><small v-else class="tsmall">—</small></td>
        </tr>
      </tbody>
    </table>
    </div>

    <h3 class="psub" v-if="hist.total">已結束 ({{ hist.total }})</h3>
    <div class="tblwrap" v-if="hist.rows.length"><table class="grid">
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
    </table></div>
    <PageNav :page="hist.page" :pages="hist.pages" :total="hist.total" @go="(p) => (page = p)" />

    <p v-if="state && !state.open.length && !hist.total" class="loading">{{ emptyText }}</p>
    <p v-else-if="!state" class="loading">載入中…</p>

    <!-- AI 信心浮框:單一實例 Teleport 到 body,fixed 定位,浮在策略上、不撐大表格滾動區 -->
    <Teleport to="body">
      <div v-if="aiPop.show" class="ai-pop" :style="{ top: aiPop.top + 'px', left: aiPop.left + 'px' }">
        <b>AI 參考信心 {{ aiPop.score }}<template v-if="aiPop.tag"> · {{ aiPop.tag }}</template></b>
        <ul v-if="aiPop.reasons.length"><li v-for="(r, i) in aiPop.reasons" :key="i">{{ r }}</li></ul>
        <em>綜合「進場品質 + 5R 目標可達性」的 AI 參考,非勝率保證</em>
      </div>
    </Teleport>
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
.tppct { display: block; font-size: 10px; font-weight: 400; color: var(--c-mut2); line-height: 1.3; }
.tp-cell.hit .tppct { color: var(--c-up); opacity: .75; }
/* 狀態 tag */
.stag { font-size: 11px; font-weight: 600; border-radius: 6px; padding: 2px 8px; font-family: var(--f-mono); white-space: nowrap; }
.stag.tp { background: var(--c-up-bg); color: var(--c-up); }
.stag.run { background: var(--c-surf2); color: var(--c-mut); }
.stag.pend { background: var(--c-gold-soft); color: var(--c-gold-b); }
/* AI 參考信心 chip(脈衝星v3)。分數色階:高綠 / 中金 / 低紅。hover/focus 顯示理由。 */
.aichip { position: relative; display: inline-block; min-width: 26px; text-align: center; font-family: var(--f-mono); font-weight: 700; font-size: 12px; border-radius: 6px; padding: 2px 7px; cursor: help; }
.aichip.hi { background: var(--c-up-bg); color: var(--c-up); }
.aichip.mid { background: var(--c-gold-soft); color: var(--c-gold-b); }
.aichip.lo { background: var(--c-dn-bg); color: var(--c-dn); }
.ai-pop { position: fixed; z-index: 60; width: 240px; text-align: left; background: var(--c-surf2); border: 1px solid var(--c-line); border-radius: 8px; padding: 8px 10px; box-shadow: 0 6px 20px rgba(0,0,0,.35); font-weight: 400; white-space: normal; }
.ai-pop b { color: var(--c-txt); font-size: 12px; }
.ai-pop ul { margin: 6px 0 4px; padding-left: 16px; color: var(--c-mut); font-size: 11.5px; line-height: 1.5; }
.ai-pop em { display: block; margin-top: 4px; color: var(--c-mut2); font-size: 10.5px; font-style: normal; }
/* 週期篩選(多週期策略:訂單塊) */
.tffilter { display: flex; gap: 6px; margin: 4px 0 12px; }
.tffilter button { font-size: 12px; color: var(--c-mut); background: var(--c-bg2); border: 1px solid var(--c-line2); border-radius: 8px; padding: 5px 15px; cursor: pointer; transition: color .12s, border-color .12s; }
.tffilter button.on { color: var(--c-gold-b); border-color: var(--c-gold-d); background: var(--c-gold-soft); }
</style>
