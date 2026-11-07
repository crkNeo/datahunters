<!--
  型態掃描:選一種型態 + 週期(1H / 4H / 1D)→ 列出成交額前 200 幣裡,目前走勢跟它最像的幣(相似度降冪)。後端 patterns.go。
  資料:GET /api/patterns?key=<型態>&tf=<1h|4h|1d> → { tf, scanned, updated, summary: {型態: [[相似度, 剛突破?1:0], ...]}, rows: [...] }
  每列:coin, score, state('forming'|'breakout'), bars, breakout, dist_pct, target, parts[{name,score,note}],
        chart{ candles:[[開,高,低,收]], pivots:[{i,p,hi}], lines:[{i1,p1,i2,p2}], level }
-->
<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { authFetch } from '../lib/api'
import { fmtPrice, fmtClock } from '../lib/format'

// 16 種型態:名稱、偏多 / 偏空、示意圖(0~100 座標的折線,y 往下)
const PATTERNS = [
  { key: 'sym_tri_bull', name: '看漲對稱三角', bias: 'bull', pts: [0, 90, 22, 45, 32, 72, 44, 50, 54, 66, 64, 54, 72, 60, 78, 56, 100, 18] },
  { key: 'sym_tri_bear', name: '看跌對稱三角', bias: 'bear', pts: [0, 10, 22, 55, 32, 28, 44, 50, 54, 34, 64, 46, 72, 40, 78, 44, 100, 82] },
  { key: 'dbl_bottom', name: '雙重底', bias: 'bull', pts: [0, 15, 22, 80, 38, 45, 54, 80, 70, 45, 100, 10] },
  { key: 'dbl_top', name: '雙重頂', bias: 'bear', pts: [0, 85, 22, 20, 38, 55, 54, 20, 70, 55, 100, 90] },
  { key: 'bull_flag', name: '牛旗', bias: 'bull', pts: [0, 95, 30, 25, 40, 40, 46, 30, 54, 46, 60, 36, 68, 52, 74, 42, 100, 8] },
  { key: 'bear_flag', name: '熊旗', bias: 'bear', pts: [0, 5, 30, 75, 40, 60, 46, 70, 54, 54, 60, 64, 68, 48, 74, 58, 100, 92] },
  { key: 'fall_wedge', name: '看漲下降模型', bias: 'bull', pts: [0, 10, 14, 60, 26, 30, 40, 68, 52, 48, 64, 72, 74, 62, 80, 70, 100, 25] },
  { key: 'rise_wedge', name: '看跌上升模型', bias: 'bear', pts: [0, 90, 14, 40, 26, 70, 40, 32, 52, 52, 64, 28, 74, 38, 80, 30, 100, 75] },
  { key: 'tri_bottom', name: '三重底', bias: 'bull', pts: [0, 20, 15, 80, 27, 45, 40, 80, 52, 45, 65, 80, 77, 45, 100, 10] },
  { key: 'tri_top', name: '三重頂', bias: 'bear', pts: [0, 80, 15, 20, 27, 55, 40, 20, 52, 55, 65, 20, 77, 55, 100, 90] },
  { key: 'hs_bottom', name: '頭肩底', bias: 'bull', pts: [0, 20, 14, 62, 24, 45, 38, 85, 52, 45, 64, 62, 74, 45, 100, 10] },
  { key: 'hs_top', name: '頭肩頂', bias: 'bear', pts: [0, 80, 14, 38, 24, 55, 38, 15, 52, 55, 64, 38, 74, 55, 100, 90] },
  { key: 'cup_bull', name: '看漲杯柄', bias: 'bull', pts: [0, 30, 8, 50, 16, 65, 26, 75, 36, 78, 46, 75, 56, 65, 64, 50, 70, 32, 76, 42, 82, 36, 86, 40, 100, 10] },
  { key: 'cup_bear', name: '看跌杯柄', bias: 'bear', pts: [0, 70, 8, 50, 16, 35, 26, 25, 36, 22, 46, 25, 56, 35, 64, 50, 70, 68, 76, 58, 82, 64, 86, 60, 100, 90] },
  { key: 'round_bottom', name: '圓弧底', bias: 'bull', pts: [0, 15, 12, 45, 25, 68, 40, 80, 55, 80, 70, 68, 82, 45, 92, 25, 100, 12] },
  { key: 'round_top', name: '圓弧頂', bias: 'bear', pts: [0, 85, 12, 55, 25, 32, 40, 20, 55, 20, 70, 32, 82, 55, 92, 75, 100, 88] },
]
const iconPath = (pts) => pts.reduce((s, v, i) => s + (i % 2 ? `,${v}` : `${i ? ' L' : 'M'}${v}`), '')

const data = ref(null)
const loading = ref(false)
const sel = ref('sym_tri_bull')
const TFS = [['1h', '1H'], ['4h', '4H'], ['1d', '1D']]
const tf = ref('1h')
const tfName = computed(() => TFS.find((t) => t[0] === tf.value)[1])
const stateF = ref('all') // all | forming | breakout
const minScore = ref(60)
const open = ref(null) // 展開的那一列(幣)

async function load() {
  loading.value = true
  try {
    const want = [sel.value, tf.value]
    const r = await authFetch(`/api/patterns?key=${want[0]}&tf=${want[1]}`)
    if (r.ok && sel.value === want[0] && tf.value === want[1]) data.value = await r.json() // 切太快時丟掉舊回應
  } catch (e) {
    /* 下次再試 */
  } finally {
    loading.value = false
  }
}
let timer = null
onMounted(() => {
  load()
  timer = setInterval(load, 5 * 60e3) // 後端每根收盤後重算;5 分鐘拉一次就夠
})
onUnmounted(() => clearInterval(timer))
watch([sel, tf], () => {
  open.value = null
  load()
})

const keep = (score, brk) => score >= minScore.value && (stateF.value === 'all' || (stateF.value === 'breakout') === !!brk)
// 每張型態卡右上角:目前有幾檔符合門檻 / 狀態篩選
const countOf = (key) => (data.value?.summary?.[key] || []).filter((x) => keep(x[0], x[1])).length
const cur = computed(() => PATTERNS.find((p) => p.key === sel.value))
const rows = computed(() => (data.value?.key === sel.value && data.value?.tf === tf.value ? data.value.rows || [] : [])
  .filter((r) => keep(r.score, r.state === 'breakout')))
// 轉折點標籤:高點 H1、H2…,低點 L1、L2…
const pivLabels = (ps) => {
  let h = 0, l = 0
  return ps.map((p) => (p.hi ? 'H' + ++h : 'L' + ++l))
}
const scoreCls = (s) => (s >= 80 ? 'hi' : s >= 70 ? 'mid' : 'lo')

// 小圖:K 棒 + 轉折點 + 趨勢線 + 突破價
function chartGeo(c, w, h) {
  const n = c.candles.length
  let hi = -Infinity, lo = Infinity
  for (const k of c.candles) { hi = Math.max(hi, k[1]); lo = Math.min(lo, k[2]) }
  if (c.level) { hi = Math.max(hi, c.level); lo = Math.min(lo, c.level) }
  const pad = (hi - lo) * 0.08
  hi += pad; lo -= pad
  const x = (i) => ((i + 0.5) / n) * w
  const y = (p) => h - ((p - lo) / (hi - lo)) * h
  return { n, x, y, bw: Math.max(1, (w / n) * 0.6) }
}
</script>

<template>
  <section class="pb">
    <div class="mk-head">
      <h2>型態掃描<span class="help" tabindex="0">?<span class="help-pop">每根 K 收盤後，對成交額前 200 的幣把 16 種型態全部算一次。<b>1H</b> 整點收盤後馬上算；<b>4H</b> 收盤後約 5～9 分鐘、<b>1D</b>（台北早上 8 點換日）約 10～14 分鐘陸續更新完（錯開時間、慢慢抓，避免撞到交易所頻率限制）。1D 要至少 80 根日 K，上市太新的幣不會出現。<b>相似度</b> = 走勢跟教科書型態有多像（轉折點、趨勢線、前段趨勢、貼合度各項加權），<b>不是勝率</b>。點一列可以看小圖和各項分數。</span></span></h2>
      <span class="mk-count" v-if="data">掃描 {{ data.scanned }} 檔 · {{ tfName }} · {{ data.updated ? '更新 ' + fmtClock(data.updated) : '第一次掃描中…' }}</span>
    </div>

    <!-- 型態選擇:16 張小卡,示意圖 + 名稱 + 目前有幾檔超過門檻 -->
    <div class="pgrid">
      <button v-for="p in PATTERNS" :key="p.key" class="pcard" :class="[p.bias, { on: sel === p.key }]" @click="sel = p.key">
        <svg viewBox="-4 -4 108 108" class="picon"><path :d="iconPath(p.pts)" /></svg>
        <span class="pname">{{ p.name }}</span>
        <span class="pcnt" v-if="data">{{ countOf(p.key) }}</span>
      </button>
    </div>

    <div class="timefilter">
      <span class="tf-label">週期</span>
      <button v-for="t in TFS" :key="t[0]" :class="{ on: tf === t[0] }" @click="tf = t[0]">{{ t[1] }}</button>
      <span class="tf-label" style="margin-left:14px">狀態</span>
      <button v-for="s in [['all', '全部'], ['forming', '形成中'], ['breakout', '剛突破']]" :key="s[0]" :class="{ on: stateF === s[0] }" @click="stateF = s[0]">{{ s[1] }}</button>
      <span class="tf-label" style="margin-left:14px">相似度</span>
      <button v-for="m in [50, 60, 70, 80]" :key="m" :class="{ on: minScore === m }" @click="minScore = m">≥ {{ m }}%</button>
    </div>

    <h3 class="psub">{{ cur.name }} <small class="tsmall">相似度由高到低 · {{ rows.length }} 檔</small></h3>
    <div class="tblwrap">
      <table class="grid">
        <thead><tr><th>#</th><th>幣種</th><th>相似度</th><th>狀態</th><th class="r" title="型態跨幾根 K">長度</th><th class="r">突破價</th><th class="r" title="現價離突破價">距突破</th><th class="r" title="等幅目標:型態高度從突破價往上 / 下量">目標價</th><th>走勢(轉折點 + 趨勢線)</th></tr></thead>
        <tbody>
          <template v-for="(r, i) in rows" :key="r.coin">
            <tr class="clickable" :class="{ sel: open === r.coin }" @click="open = open === r.coin ? null : r.coin">
              <td class="tsmall">{{ i + 1 }}</td>
              <td class="coin">{{ r.coin }}</td>
              <td><div class="sbar"><i :class="scoreCls(r.score)" :style="{ width: r.score + '%' }"></i></div><b class="sval" :class="scoreCls(r.score)">{{ r.score }}%</b></td>
              <td><span class="chip" :class="r.state === 'breakout' ? (cur.bias === 'bull' ? 'long' : 'short') : 'pend'">{{ r.state === 'breakout' ? '剛突破' : '形成中' }}</span></td>
              <td class="r tsmall">{{ r.bars }} 根</td>
              <td class="r">{{ fmtPrice(r.breakout) }}</td>
              <td class="r" :class="r.state === 'breakout' ? (cur.bias === 'bull' ? 'long' : 'short') : ''">{{ r.dist_pct > 0 ? '+' : '' }}{{ r.dist_pct.toFixed(2) }}%</td>
              <td class="r" :class="cur.bias === 'bull' ? 'long' : 'short'">{{ fmtPrice(r.target) }}</td>
              <td>
                <svg :viewBox="`0 0 220 54`" class="mini">
                  <template v-for="g in [chartGeo(r.chart, 220, 54)]" :key="'g'">
                    <line v-if="r.chart.level" :x1="0" :x2="220" :y1="g.y(r.chart.level)" :y2="g.y(r.chart.level)" class="lv" />
                    <g v-for="(k, j) in r.chart.candles" :key="j" :class="k[3] >= k[0] ? 'up' : 'dn'">
                      <line :x1="g.x(j)" :x2="g.x(j)" :y1="g.y(k[1])" :y2="g.y(k[2])" />
                      <rect :x="g.x(j) - g.bw / 2" :width="g.bw" :y="g.y(Math.max(k[0], k[3]))" :height="Math.max(0.8, Math.abs(g.y(k[0]) - g.y(k[3])))" />
                    </g>
                    <line v-for="(l, j) in r.chart.lines" :key="'l' + j" :x1="g.x(l.i1)" :y1="g.y(l.p1)" :x2="g.x(l.i2)" :y2="g.y(l.p2)" class="tl" />
                  </template>
                </svg>
              </td>
            </tr>
            <tr v-if="open === r.coin" class="detail">
              <td colspan="9">
                <div class="dwrap">
                  <svg viewBox="0 0 640 220" class="big">
                    <template v-for="g in [chartGeo(r.chart, 640, 220)]" :key="'G'">
                      <line v-if="r.chart.level" x1="0" x2="640" :y1="g.y(r.chart.level)" :y2="g.y(r.chart.level)" class="lv" />
                      <text v-if="r.chart.level" x="4" :y="g.y(r.chart.level) - 4" class="lvt">突破價 {{ fmtPrice(r.chart.level) }}</text>
                      <g v-for="(k, j) in r.chart.candles" :key="j" :class="k[3] >= k[0] ? 'up' : 'dn'">
                        <line :x1="g.x(j)" :x2="g.x(j)" :y1="g.y(k[1])" :y2="g.y(k[2])" />
                        <rect :x="g.x(j) - g.bw / 2" :width="g.bw" :y="g.y(Math.max(k[0], k[3]))" :height="Math.max(1, Math.abs(g.y(k[0]) - g.y(k[3])))" />
                      </g>
                      <line v-for="(l, j) in r.chart.lines" :key="'L' + j" :x1="g.x(l.i1)" :y1="g.y(l.p1)" :x2="g.x(l.i2)" :y2="g.y(l.p2)" class="tl" />
                      <g v-for="(p, j) in r.chart.pivots" :key="'p' + j">
                        <circle :cx="g.x(p.i)" :cy="g.y(p.p)" r="3.5" :class="p.hi ? 'ph' : 'pl'" />
                        <text :x="g.x(p.i)" :y="p.hi ? g.y(p.p) - 7 : g.y(p.p) + 14" class="pt">{{ pivLabels(r.chart.pivots)[j] }}</text>
                      </g>
                    </template>
                  </svg>
                  <div class="parts">
                    <div class="ptitle">相似度 {{ r.score }}% 怎麼來的</div>
                    <div v-for="p in r.parts" :key="p.name" class="prow">
                      <span class="pn">{{ p.name }}</span>
                      <div class="sbar sm"><i :class="scoreCls(p.score)" :style="{ width: p.score + '%' }"></i></div>
                      <b :class="scoreCls(p.score)">{{ p.score }}</b>
                      <span class="tsmall pnote">{{ p.note }}</span>
                    </div>
                  </div>
                </div>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
    </div>
    <p v-if="data && !rows.length && !loading" class="empty">{{ data.updated ? `目前沒有相似度 ≥ ${minScore}% 的幣。` : (tf === '1h' ? '剛開機,第一次掃描約需 1~2 分鐘…' : '剛開機,第一次掃描約需 5 分鐘左右(收盤後的前幾分鐘會先等一下)…') }}</p>
    <p v-else-if="!data" class="loading">載入中…</p>
    <p class="loginhint" style="margin-top:12px">相似度只代表「長得多像教科書型態」，不是勝率。之後會記錄每次掃到的型態後續漲跌，再看哪些型態真的有用。</p>
  </section>
</template>

<style scoped>
.pgrid { display: grid; grid-template-columns: repeat(8, 1fr); gap: 8px; margin: 12px 0 6px; }
@media (max-width: 1100px) { .pgrid { grid-template-columns: repeat(4, 1fr); } }
@media (max-width: 520px) {
  .pgrid { grid-template-columns: repeat(4, 1fr); gap: 5px; }
  .pcard { padding: 6px 2px 4px; font-size: 10.5px; }
  .picon { width: 40px; height: 26px; }
  .pname { white-space: normal; text-align: center; line-height: 1.2; }
}
.pcard { position: relative; display: flex; flex-direction: column; align-items: center; gap: 4px; padding: 8px 4px 6px; background: #16181d; border: 1px solid #23262d; border-radius: 10px; cursor: pointer; color: #c8cdd6; font-size: 12px; }
.pcard:hover { border-color: #3a3f48; }
.pcard.on { background: #1b2942; border-color: #5b8def; color: #cfe0ff; }
.picon { width: 54px; height: 38px; }
.picon path { fill: none; stroke-width: 6; stroke-linejoin: round; stroke-linecap: round; }
.pcard.bull .picon path { stroke: #2ec26b; }
.pcard.bear .picon path { stroke: #ff5c5c; }
.pname { white-space: nowrap; }
.pcnt { position: absolute; top: 4px; right: 6px; font-size: 10px; color: #e0b341; font-family: var(--f-mono); }
.sbar { display: inline-block; width: 70px; height: 6px; background: #23262d; border-radius: 3px; overflow: hidden; vertical-align: middle; margin-right: 6px; }
.sbar.sm { width: 90px; }
.sbar i { display: block; height: 100%; }
.sbar i.hi, .sval.hi, b.hi { background: none; }
.sbar i.hi { background: #2ec26b; } .sbar i.mid { background: #e0b341; } .sbar i.lo { background: #8b909a; }
b.hi, .sval.hi { color: #2ec26b; } b.mid, .sval.mid { color: #e0b341; } b.lo, .sval.lo { color: #8b909a; }
.chip.pend { background: #2a2410; color: #e0b341; }
.mini { width: 220px; height: 54px; display: block; }
.big { width: 100%; max-width: 640px; height: auto; display: block; background: #0f1115; border-radius: 8px; }
.up line, .up rect { stroke: #2ec26b; fill: #2ec26b; }
.dn line, .dn rect { stroke: #ff5c5c; fill: #ff5c5c; }
.up line, .dn line { stroke-width: 1; }
.tl { stroke: #5b8def; stroke-width: 1.5; stroke-dasharray: 5 3; }
.lv { stroke: #e0b341; stroke-width: 1; stroke-dasharray: 2 3; }
.lvt { fill: #e0b341; font-size: 11px; }
.ph { fill: #ff5c5c; } .pl { fill: #2ec26b; }
.pt { fill: #c8cdd6; font-size: 10px; text-anchor: middle; }
tr.sel td { background: #161c27; }
tr.detail td { background: #12151b; }
.dwrap { display: flex; gap: 18px; flex-wrap: wrap; padding: 8px 4px; }
.parts { flex: 1; min-width: 260px; }
.ptitle { font-weight: 700; margin-bottom: 8px; }
.prow { display: grid; grid-template-columns: 110px 96px 30px 1fr; align-items: center; gap: 6px; margin-bottom: 6px; font-size: 13px; }
.pnote { white-space: normal; }
</style>
