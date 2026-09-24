<!--
  名人動向(whale watch)。自己抓 /api/whales(後端 Hyperliquid 即時倉位 + 動作事件流)。
  ⚠️ 地址為社群/鏈上標註,可能過期或標錯;純資訊呈現,非投資建議、跟單風險自負。
-->
<script setup>
import { ref, computed, onMounted, onUnmounted } from "vue"
import { authFetch } from "../lib/api"

const props = defineProps({ admin: { type: Boolean, default: false } })
const emit = defineEmits(["coin", "toast"])

const data = ref(null)
async function load() {
  try {
    const res = await authFetch("/api/whales")
    if (res.ok) data.value = await res.json()
  } catch (e) { /* secondary */ }
}
const cards = computed(() => (data.value ? data.value.cards : []))
function sumNtl(c) { return (c.positions || []).reduce((s, p) => s + Math.abs(p.notional), 0) }
const hasPos = (c) => !!(c && c.positions && c.positions.length)
// 標籤順序:有持倉的排前面(依名目大小),沒持倉的(監控中)排後面
const orderedWhales = computed(() => {
  const active = cards.value.filter(hasPos).sort((a, b) => sumNtl(b) - sumNtl(a))
  const flat = cards.value.filter((c) => !hasPos(c))
  return [...active, ...flat]
})
const selected = ref("") // 目前選中的地址
// 一律回一個有效對象(沒選就用第一個,通常是持倉最大的)
const sel = computed(() => {
  const list = orderedWhales.value
  if (!list.length) return null
  return list.find((c) => c.addr === selected.value) || list[0]
})
const events = computed(() => (data.value ? data.value.events : []))
const rank = computed(() => (data.value ? data.value.rank || [] : []))
const pushOn = ref(false)
async function togglePush() {
  const next = !pushOn.value
  try {
    const res = await authFetch("/api/admin/whale-push", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ on: next }) })
    if (res.ok) { const d = await res.json(); pushOn.value = !!d.on; emit("toast", pushOn.value ? "名人動向推播已開啟" : "名人動向推播已關閉") }
  } catch (e) { emit("toast", "設定失敗") }
}

function fmtUsd(v) {
  const a = Math.abs(v)
  if (a >= 1e9) return "$" + (v / 1e9).toFixed(2) + "B"
  if (a >= 1e6) return "$" + (v / 1e6).toFixed(2) + "M"
  if (a >= 1e3) return "$" + (v / 1e3).toFixed(1) + "K"
  return "$" + v.toFixed(0)
}
function fmtPx(v) { return v >= 100 ? v.toLocaleString(undefined, { maximumFractionDigits: 2 }) : v.toPrecision(4) }
function clock(ms) { return new Date(ms).toLocaleTimeString("zh-TW", { hour: "2-digit", minute: "2-digit", hour12: false }) }
const kindIcon = { open: "🟢", add: "➕", reduce: "➖", close: "✅", flip: "🔄" }
const near = (d) => d > 0 && d < 5 // 距強平 <5% → 高亮

let timer = null
onMounted(() => { load().then(() => { if (data.value) pushOn.value = !!data.value.push_on }); timer = setInterval(load, 30000) })
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <section>
    <div class="mk-head">
      <h2>名人動向<span class="help" tabindex="0">?<span class="help-pop">精選對象在 <b>Hyperliquid</b> 的<b>即時永續倉位</b>與動作(開/加/減/平/反手)。Hyperliquid 完全鏈上公開,任何地址的倉位、進場、未實現盈虧、<b>強平價</b>都看得到。<br>⚠️ 地址為<b>社群/鏈上偵測標註,可能過期或標錯</b>;交易所(CEX)內的倉位無法得知。純資訊呈現,<b>非投資建議、跟單風險自負</b>。</span></span></h2>
      <span class="mk-count" v-if="data">來源 {{ data.source }}</span>
    </div>

    <div v-if="props.admin" class="wl-admin">
      <label class="wl-toggle"><input type="checkbox" :checked="pushOn" @change="togglePush" /> 名人動向推播<small>{{ pushOn ? '(已開啟)' : '(預設關閉)' }}</small></label>
      <span class="wl-adnote">開/平/反手時推播給所有訂閱者</span>
    </div>

    <!-- 標籤切換人:綠點=有持倉,灰=監控中(空倉);點名字切換,下方只顯示這一位 -->
    <div v-if="cards.length" class="wl-picker">
      <button v-for="c in orderedWhales" :key="c.addr" class="wl-pill" :class="{ on: sel && sel.addr === c.addr, live: hasPos(c) }" @click="selected = c.addr" :title="c.note">
        <i v-if="hasPos(c)" class="wl-dot"></i>{{ c.name }}
      </button>
    </div>

    <div v-if="sel" class="wl-card">
      <div class="wl-top">
        <div class="wl-who"><span class="wl-name">{{ sel.name }}</span><span class="wl-note">{{ sel.note }}</span></div>
        <div class="wl-acct"><span class="wl-k">帳戶淨值</span><b>{{ fmtUsd(sel.acct) }}</b></div>
      </div>
      <div v-if="hasPos(sel)" class="tblwrap">
        <table class="grid wl-pos">
          <thead><tr><th>幣種</th><th>方向</th><th class="r">名目</th><th class="r">進場</th><th class="r">未實現</th><th class="r">槓桿</th><th class="r">距強平</th></tr></thead>
          <tbody>
            <tr v-for="p in sel.positions" :key="p.coin" class="clickable" :class="{ 'wl-danger': near(p.liq_dist) }" @click="$emit('coin', p.coin)">
              <td class="coin">{{ p.coin }}</td>
              <td><span class="dir" :class="p.side === 'long' ? 'long' : 'short'">{{ p.side === 'long' ? '做多' : '做空' }}</span></td>
              <td class="r mono">{{ fmtUsd(p.notional) }}</td>
              <td class="r mono tsmall">{{ fmtPx(p.entry) }}</td>
              <td class="r mono" :class="p.upnl >= 0 ? 'short' : 'long'">{{ p.upnl >= 0 ? '+' : '' }}{{ fmtUsd(p.upnl) }}</td>
              <td class="r tsmall">{{ p.lev }}x</td>
              <td class="r mono" :class="{ 'wl-liq': near(p.liq_dist) }">{{ p.liq_dist ? p.liq_dist.toFixed(1) + '%' : '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="wl-empty">目前無持倉 · 監控中 —— 一有動作會在下方「近期動作」出現</p>
    </div>
    <p v-else class="loading">載入名人動向中…</p>

    <template v-if="rank.length">
      <h3 class="psub">🐋 巨鯨排行 · 即時<span class="wl-sub">HL 目前總名目最大者 · 附主倉進場點位與距強平(自動)</span></h3>
      <div class="tblwrap">
        <table class="grid wl-rank">
          <thead><tr><th class="r">#</th><th>對象</th><th class="r">總名目</th><th>主倉</th><th class="r">進場</th><th class="r">距強平</th></tr></thead>
          <tbody>
            <tr v-for="r in rank" :key="r.addr" class="clickable" @click="$emit('coin', r.top.coin)">
              <td class="r tsmall">{{ r.rank }}</td>
              <td class="coin"><span :class="r.known ? 'wl-known' : 'wl-anon'">{{ r.name }}</span></td>
              <td class="r mono"><b>{{ fmtUsd(r.ntl) }}</b></td>
              <td><b>{{ r.top.coin }}</b> <span class="dir" :class="r.top.side === 'long' ? 'long' : 'short'">{{ r.top.side === 'long' ? '多' : '空' }}</span> <span class="wl-lev">{{ r.top.lev }}x</span></td>
              <td class="r mono tsmall">{{ fmtPx(r.top.entry) }}</td>
              <td class="r mono" :class="{ 'wl-liq': near(r.top.liq_dist) }">{{ r.top.liq_dist ? r.top.liq_dist.toFixed(1) + '%' : '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <template v-if="events.length">
      <h3 class="psub">近期動作</h3>
      <div class="wl-feed">
        <div v-for="(e, i) in events" :key="i" class="wl-ev">
          <span class="wl-evt">{{ clock(e.time) }}</span>
          <span class="wl-evk">{{ kindIcon[e.kind] || '•' }}</span>
          <span class="wl-evtx clickable" @click="$emit('coin', e.coin)">{{ e.text }}</span>
        </div>
      </div>
    </template>
  </section>
</template>

<style scoped>
/* 本站無 CSS 變數,全部用實色(對齊 App.vue 的調色:金 var(--c-gold)、綠 var(--c-up)、紅 var(--c-dn)) */
.wl-admin { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; background: var(--c-bg2); border: 1px solid var(--c-line); border-radius: 10px; padding: 9px 14px; margin-bottom: 12px; }
.wl-toggle { display: flex; align-items: center; gap: 7px; font-size: 13px; font-weight: 600; color: var(--c-txt); cursor: pointer; }
.wl-toggle small { color: var(--c-mut); font-weight: 400; }
.wl-adnote { font-size: 11px; color: var(--c-mut2); }
/* 標籤切換人 */
.wl-picker { display: flex; align-items: center; gap: 7px; flex-wrap: wrap; margin-bottom: 12px; }
.wl-pill { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; font-weight: 600; cursor: pointer;
  color: var(--c-mut); background: var(--c-bg2); border: 1px solid var(--c-line); border-radius: 20px; padding: 5px 13px; transition: all .12s; }
.wl-pill:hover { color: var(--c-txt); border-color: var(--c-line2); }
.wl-pill.live { color: var(--c-txt); }
.wl-pill.on { background: var(--c-gold-soft); border-color: var(--c-gold); color: var(--c-gold); font-weight: 800; }
.wl-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--c-up); box-shadow: 0 0 6px rgba(55,214,138,.7); flex: 0 0 auto; }
.wl-card { background: var(--c-surf); border: 1px solid var(--c-line); border-radius: 14px; padding: 14px 16px; min-width: 0; }
.wl-top { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; margin-bottom: 10px; }
.wl-who { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.wl-name { font-weight: 800; font-size: 17px; color: var(--c-gold); }
.wl-note { font-size: 11px; color: var(--c-mut); }
.wl-acct { text-align: right; white-space: nowrap; }
.wl-acct .wl-k { display: block; font-size: 10.5px; color: var(--c-mut2); }
.wl-acct b { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 15px; color: var(--c-txt); }
/* 表格給 min-width,窄螢幕時在 .tblwrap 內橫向捲動,不擠壓換行 */
.wl-pos { width: 100%; min-width: 440px; }
.wl-pos th, .wl-pos td { white-space: nowrap; }
.wl-pos .mono, .wl-rank .mono, .wl-lev { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.wl-danger { background: var(--c-dn-bg); }
.wl-liq { color: var(--c-dn); font-weight: 700; }
.wl-lev { font-size: 11px; color: var(--c-mut); }
.wl-empty { font-size: 13px; color: var(--c-mut); padding: 14px 4px; margin: 0; }
.tblwrap { overflow-x: auto; -webkit-overflow-scrolling: touch; max-width: 100%; }
.wl-sub { margin-left: 10px; font-size: 11px; font-weight: 400; color: var(--c-mut2); }
.wl-rank { width: 100%; min-width: 480px; }
.wl-rank th, .wl-rank td { white-space: nowrap; }
.wl-known { color: var(--c-gold); font-weight: 700; }
.wl-anon { color: var(--c-mut); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
.wl-feed { display: flex; flex-direction: column; gap: 1px; }
.wl-ev { display: grid; grid-template-columns: 48px 22px 1fr; gap: 8px; align-items: center; padding: 6px 4px; border-bottom: 1px solid var(--c-line); font-size: 12.5px; }
.wl-ev:last-child { border-bottom: 0; }
.wl-evt { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 11px; color: var(--c-mut2); }
.wl-evtx { color: var(--c-txt); cursor: pointer; }
.wl-evtx:hover { color: var(--c-gold); }
</style>
