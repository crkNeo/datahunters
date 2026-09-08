<!--
  清算看板(公開)。自己抓 /api/liquidations。
-->
<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from "vue"
import { authFetch } from "../lib/api"
import { fmtNum, fmtPrice } from "../lib/format"

const emit = defineEmits(["coin"])
const liquidations = ref(null)
async function load() {
  try {
    const lq = await authFetch("/api/liquidations")
    if (lq.ok) liquidations.value = await lq.json()
  } catch (e) { /* secondary */ }
}
function liqClock(ms) {
  return new Date(ms).toLocaleTimeString("zh-TW", { hour: "2-digit", minute: "2-digit", hour12: false })
}

// 清算熱區:把某幣近期清算依「價位」分桶,看哪些價位發生多/空清算。
// 誠實版:呈現「近期實際清算」的價位分布(非 OI 槓桿預測的未來磁吸位)。
const liqCoin = ref("")
const liqCoins = computed(() => {
  if (!liquidations.value) return []
  const m = {}
  for (const r of liquidations.value.recent) m[r.coin] = (m[r.coin] || 0) + r.usd
  return Object.keys(m).sort((a, b) => m[b] - m[a]) // 清算量大→小
})
watch(liqCoins, (cs) => { if (cs.length && !cs.includes(liqCoin.value)) liqCoin.value = cs[0] }, { immediate: true })
const liqHeat = computed(() => {
  if (!liquidations.value || !liqCoin.value) return null
  const evs = liquidations.value.recent.filter((r) => r.coin === liqCoin.value && r.px > 0)
  if (evs.length < 3) return null
  const ps = evs.map((e) => e.px)
  const lo = Math.min(...ps), hi = Math.max(...ps)
  if (hi <= lo) return null
  const N = 12
  const b = Array.from({ length: N }, (_, i) => ({ lo: lo + (hi - lo) * i / N, hi: lo + (hi - lo) * (i + 1) / N, long: 0, short: 0 }))
  for (const e of evs) {
    let i = Math.floor((e.px - lo) / (hi - lo) * N)
    if (i >= N) i = N - 1; if (i < 0) i = 0
    if (e.side === "long") b[i].long += e.usd; else b[i].short += e.usd
  }
  const max = Math.max(...b.map((x) => x.long + x.short), 1)
  return { buckets: b.reverse(), max } // 高價在上
})
let timer = null
onMounted(() => { load(); timer = setInterval(load, 30000) })
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <section>
  <div class="mk-head">
    <h2>清算<span class="help" tabindex="0">?<span class="help-pop">即時清算事件(OKX 永續)。<b>即時監控、非回測訊號</b>;持續累積,日後可驗證是否領先。多單被洗=下殺、空單被軋=上拉。</span></span></h2>
  </div>

  <!-- liquidation summary + feed -->
  <div v-if="liquidations" class="liqsum">
    <div class="liqbox short"><div class="stat-k">近 1h 多單爆倉</div><div class="stat-v short">${{ (liquidations.long_usd_1h / 1e6).toFixed(2) }}M</div></div>
    <div class="liqbox long"><div class="stat-k">近 1h 空單爆倉</div><div class="stat-v long">${{ (liquidations.short_usd_1h / 1e6).toFixed(2) }}M</div></div>
    <div class="liqbox"><div class="stat-k">偏向</div><div class="stat-v" :class="liquidations.long_usd_1h > liquidations.short_usd_1h ? 'short' : 'long'">{{ liquidations.long_usd_1h > liquidations.short_usd_1h ? '多單被洗(下殺)' : '空單被軋(上拉)' }}</div></div>
  </div>

  <!-- 清算熱區:近期清算依價位分布(多單被清=紅、空單被清=綠)-->
  <div v-if="liqHeat" class="liqheat">
    <div class="lh-head">
      <span class="lh-title">清算熱區<span class="help" tabindex="0">?<span class="help-pop">把該幣<b>近期實際發生的清算</b>依「價位」分桶,看哪些價位帶被清算最多。<b>紅</b>=多單被清(下殺打到)、<b>綠</b>=空單被清(上拉軋到)。這是近期已發生的分布,<b>非</b>用未平倉量預測的未來磁吸價位。</span></span></span>
      <select v-model="liqCoin" class="lh-sel"><option v-for="c in liqCoins" :key="c" :value="c">{{ c }}</option></select>
      <span class="lh-legend"><i class="lh-dot long"></i>多單被清 <i class="lh-dot short"></i>空單被清</span>
    </div>
    <div class="lh-rows">
      <div v-for="(b, i) in liqHeat.buckets" :key="i" class="lh-row">
        <span class="lh-px">{{ fmtPrice(b.hi) }}</span>
        <span class="lh-bar">
          <i class="lh-long" :style="{ width: (b.long / liqHeat.max * 100) + '%' }"></i>
          <i class="lh-short" :style="{ width: (b.short / liqHeat.max * 100) + '%' }"></i>
        </span>
        <span class="lh-amt">{{ (b.long + b.short) > 0 ? '$' + ((b.long + b.short) / 1e6).toFixed(2) + 'M' : '' }}</span>
      </div>
    </div>
  </div>

  <h3 class="psub" v-if="liquidations && liquidations.recent.length">近期清算事件 ({{ liquidations.recent.length }})</h3>
  <table v-if="liquidations && liquidations.recent.length" class="grid">
    <thead><tr><th>時間</th><th>幣種</th><th>被清算</th><th class="r">金額</th><th class="r">價格</th></tr></thead>
    <tbody>
      <tr v-for="(r, i) in liquidations.recent" :key="i" class="clickable" @click="$emit('coin', r.coin)">
        <td class="tsmall">{{ liqClock(r.time) }}</td>
        <td class="coin">{{ r.coin }}</td>
        <td><span class="dir" :class="r.side === 'long' ? 'short' : 'long'">{{ r.side === 'long' ? '多單' : '空單' }}</span></td>
        <td class="r"><b>${{ r.usd >= 1e6 ? (r.usd / 1e6).toFixed(2) + 'M' : (r.usd / 1e3).toFixed(1) + 'K' }}</b></td>
        <td class="r">{{ fmtPrice(r.px) }}</td>
      </tr>
    </tbody>
  </table>
  </section>
</template>

<style scoped>
/* 清算熱區(近期清算依價位分布)*/
.liqheat { background: var(--c-surf); border: 1px solid var(--c-line); border-radius: var(--r-lg); padding: 14px 16px; margin-bottom: 14px; }
.lh-head { display: flex; align-items: center; gap: 12px; margin-bottom: 10px; flex-wrap: wrap; }
.lh-title { font-family: var(--f-disp); font-weight: 700; font-size: 14px; }
.lh-sel { background: var(--c-bg2); border: 1px solid var(--c-line2); color: var(--c-txt); border-radius: 8px; padding: 4px 8px; font-family: var(--f-mono); font-size: 12px; }
.lh-legend { margin-left: auto; font-size: 11px; color: var(--c-mut); display: flex; align-items: center; gap: 5px; }
.lh-dot { display: inline-block; width: 9px; height: 9px; border-radius: 2px; }
.lh-dot.long { background: var(--c-dn); } .lh-dot.short { background: var(--c-up); }
.lh-rows { display: flex; flex-direction: column; gap: 3px; }
.lh-row { display: grid; grid-template-columns: 84px 1fr 72px; gap: 8px; align-items: center; }
.lh-px { font-family: var(--f-mono); font-size: 11.5px; color: var(--c-mut); text-align: right; }
.lh-bar { display: flex; height: 15px; background: var(--c-bg2); border-radius: 4px; overflow: hidden; }
.lh-long { height: 100%; background: linear-gradient(90deg, rgba(255,92,108,.5), var(--c-dn)); }
.lh-short { height: 100%; background: linear-gradient(90deg, var(--c-up), rgba(55,214,138,.5)); }
.lh-amt { font-family: var(--f-mono); font-size: 11px; color: var(--c-mut); }
</style>
