<!--
  CandleChart —— 幣種明細的 K 線圖(蠟燭 + 成交量),週期可切 15m / 1H / 2H / 4H。
  資料來自 /api/klines(Binance 永續,後端 30 秒快取)。純 SVG、不依賴圖表套件。
  手指 / 滑鼠在圖上移動會顯示該根的時間與開高低收;離開後回到最新一根。
-->
<script setup>
import { ref, computed, watch, onMounted } from 'vue'
import { authFetch } from '../lib/api'
import { fmtPrice } from '../lib/format'

const props = defineProps({ coin: { type: String, required: true } })

const INTERVALS = [
  { v: '15m', label: '15m' },
  { v: '1h', label: '1H' },
  { v: '2h', label: '2H' },
  { v: '4h', label: '4H' },
]
const SHOW = 60 // 顯示最近幾根
const IV_KEY = 'cm-kline-iv'

function savedInterval() {
  try {
    const v = localStorage.getItem(IV_KEY)
    if (INTERVALS.some((i) => i.v === v)) return v
  } catch (e) { /* 私密模式等讀不到就用預設 */ }
  return '1h'
}

const interval = ref(savedInterval())
const bars = ref([])
const loading = ref(false)
const failed = ref(false)
const hover = ref(-1)
let reqId = 0

async function load() {
  const id = ++reqId
  loading.value = true
  failed.value = false
  try {
    const res = await authFetch(`/api/klines?coin=${encodeURIComponent(props.coin)}&interval=${interval.value}`)
    if (!res.ok) throw new Error('HTTP ' + res.status)
    const d = await res.json()
    if (id !== reqId) return // 已切到別的幣 / 週期,丟掉舊回應
    bars.value = (Array.isArray(d) ? d : []).slice(-SHOW)
  } catch (e) {
    if (id === reqId) { bars.value = []; failed.value = true }
  } finally {
    if (id === reqId) loading.value = false
  }
}

function pick(v) {
  if (v === interval.value) return
  interval.value = v
  try { localStorage.setItem(IV_KEY, v) } catch (e) { /* ignore */ }
}

watch(() => props.coin, () => { hover.value = -1; load() })
watch(interval, () => { hover.value = -1; load() })
onMounted(load)

// ---- 版面(SVG viewBox 座標)----
const W = 600
const PH = 200 // 價格區高
const GAP = 8
const VH = 44 // 成交量區高
const H = PH + GAP + VH
const PAD = 6 // 價格區上下留白

const geo = computed(() => {
  const b = bars.value
  if (b.length < 2) return null
  let hi = -Infinity, lo = Infinity, vmax = 0, hiIdx = 0, loIdx = 0
  b.forEach((k, i) => {
    if (k.h > hi) { hi = k.h; hiIdx = i }
    if (k.l < lo) { lo = k.l; loIdx = i }
    vmax = Math.max(vmax, k.v || 0)
  })
  const span = hi - lo || hi || 1
  const step = W / b.length
  const bw = Math.max(1, step * 0.66)
  const y = (p) => PAD + (hi - p) / span * (PH - PAD * 2)
  const candles = b.map((k, i) => {
    const x = i * step + step / 2
    const up = k.c >= k.o
    const top = y(Math.max(k.o, k.c))
    const bot = y(Math.min(k.o, k.c))
    const vh = vmax > 0 ? (k.v || 0) / vmax * VH : 0
    return {
      x, up, bx: x - bw / 2, bw,
      wy1: y(k.h), wy2: y(k.l),
      by: top, bh: Math.max(1, bot - top),
      vy: H - vh, vh,
    }
  })
  const last = b[b.length - 1]
  return {
    candles, hi, lo, hiIdx, loIdx, step,
    lastY: y(last.c), lastUp: last.c >= last.o,
    pct: (p) => (y(p) / H) * 100, // 給 HTML 標籤定位用(% of 高度)
  }
})

const cur = computed(() => {
  const b = bars.value
  if (!b.length) return null
  const i = hover.value >= 0 && hover.value < b.length ? hover.value : b.length - 1
  const k = b[i]
  return { k, chg: k.o ? (k.c - k.o) / k.o * 100 : 0, isLast: i === b.length - 1 }
})

function fmtTime(t) {
  const d = new Date(t)
  const p = (n) => String(n).padStart(2, '0')
  const hm = `${p(d.getHours())}:${p(d.getMinutes())}`
  return interval.value === '15m' ? hm : `${d.getMonth() + 1}/${d.getDate()} ${hm}` // 1H 以上 60 根跨好幾天,要帶日期
}

function onMove(e) {
  const g = geo.value
  if (!g) return
  const r = e.currentTarget.getBoundingClientRect()
  if (r.width <= 0) return
  const i = Math.floor(((e.clientX - r.left) / r.width) * bars.value.length)
  hover.value = Math.min(bars.value.length - 1, Math.max(0, i))
}
</script>

<template>
  <div class="kc">
    <div class="kc-top">
      <div class="kc-iv">
        <button v-for="i in INTERVALS" :key="i.v" :class="{ on: interval === i.v }" @click="pick(i.v)">{{ i.label }}</button>
      </div>
      <div v-if="cur" class="kc-ohlc">
        <span class="kc-t">{{ fmtTime(cur.k.t) }}</span>
        <span>開 <b>{{ fmtPrice(cur.k.o) }}</b></span>
        <span>高 <b>{{ fmtPrice(cur.k.h) }}</b></span>
        <span>低 <b>{{ fmtPrice(cur.k.l) }}</b></span>
        <span>收 <b :class="cur.k.c >= cur.k.o ? 'up' : 'dn'">{{ fmtPrice(cur.k.c) }}</b></span>
        <span :class="cur.chg >= 0 ? 'up' : 'dn'">{{ cur.chg >= 0 ? '+' : '' }}{{ cur.chg.toFixed(2) }}%</span>
      </div>
    </div>

    <div class="kc-box">
      <p v-if="loading && !geo" class="kc-msg">K 線載入中…</p>
      <p v-else-if="failed" class="kc-msg">K 線載入失敗,請稍後再試</p>
      <p v-else-if="!geo" class="kc-msg">沒有 K 線資料</p>
      <template v-else>
        <svg class="kc-svg" :viewBox="`0 0 ${W} ${H}`" preserveAspectRatio="none"
          @pointermove="onMove" @pointerdown="onMove" @pointerleave="hover = -1">
          <line v-for="f in [0.25, 0.5, 0.75]" :key="f" class="kc-grid" x1="0" :x2="W" :y1="PH * f" :y2="PH * f" />
          <line class="kc-last" :class="geo.lastUp ? 'up' : 'dn'" x1="0" :x2="W" :y1="geo.lastY" :y2="geo.lastY" />
          <g v-for="(c, i) in geo.candles" :key="i" :class="c.up ? 'up' : 'dn'">
            <rect class="kc-vol" :x="c.bx" :y="c.vy" :width="c.bw" :height="c.vh" />
            <line class="kc-wick" :x1="c.x" :x2="c.x" :y1="c.wy1" :y2="c.wy2" />
            <rect class="kc-body" :x="c.bx" :y="c.by" :width="c.bw" :height="c.bh" />
          </g>
          <line v-if="hover >= 0 && geo.candles[hover]" class="kc-cross" :x1="geo.candles[hover].x" :x2="geo.candles[hover].x" y1="0" :y2="H" />
        </svg>
        <!-- 價格標籤用 HTML 疊上去,避免 SVG 拉伸讓字變形 -->
        <span class="kc-lbl kc-hi" :style="{ top: geo.pct(geo.hi) + '%' }">{{ fmtPrice(geo.hi) }}</span>
        <span class="kc-lbl kc-lo" :style="{ top: geo.pct(geo.lo) + '%' }">{{ fmtPrice(geo.lo) }}</span>
        <span class="kc-lbl kc-px" :class="geo.lastUp ? 'up' : 'dn'" :style="{ top: (geo.lastY / H * 100) + '%' }">{{ fmtPrice(bars[bars.length - 1].c) }}</span>
      </template>
    </div>
  </div>
</template>

<style scoped>
.kc { margin-bottom: 16px; }
.kc-top { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; margin-bottom: 8px; }
.kc-iv { display: inline-flex; background: var(--c-bg2); border: 1px solid var(--c-line); border-radius: 8px; padding: 2px; }
.kc-iv button { background: none; border: 0; color: var(--c-mut); font-family: var(--f-mono); font-size: 12px; font-weight: 600; padding: 4px 10px; border-radius: 6px; cursor: pointer; }
.kc-iv button.on { background: var(--c-surf2); color: var(--c-gold); }
.kc-ohlc { display: flex; flex-wrap: wrap; gap: 2px 10px; font-family: var(--f-mono); font-size: 11px; color: var(--c-mut); }
.kc-ohlc b { color: var(--c-txt); font-weight: 600; }
.kc-t { color: var(--c-txt); }
.up { color: var(--c-up); }
.dn { color: var(--c-dn); }

.kc-box { position: relative; height: 230px; background: var(--c-bg2); border: 1px solid var(--c-line); border-radius: 10px; overflow: hidden; }
.kc-svg { display: block; width: calc(100% - 64px); height: 100%; touch-action: pan-y; cursor: crosshair; }
.kc-msg { margin: 0; height: 100%; display: flex; align-items: center; justify-content: center; color: var(--c-mut); font-size: 13px; }

.kc-grid { stroke: var(--c-line); stroke-width: 1; vector-effect: non-scaling-stroke; }
.kc-last { stroke-width: 1; stroke-dasharray: 3 3; vector-effect: non-scaling-stroke; opacity: .7; }
.kc-last.up { stroke: var(--c-up); }
.kc-last.dn { stroke: var(--c-dn); }
.kc-cross { stroke: var(--c-mut); stroke-width: 1; stroke-dasharray: 2 3; vector-effect: non-scaling-stroke; }
.kc-wick { stroke-width: 1; vector-effect: non-scaling-stroke; }
g.up .kc-wick { stroke: var(--c-up); }
g.dn .kc-wick { stroke: var(--c-dn); }
g.up .kc-body { fill: var(--c-up); }
g.dn .kc-body { fill: var(--c-dn); }
g.up .kc-vol { fill: var(--c-up); opacity: .28; }
g.dn .kc-vol { fill: var(--c-dn); opacity: .28; }

/* 右側價格軸標籤(相對整個圖框高度定位) */
.kc-lbl { position: absolute; right: 4px; transform: translateY(-50%); font-family: var(--f-mono); font-size: 10px; line-height: 1; padding: 2px 4px; border-radius: 4px; color: var(--c-mut); background: var(--c-bg2); white-space: nowrap; pointer-events: none; }
.kc-px { color: #0a0b0e; font-weight: 700; }
.kc-px.up { background: var(--c-up); }
.kc-px.dn { background: var(--c-dn); }
</style>
