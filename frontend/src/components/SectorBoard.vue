<!--
  板塊強弱 / 輪動(公開)。把全市場 24h 漲跌依板塊聚合,每整點更新。

  完全自給自足:自己打 /api/sectors,排序與展開狀態都是內部的。
-->
<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { authFetch } from '../lib/api'
import { fmtPct, fundClock } from '../lib/format'

const sectors = ref(null)
async function load() {
  try {
    const res = await authFetch('/api/sectors')
    if (res.ok) sectors.value = await res.json()
  } catch (e) {
    /* secondary */
  }
}

const sectorSort = ref('strength') // 'strength' | 'rotation'
const sectorVw = ref(false)        // false=等權 avg, true=量權 vw
const sectorOpen = ref('')         // 目前展開的板塊(下鑽)

const sectorRows = computed(() => {
  if (!sectors.value) return []
  const rows = [...sectors.value.rows]
  if (sectorSort.value === 'rotation') rows.sort((a, b) => b.delta - a.delta)
  else rows.sort((a, b) => (sectorVw.value ? b.vw_chg - a.vw_chg : b.avg_chg - a.avg_chg))
  return rows
})

// 不用 AI 的固定結論句:領頭 / 落後 / 本小時輪動。
// 領頭與落後跟著「等權/量權」切換,才會與表格一致。
const sectorByStrength = computed(() => {
  const rows = [...(sectors.value?.rows || [])]
  rows.sort((a, b) => (sectorVw.value ? b.vw_chg - a.vw_chg : b.avg_chg - a.avg_chg))
  return rows
})
const sectorLead = computed(() => sectorByStrength.value.slice(0, 2).map((x) => x.sector).join('、'))
const sectorLag = computed(() => { const r = sectorByStrength.value; return r.length ? r[r.length - 1].sector : '' })
const sectorHot = computed(() => {
  let best = null
  for (const x of sectors.value?.rows || []) if (x.delta >= 0.8 && x.vs_btc > 0 && (!best || x.delta > best.delta)) best = x
  return best ? `${best.sector}(▲+${best.delta})` : ''
})

// 後端每整點才重算,但使用者可能把這頁開著不動,所以自己定期回抓。
// (原本是靠 App.vue 的 15 秒輪詢帶著刷,拆出來後要自己顧。)
let timer = null
onMounted(() => {
  load()
  timer = setInterval(load, 5 * 60 * 1000)
})
onUnmounted(() => clearInterval(timer))
defineExpose({ load })
</script>

<template>
  <section>
  <div class="mk-head">
    <h2>板塊強弱<span class="help" tabindex="0">?<span class="help-pop">把全市場 24h 漲跌依板塊聚合,排出強弱。<b>相對BTC</b>=板塊平均 − BTC 24h(&gt;0 = 跑贏大盤、資金流入)。<b>本小時輪動</b>=相對BTC 較上小時的變化(▲ 資金轉入、▼ 轉出)。<b>上漲比例</b>=板塊內上漲檔數占比。<br><b>等權</b>=板塊內每檔幣一票(小幣大漲也算);<b>量權</b>=用成交量加權(大市值/主流幣主導)。<br>點板塊可展開看是哪幾檔在拉。每整點更新。⚠️ 僅供參考,非投資建議。</span></span></h2>
    <span class="mk-count" v-if="sectors && sectors.updated_at">BTC 24h {{ fmtPct(sectors.btc_chg) }} · {{ sectorRows.length }} 板塊 · {{ fundClock(new Date(sectors.updated_at).getTime()) }} 更新</span>
  </div>
  <div v-if="sectorRows.length" class="sec-summary">
    🏆 領頭 <b class="long">{{ sectorLead }}</b> · 🐢 落後 <b class="short">{{ sectorLag }}</b><template v-if="sectorHot"> · 🔥 本小時轉強 <b class="long">{{ sectorHot }}</b></template>
  </div>
  <div class="timefilter" v-if="sectors && sectors.rows.length">
    <span class="tf-label">排序</span>
    <button :class="{ on: sectorSort === 'strength' }" @click="sectorSort = 'strength'">強弱</button>
    <button :class="{ on: sectorSort === 'rotation' }" @click="sectorSort = 'rotation'">本小時輪動</button>
    <button class="tf-sort" :class="{ on: !sectorVw }" @click="sectorVw = false" title="板塊內每檔幣一票(小幣大漲也算)">等權</button>
    <button class="tf-sort" :class="{ on: sectorVw }" @click="sectorVw = true" title="用成交量加權(大市值主導)">量權</button>
  </div>
  <table v-if="sectorRows.length" class="grid">
    <thead><tr><th>板塊</th><th class="r">平均24h</th><th class="r">相對BTC</th><th class="r" title="板塊內上漲檔數占比">上漲比例</th><th class="r" title="相對BTC 較上小時的變化">本小時輪動</th><th class="r">檔數</th></tr></thead>
    <tbody>
      <template v-for="r in sectorRows" :key="r.sector">
        <tr class="clickable" @click="sectorOpen = sectorOpen === r.sector ? '' : r.sector">
          <td class="coin">{{ sectorOpen === r.sector ? '▾' : '▸' }} {{ r.sector }}</td>
          <td class="r" :class="(sectorVw ? r.vw_chg : r.avg_chg) >= 0 ? 'long' : 'short'"><b>{{ fmtPct(sectorVw ? r.vw_chg : r.avg_chg) }}</b></td>
          <td class="r" :class="r.vs_btc >= 0 ? 'long' : 'short'">{{ fmtPct(r.vs_btc) }}</td>
          <td class="r tsmall">{{ r.breadth }}%</td>
          <td class="r" :class="r.delta > 0 ? 'long' : r.delta < 0 ? 'short' : ''">{{ r.delta > 0 ? '▲' : r.delta < 0 ? '▼' : '' }}{{ r.delta >= 0 ? '+' : '' }}{{ r.delta }}</td>
          <td class="r tsmall">{{ r.count }}</td>
        </tr>
        <tr v-if="sectorOpen === r.sector" class="sec-detail">
          <td colspan="6">
            <span class="sec-detail-lbl">板塊成員(24h 由強到弱):</span>
            <span v-for="c in r.coins" :key="c.coin" class="sec-chip" :class="c.chg >= 0 ? 'up' : 'down'">{{ c.coin }} {{ fmtPct(c.chg) }}</span>
          </td>
        </tr>
      </template>
    </tbody>
  </table>
  <p v-else class="loading">計算板塊強弱中…(每整點更新;首個整點後建立)</p>
</section>
</template>
