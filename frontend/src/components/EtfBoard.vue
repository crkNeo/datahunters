<!--
  現貨 ETF 每日淨流面板(公開)。自己抓 /api/etf(後端 Farside 抓取,誠實真實值)。
  每個資產:最新淨流大字 + 近5日/近14日合計 + 近14日長條(綠流入/紅流出,零軸在中)。
-->
<script setup>
import { ref, computed, onMounted, onUnmounted } from "vue"
import { authFetch } from "../lib/api"

const etf = ref(null)
async function load() {
  try {
    const res = await authFetch("/api/etf")
    if (res.ok) etf.value = await res.json()
  } catch (e) { /* secondary */ }
}
// 長條相對高度:各資產自己的 14 日 |淨流| 最大值當滿格
function maxAbs(a) { return Math.max(1, ...a.history.map((d) => Math.abs(d.net_m))) }
function barPct(a, d) { return Math.round(Math.abs(d.net_m) / maxAbs(a) * 100) + "%" }
function fmtM(v) { return (v >= 0 ? "+" : "−") + "$" + Math.abs(v).toFixed(1) + "M" }
// 只顯示日期的「日 月」讓 X 軸不擁擠(如 "07 Jul")
function shortDate(s) { return (s || "").split(" ").slice(0, 2).join(" ") }
const hasData = computed(() => etf.value && etf.value.assets.length > 0)

let timer = null
onMounted(() => { load(); timer = setInterval(load, 10 * 60 * 1000) })
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <section>
    <div class="mk-head">
      <h2>現貨 ETF 淨流<span class="help" tabindex="0">?<span class="help-pop">美國現貨比特幣/以太幣 ETF 的<b>每日淨流</b>(資料來源 Farside Investors)。<b>綠</b>=淨流入(機構買盤)、<b>紅</b>=淨流出。長期淨流入常是主升段的資金後盾;連續淨流出則是需求轉弱的警訊。零軸在中間,長條往上為流入、往下為流出。⚠️ 僅供參考,非投資建議。</span></span></h2>
      <span class="mk-count" v-if="etf">來源 {{ etf.source }} · 單位 US$M</span>
    </div>

    <div v-if="hasData" class="etf-grid">
      <div v-for="a in etf.assets" :key="a.asset" class="etf-card">
        <div class="etf-top">
          <span class="etf-asset">{{ a.asset }}</span>
          <span class="etf-latest" :class="a.latest.net_m >= 0 ? 'up' : 'dn'">{{ fmtM(a.latest.net_m) }}</span>
          <span class="etf-date">{{ a.latest.date }}</span>
        </div>
        <div class="etf-sums">
          <span>近 5 日 <b :class="a.sum5 >= 0 ? 'up' : 'dn'">{{ fmtM(a.sum5) }}</b></span>
          <span>近 14 日 <b :class="a.sum14 >= 0 ? 'up' : 'dn'">{{ fmtM(a.sum14) }}</b></span>
        </div>
        <div class="etf-chart">
          <div v-for="(d, i) in a.history" :key="i" class="etf-col" :title="shortDate(d.date) + ' · ' + fmtM(d.net_m)">
            <div class="etf-up"><i v-if="d.net_m >= 0" :style="{ height: barPct(a, d) }"></i></div>
            <div class="etf-dn"><i v-if="d.net_m < 0" :style="{ height: barPct(a, d) }"></i></div>
          </div>
        </div>
        <div class="etf-axis"><span>{{ shortDate(a.history[0] && a.history[0].date) }}</span><span>最新</span></div>
      </div>
    </div>
    <p v-else class="loading">載入 ETF 淨流中…(資料來源 Farside,每日更新)</p>
  </section>
</template>

<style scoped>
.etf-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 14px; }
.etf-card { background: var(--c-surf); border: 1px solid var(--c-line); border-radius: var(--r-lg); padding: 16px 18px; }
.etf-top { display: flex; align-items: baseline; gap: 12px; flex-wrap: wrap; }
.etf-asset { font-family: var(--f-disp); font-weight: 800; font-size: 22px; color: var(--c-gold); }
.etf-latest { font-family: var(--f-mono); font-weight: 700; font-size: 22px; }
.etf-date { font-size: 11.5px; color: var(--c-mut2); margin-left: auto; }
.etf-sums { display: flex; gap: 18px; margin: 10px 0 12px; font-size: 12.5px; color: var(--c-mut); }
.etf-sums b { font-family: var(--f-mono); }
.up { color: var(--c-up); } .dn { color: var(--c-dn); }
/* 14 日長條:零軸在中,上綠下紅 */
.etf-chart { display: flex; align-items: stretch; gap: 3px; height: 96px; border-top: 1px solid var(--c-line); border-bottom: 1px solid var(--c-line); padding: 0; }
.etf-col { flex: 1; display: flex; flex-direction: column; min-width: 0; }
.etf-up, .etf-dn { flex: 1; display: flex; }
.etf-up { align-items: flex-end; border-bottom: 1px dashed var(--c-line2); }
.etf-dn { align-items: flex-start; }
.etf-up i, .etf-dn i { display: block; width: 100%; border-radius: 2px; }
.etf-up i { background: linear-gradient(180deg, var(--c-up), rgba(55,214,138,.45)); }
.etf-dn i { background: linear-gradient(180deg, rgba(255,92,108,.45), var(--c-dn)); }
.etf-axis { display: flex; justify-content: space-between; font-size: 10.5px; color: var(--c-mut2); margin-top: 5px; font-family: var(--f-mono); }
</style>
