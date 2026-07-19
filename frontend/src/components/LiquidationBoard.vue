<!--
  清算看板(公開)。自己抓 /api/liquidations。
-->
<script setup>
import { ref, onMounted, onUnmounted } from "vue"
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
