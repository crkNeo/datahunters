<!--
  資金費率看板(公開)。自己抓 /api/funding,板塊篩選與排序都是內部狀態。
  原本靠 App.vue 的 15 秒輪詢帶著刷,拆出來後自己顧。
-->
<script setup>
import { ref, computed, onMounted, onUnmounted } from "vue"
import { authFetch } from "../lib/api"
import { fmtPct, fundClock } from "../lib/format"

const emit = defineEmits(["coin"])
const funding = ref(null)
async function load() {
  try {
    const res = await authFetch("/api/funding")
    if (res.ok) funding.value = await res.json()
  } catch (e) { /* secondary */ }
}
const fundingSector = ref("")
const fundingAbs = ref(false) // true: 依 |費率| 排序,找兩端極值
const fundingSectors = computed(() => {
  if (!funding.value) return []
  const counts = {}
  for (const r of funding.value.rows) counts[r.sector] = (counts[r.sector] || 0) + 1
  return Object.keys(counts).sort((a, b) => counts[b] - counts[a]) // 幣多的排前面
})
const fundingRows = computed(() => {
  if (!funding.value) return []
  let rows = fundingSector.value ? funding.value.rows.filter((r) => r.sector === fundingSector.value) : funding.value.rows
  if (fundingAbs.value) rows = [...rows].sort((a, b) => Math.abs(b.rate) - Math.abs(a.rate))
  return rows
})
let timer = null
onMounted(() => { load(); timer = setInterval(load, 60000) })
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <section>
  <div class="mk-head">
    <h2>資金費率<span class="help" tabindex="0">?<span class="help-pop">各永續合約的當期資金費率(資料來源 OKX)。<b>正費率</b>=多方付費給空方(市場偏多、多單擁擠),<b>負費率</b>=空方付費給多方。每 8 小時結算一次。費率極端常是情緒過熱/反轉的參考,⚠️ 非投資建議。</span></span></h2>
    <span class="mk-count" v-if="funding && funding.updated_at">來源 OKX · {{ fundingRows.length }} / {{ funding.rows.length }} 檔 · {{ fundClock(new Date(funding.updated_at).getTime()) }} 更新</span>
  </div>
  <div class="timefilter" v-if="funding && funding.rows.length">
    <span class="tf-label">板塊</span>
    <button :class="{ on: fundingSector === '' }" @click="fundingSector = ''">全部</button>
    <button v-for="s in fundingSectors" :key="s" :class="{ on: fundingSector === s }" @click="fundingSector = s">{{ s }}</button>
    <button class="tf-sort" :class="{ on: fundingAbs }" @click="fundingAbs = !fundingAbs" title="切換:費率高→低 / 絕對值大→小(找兩邊極端)">{{ fundingAbs ? '極端排序' : '費率排序' }}</button>
  </div>
  <table v-if="fundingRows.length" class="grid">
    <thead><tr><th>幣種</th><th>板塊</th><th class="r">資金費率</th><th class="r">下次結算</th></tr></thead>
    <tbody>
      <tr v-for="f in fundingRows" :key="f.coin" class="clickable" @click="$emit('coin', f.coin)">
        <td class="coin">{{ f.coin }}</td>
        <td class="tsmall">{{ f.sector }}</td>
        <td class="r" :class="f.rate >= 0 ? 'short' : 'long'"><b>{{ (f.rate * 100).toFixed(4) }}%</b></td>
        <td class="r tsmall">{{ fundClock(f.next_ms) }}</td>
      </tr>
    </tbody>
  </table>
  <p v-else class="loading">載入資金費率中…</p>
  </section>
</template>
