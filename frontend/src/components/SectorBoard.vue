<!--
  板塊強弱 / 輪動(公開)。把全市場 24h 漲跌依板塊聚合,每整點更新。

  完全自給自足:自己打 /api/sectors,排序與展開狀態都是內部的。
-->
<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { authFetch } from '../lib/api'
import { fmtPct, fundClock } from '../lib/format'

defineEmits(['coin']) // 點成員幣種 → 開幣種明細(交由 App.vue 的 openDetail)

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
// 強弱長條(依 mock):寬度 ∝ 該板塊平均漲跌相對本批最大絕對值
const chgOf = (r) => (sectorVw.value ? r.vw_chg : r.avg_chg)
const secMaxAbs = computed(() => Math.max(0.01, ...sectorRows.value.map((r) => Math.abs(chgOf(r)))))
const secBarW = (r) => Math.max(6, Math.round(Math.abs(chgOf(r)) / secMaxAbs.value * 100))

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
    <thead><tr><th>板塊</th><th class="sec-strength">強弱<span class="help" tabindex="0">?<span class="help-pop">長條寬度 = 該板塊<b>平均 24h 漲跌</b>相對「本批最強板塊」的比例(最強者滿格,其餘按比例縮短);<b>綠</b>=平均上漲、<b>紅</b>=下跌。會跟著上方 <b>等權 / 量權</b> 切換(等權=板塊內每檔一票;量權=成交量加權,大幣主導)。純比較各板塊相對強弱,非絕對數值。</span></span></th><th class="r">平均24h</th><th class="r">相對BTC</th><th class="r" title="板塊內上漲檔數占比">上漲比例</th><th class="r" title="相對BTC 較上小時的變化">本小時輪動</th><th class="r">檔數</th></tr></thead>
    <tbody>
      <template v-for="r in sectorRows" :key="r.sector">
        <tr class="clickable" @click="sectorOpen = sectorOpen === r.sector ? '' : r.sector">
          <td class="coin">{{ sectorOpen === r.sector ? '▾' : '▸' }} {{ r.sector }}</td>
          <td class="sec-strength"><span class="secbar"><i :class="chgOf(r) >= 0 ? 'pos' : 'neg'" :style="{ width: secBarW(r) + '%' }"></i></span></td>
          <td class="r" :class="(sectorVw ? r.vw_chg : r.avg_chg) >= 0 ? 'long' : 'short'"><b>{{ fmtPct(sectorVw ? r.vw_chg : r.avg_chg) }}</b></td>
          <td class="r" :class="r.vs_btc >= 0 ? 'long' : 'short'">{{ fmtPct(r.vs_btc) }}</td>
          <td class="r tsmall">{{ r.breadth }}%</td>
          <td class="r" :class="r.delta > 0 ? 'long' : r.delta < 0 ? 'short' : ''">{{ r.delta > 0 ? '▲' : r.delta < 0 ? '▼' : '' }}{{ r.delta >= 0 ? '+' : '' }}{{ r.delta }}</td>
          <td class="r tsmall">{{ r.count }}</td>
        </tr>
        <tr v-if="sectorOpen === r.sector" class="sec-detail">
          <td colspan="7">
            <div class="sec-detail-lbl">板塊成員(24h 由強到弱)</div>
            <div class="sec-sublist">
              <div v-for="c in r.coins" :key="c.coin" class="sec-subrow clickable" @click.stop="$emit('coin', c.coin)">
                <span class="sec-sub-coin">{{ c.coin }}</span>
                <span class="sec-sub-chg" :class="c.chg >= 0 ? 'long' : 'short'">{{ fmtPct(c.chg) }}</span>
              </div>
            </div>
          </td>
        </tr>
      </template>
    </tbody>
  </table>
  <p v-else class="loading">計算板塊強弱中…(每整點更新;首個整點後建立)</p>
</section>
</template>

<style scoped>
/* 強弱長條(依 mock:綠正紅負,寬度 ∝ 相對強度)*/
.sec-strength { width: 22%; min-width: 90px; }
.secbar { display: block; height: 8px; border-radius: 5px; background: var(--c-surf2); overflow: hidden; }
.secbar i { display: block; height: 100%; border-radius: 5px; }
.secbar i.pos { background: linear-gradient(90deg, rgba(55,214,138,.5), var(--c-up)); }
.secbar i.neg { background: linear-gradient(90deg, var(--c-dn), rgba(255,92,108,.5)); }
/* 展開:板塊成員逐條下拉列(一行一檔:幣種左、漲跌右)*/
.sec-detail-lbl { display: block; font-size: 11px; color: var(--c-mut2); margin: 2px 0 8px; }
.sec-sublist { display: flex; flex-direction: column; max-width: 460px; }
.sec-subrow { display: flex; align-items: center; justify-content: space-between; padding: 7px 10px; font-family: var(--f-mono); font-size: 12.5px; border-bottom: 1px solid var(--c-line); cursor: pointer; border-radius: 6px; transition: background .12s; }
.sec-subrow:hover { background: var(--c-surf2); }
.sec-subrow:last-child { border-bottom: 0; }
.sec-sub-coin { font-family: var(--f-disp); font-weight: 600; color: var(--c-txt); }
@media (max-width: 768px) { .sec-strength { display: none; } .sec-sublist { max-width: 100%; } }
</style>
