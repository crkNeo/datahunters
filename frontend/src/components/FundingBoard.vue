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
// 極端費率門檻(每 8h):≥0.05% = 多單擁擠;<0 = 空方付費(潛在軋空)
const HOT = 0.0005
function fundTag(rate) {
  if (rate >= HOT) return { t: '🔥 多單擁擠', c: 'hot' }
  if (rate < 0) return { t: '🧊 空方付費', c: 'cold' }
  return null
}
// 摘要:全市場最擁擠多單(費率最高)+ 空方付費最多(費率最負)
const fundExtremes = computed(() => {
  const rows = funding.value ? funding.value.rows : []
  if (!rows.length) return null
  let hi = rows[0], lo = rows[0]
  for (const r of rows) { if (r.rate > hi.rate) hi = r; if (r.rate < lo.rate) lo = r }
  return { hi, lo }
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
  <div v-if="fundExtremes" class="fund-sum">
    🔥 多單最擁擠 <b class="short" @click.stop="$emit('coin', fundExtremes.hi.coin)">{{ fundExtremes.hi.coin }} {{ (fundExtremes.hi.rate * 100).toFixed(4) }}%</b>
    · 🧊 空方付費最多 <b class="long" @click.stop="$emit('coin', fundExtremes.lo.coin)">{{ fundExtremes.lo.coin }} {{ (fundExtremes.lo.rate * 100).toFixed(4) }}%</b>
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
        <td class="r" :class="f.rate >= 0 ? 'short' : 'long'"><b>{{ (f.rate * 100).toFixed(4) }}%</b><span v-if="fundTag(f.rate)" class="fund-tag" :class="fundTag(f.rate).c">{{ fundTag(f.rate).t }}</span></td>
        <td class="r tsmall">{{ fundClock(f.next_ms) }}</td>
      </tr>
    </tbody>
  </table>
  <p v-else class="loading">載入資金費率中…</p>
  </section>
</template>

<style scoped>
/* 極端費率摘要 + tag */
.fund-sum { background: var(--c-bg2); border: 1px solid var(--c-line); border-radius: var(--r-md); padding: 10px 15px; font-size: 12.5px; color: var(--c-mut); margin-bottom: 12px; }
.fund-sum b { font-family: var(--f-mono); cursor: pointer; }
.fund-sum b:hover { text-decoration: underline; }
.fund-tag { margin-left: 8px; font-size: 10px; font-family: var(--f-mono); border-radius: 5px; padding: 1px 6px; font-weight: 600; }
.fund-tag.hot { background: var(--c-dn-bg); color: var(--c-dn); }
.fund-tag.cold { background: var(--c-up-bg); color: var(--c-up); }
</style>
