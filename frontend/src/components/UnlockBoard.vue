<!--
  代幣解鎖看板(公開,DefiLlama emissions)。自己抓 /api/unlock。
-->
<script setup>
import { ref, computed, onMounted, onUnmounted } from "vue"
import { authFetch } from "../lib/api"
import { fmtPct, fmtNum, fundClock } from "../lib/format"

const unlock = ref(null)
async function load() {
  try {
    const res = await authFetch("/api/unlock")
    if (res.ok) unlock.value = await res.json()
  } catch (e) { /* secondary */ }
}
const unlockSort = ref("sell") // sell: 30d 佔流通% 大→小(賣壓);date: 最近懸崖優先
const unlockRows = computed(() => {
  if (!unlock.value) return []
  const rows = [...unlock.value.rows]
  if (unlockSort.value === "date") {
    rows.sort((a, b) => {
      const ta = a.peak_date ? new Date(a.peak_date).getTime() : Infinity
      const tb = b.peak_date ? new Date(b.peak_date).getTime() : Infinity
      return ta - tb
    })
  }
  return rows
})
function unlockDays(d) {
  if (!d) return "—"
  const days = Math.round((new Date(d).getTime() - Date.now()) / 86400000)
  if (days <= 0) return "今日"
  return days + " 天"
}
function unlockDate(d) {
  if (!d) return "—"
  return new Date(d).toLocaleDateString("zh-TW", { month: "2-digit", day: "2-digit" })
}
let timer = null
onMounted(() => { load(); timer = setInterval(load, 10 * 60000) })
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <section>
  <div class="mk-head">
    <h2>代幣解鎖<span class="help" tabindex="0">?<span class="help-pop">主流代幣的即將解鎖佔<b>流通供給</b>的比例=市場潛在賣壓;比例越高、越集中,拋壓風險越大。「下次懸崖」為未來 30 天內單日最大解鎖(佔最大供給%)。持續性的質押釋放不計入。⚠️ 非投資建議。</span></span></h2>
    <span class="mk-count" v-if="unlock && unlock.updated_at">來源 DefiLlama · {{ unlockRows.length }} 檔 · {{ fundClock(new Date(unlock.updated_at).getTime()) }} 更新</span>
  </div>
  <div class="timefilter" v-if="unlock && unlock.rows.length">
    <span class="tf-label">排序</span>
    <button :class="{ on: unlockSort === 'sell' }" @click="unlockSort = 'sell'">賣壓(30天%)</button>
    <button :class="{ on: unlockSort === 'date' }" @click="unlockSort = 'date'">最近懸崖</button>
  </div>
  <table v-if="unlockRows.length" class="grid">
    <thead><tr><th>代幣</th><th class="r" title="未來 7 天解鎖佔流通供給">7天</th><th class="r" title="未來 30 天解鎖佔流通供給 + 數量">30天</th><th class="r">30天估值</th><th class="r" title="30 天內單日最大解鎖(佔最大供給%)">下次懸崖</th><th>解鎖對象</th></tr></thead>
    <tbody>
      <tr v-for="u in unlockRows" :key="u.name">
        <td class="coin">{{ u.coin }}<small class="vtag"> {{ u.name }}</small></td>
        <td class="r tsmall">{{ u.next7_pct ? u.next7_pct.toFixed(2) + '%' : '—' }}</td>
        <td class="r"><b :class="{ short: u.next30_pct >= 3 }">{{ u.next30_pct.toFixed(2) }}%</b><small class="vtag"> {{ fmtNum(u.next30_amt) }}<template v-if="!u.by_circ"> ⚠</template></small></td>
        <td class="r tsmall">{{ u.usd30 ? '$' + fmtNum(u.usd30) : '—' }}</td>
        <td class="r tsmall">{{ unlockDate(u.peak_date) }} <span class="vtag">{{ unlockDays(u.peak_date) }}</span> · {{ u.peak_pct_max ? u.peak_pct_max.toFixed(2) + '%' : '—' }}</td>
        <td class="tsmall">{{ u.cats.join('、') }}</td>
      </tr>
    </tbody>
  </table>
  <p v-else class="loading">載入代幣解鎖中…</p>
  </section>
</template>
