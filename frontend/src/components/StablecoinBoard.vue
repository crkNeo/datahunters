<!--
  穩定幣資金流(公開)。自己抓 /api/stablecoins(後端 DefiLlama,免費無 key)。
  淨增發=場外資金進場買力;淨縮=資金撤離。呈現總供給 + 日/週/月變化 + 30日趨勢 + 前幾大穩定幣。
-->
<script setup>
import { ref, computed, onMounted, onUnmounted } from "vue"
import { authFetch } from "../lib/api"

const sc = ref(null)
async function load() {
  try {
    const res = await authFetch("/api/stablecoins")
    if (res.ok) sc.value = await res.json()
  } catch (e) { /* secondary */ }
}
const hasData = computed(() => sc.value && sc.value.total > 0)
// 金額格式:十億以上用 B、其餘用 M
function fmtBig(v) { return "$" + (v / 1e9).toFixed(1) + "B" }
function fmtChg(v) {
  const s = v >= 0 ? "+" : "−", a = Math.abs(v)
  return s + "$" + (a >= 1e9 ? (a / 1e9).toFixed(2) + "B" : (a / 1e6).toFixed(0) + "M")
}
// 30 日總供給趨勢 sparkline(min/max 縮放,讓每日小變化看得出來)
const W = 600, H = 72
const spark = computed(() => {
  const h = sc.value?.history || []
  if (h.length < 2) return null
  const ys = h.map((p) => p.mcap)
  const lo = Math.min(...ys), hi = Math.max(...ys), span = hi - lo || 1
  const pts = h.map((p, i) => [i / (h.length - 1) * W, H - ((p.mcap - lo) / span) * (H - 10) - 5])
  const line = pts.map((p, i) => (i ? "L" : "M") + p[0].toFixed(1) + " " + p[1].toFixed(1)).join(" ")
  const area = "M" + pts[0][0].toFixed(1) + " " + H + " " + pts.map((p) => "L" + p[0].toFixed(1) + " " + p[1].toFixed(1)).join(" ") + " L" + W + " " + H + " Z"
  return { line, area, up: ys[ys.length - 1] >= ys[0] }
})

let timer = null
onMounted(() => { load(); timer = setInterval(load, 10 * 60 * 1000) })
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <section class="sc-wrap">
    <div class="mk-head">
      <h2>穩定幣資金流<span class="help" tabindex="0">?<span class="help-pop">全市場穩定幣(USDT/USDC…)<b>總供給</b>(資料來源 DefiLlama)。穩定幣是加密市場的「場外現金」:<b>淨增發</b>=新資金進場、買盤子彈變多(偏多的資金後盾);<b>淨縮水</b>=資金撤離、需求轉弱。看日/週/月變化與趨勢判斷資金是流入還是流出。⚠️ 僅供參考,非投資建議。</span></span></h2>
      <span class="mk-count" v-if="sc">來源 DefiLlama · 全鏈合計</span>
    </div>

    <div v-if="hasData" class="sc-card">
      <div class="sc-top">
        <div class="sc-total">
          <span class="sc-k">穩定幣總供給</span>
          <span class="sc-v">{{ fmtBig(sc.total) }}</span>
        </div>
        <div class="sc-chgs">
          <span class="sc-chg"><i>日</i><b :class="sc.day_chg >= 0 ? 'up' : 'dn'">{{ fmtChg(sc.day_chg) }}</b></span>
          <span class="sc-chg"><i>週</i><b :class="sc.week_chg >= 0 ? 'up' : 'dn'">{{ fmtChg(sc.week_chg) }}</b></span>
          <span class="sc-chg"><i>月</i><b :class="sc.month_chg >= 0 ? 'up' : 'dn'">{{ fmtChg(sc.month_chg) }}</b></span>
        </div>
      </div>

      <svg v-if="spark" class="sc-spark" :viewBox="`0 0 ${W} ${H}`" preserveAspectRatio="none">
        <path :d="spark.area" :class="spark.up ? 'area-up' : 'area-dn'" />
        <path :d="spark.line" fill="none" :class="spark.up ? 'line-up' : 'line-dn'" stroke-width="2" vector-effect="non-scaling-stroke" />
      </svg>
      <div class="sc-axis"><span>30 日前</span><span>近 30 日總供給趨勢</span><span>今日</span></div>

      <div v-if="sc.coins && sc.coins.length" class="sc-coins">
        <div v-for="c in sc.coins" :key="c.symbol" class="sc-coinrow">
          <span class="sc-sym">{{ c.symbol }}</span>
          <span class="sc-mcap">{{ fmtBig(c.mcap) }}</span>
          <span class="sc-day" :class="c.day_chg >= 0 ? 'up' : 'dn'">日 {{ fmtChg(c.day_chg) }}</span>
          <span class="sc-week" :class="c.week_chg >= 0 ? 'up' : 'dn'">週 {{ fmtChg(c.week_chg) }}</span>
        </div>
      </div>
    </div>
    <p v-else class="loading">載入穩定幣資金流中…(資料來源 DefiLlama)</p>
  </section>
</template>

<style scoped>
.sc-wrap { margin-bottom: 20px; }
.sc-card { background: var(--c-surf); border: 1px solid var(--c-line); border-radius: var(--r-lg); padding: 16px 18px; }
.sc-top { display: flex; align-items: center; justify-content: space-between; gap: 16px; flex-wrap: wrap; }
.sc-total { display: flex; flex-direction: column; gap: 2px; }
.sc-k { font-size: 12px; color: var(--c-mut); }
.sc-v { font-family: var(--f-mono); font-weight: 800; font-size: 28px; color: var(--c-gold); }
.sc-chgs { display: flex; gap: 20px; }
.sc-chg { display: flex; flex-direction: column; align-items: flex-end; gap: 2px; }
.sc-chg i { font-style: normal; font-size: 11px; color: var(--c-mut2); }
.sc-chg b { font-family: var(--f-mono); font-size: 15px; }
.up { color: var(--c-up); } .dn { color: var(--c-dn); }
.sc-spark { width: 100%; height: 72px; margin: 14px 0 4px; display: block; }
.line-up { stroke: var(--c-up); } .line-dn { stroke: var(--c-dn); }
.area-up { fill: rgba(55,214,138,.12); } .area-dn { fill: rgba(255,92,108,.12); }
.sc-axis { display: flex; justify-content: space-between; font-size: 10.5px; color: var(--c-mut2); font-family: var(--f-mono); }
.sc-coins { margin-top: 14px; border-top: 1px solid var(--c-line); padding-top: 10px; display: flex; flex-direction: column; gap: 2px; }
.sc-coinrow { display: grid; grid-template-columns: 1fr auto auto auto; gap: 12px; align-items: center; padding: 6px 4px; border-bottom: 1px solid var(--c-line); font-family: var(--f-mono); font-size: 12.5px; }
.sc-coinrow:last-child { border-bottom: 0; }
.sc-sym { font-family: var(--f-disp); font-weight: 700; color: var(--c-txt); }
.sc-mcap { color: var(--c-txt); }
.sc-day, .sc-week { font-size: 11.5px; min-width: 92px; text-align: right; }
@media (max-width: 640px) {
  .sc-coinrow { grid-template-columns: 1fr auto; row-gap: 2px; }
  .sc-mcap { text-align: right; }
  .sc-day, .sc-week { grid-column: span 1; }
}
</style>
