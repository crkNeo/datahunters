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
// 有持倉的排前面(依名目大小),沒持倉的收進「監控中」小列
const activeCards = computed(() => cards.value.filter((c) => c.positions && c.positions.length)
  .sort((a, b) => sumNtl(b) - sumNtl(a)))
const flatCards = computed(() => cards.value.filter((c) => !c.positions || !c.positions.length))
function sumNtl(c) { return (c.positions || []).reduce((s, p) => s + Math.abs(p.notional), 0) }
const events = computed(() => (data.value ? data.value.events : []))
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

    <div v-if="activeCards.length" class="wl-cards">
      <div v-for="c in activeCards" :key="c.addr" class="wl-card">
        <div class="wl-top">
          <div class="wl-who"><span class="wl-name">{{ c.name }}</span><span class="wl-note">{{ c.note }}</span></div>
          <div class="wl-acct"><span class="wl-k">帳戶淨值</span><b>{{ fmtUsd(c.acct) }}</b></div>
        </div>
        <table class="grid wl-pos">
          <thead><tr><th>幣種</th><th>方向</th><th class="r">名目</th><th class="r">進場</th><th class="r">未實現</th><th class="r">槓桿</th><th class="r">距強平</th></tr></thead>
          <tbody>
            <tr v-for="p in c.positions" :key="p.coin" class="clickable" :class="{ 'wl-danger': near(p.liq_dist) }" @click="$emit('coin', p.coin)">
              <td class="coin">{{ p.coin }}</td>
              <td><span class="dir" :class="p.side === 'long' ? 'short' : 'long'">{{ p.side === 'long' ? '做多' : '做空' }}</span></td>
              <td class="r mono">{{ fmtUsd(p.notional) }}</td>
              <td class="r mono tsmall">{{ fmtPx(p.entry) }}</td>
              <td class="r mono" :class="p.upnl >= 0 ? 'short' : 'long'">{{ p.upnl >= 0 ? '+' : '' }}{{ fmtUsd(p.upnl) }}</td>
              <td class="r tsmall">{{ p.lev }}x</td>
              <td class="r mono" :class="{ 'wl-liq': near(p.liq_dist) }">{{ p.liq_dist ? p.liq_dist.toFixed(1) + '%' : '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
    <p v-else-if="cards.length" class="wl-none">目前追蹤名單皆無進行中持倉 · 一有動作會在下方「近期動作」出現</p>
    <p v-else class="loading">載入名人動向中…</p>

    <div v-if="flatCards.length" class="wl-flat">
      <span class="wl-flat-lbl">監控中 · 目前無持倉</span>
      <span v-for="c in flatCards" :key="c.addr" class="wl-chip" :title="c.note">{{ c.name }}</span>
    </div>

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
.wl-admin { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; background: var(--c-bg2); border: 1px solid var(--c-line); border-radius: var(--r-md); padding: 9px 14px; margin-bottom: 12px; }
.wl-toggle { display: flex; align-items: center; gap: 7px; font-size: 13px; font-weight: 600; cursor: pointer; }
.wl-toggle small { color: var(--c-mut); font-weight: 400; }
.wl-adnote { font-size: 11px; color: var(--c-mut2); }
.wl-cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(360px, 1fr)); gap: 14px; }
.wl-card { background: var(--c-surf); border: 1px solid var(--c-line); border-radius: var(--r-lg); padding: 14px 16px; }
.wl-top { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; margin-bottom: 10px; }
.wl-who { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.wl-name { font-family: var(--f-disp); font-weight: 800; font-size: 17px; color: var(--c-gold); }
.wl-note { font-size: 11px; color: var(--c-mut2); }
.wl-acct { text-align: right; white-space: nowrap; }
.wl-acct .wl-k { display: block; font-size: 10.5px; color: var(--c-mut2); }
.wl-acct b { font-family: var(--f-mono); font-size: 15px; color: var(--c-txt); }
.wl-pos { width: 100%; }
.wl-pos .mono { font-family: var(--f-mono); }
.wl-danger { background: var(--c-dn-bg); }
.wl-liq { color: var(--c-dn); font-weight: 700; }
.wl-none { font-size: 13px; color: var(--c-mut); padding: 16px 4px; margin: 0; }
.wl-flat { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-top: 14px; padding: 10px 12px; background: var(--c-bg2); border: 1px solid var(--c-line); border-radius: var(--r-md); }
.wl-flat-lbl { font-size: 11.5px; color: var(--c-mut2); }
.wl-chip { font-size: 12px; font-weight: 600; color: var(--c-mut); background: var(--c-surf2); border: 1px solid var(--c-line); border-radius: 20px; padding: 3px 11px; cursor: default; }
.wl-feed { display: flex; flex-direction: column; gap: 1px; }
.wl-ev { display: grid; grid-template-columns: 48px 22px 1fr; gap: 8px; align-items: center; padding: 6px 4px; border-bottom: 1px solid var(--c-line); font-size: 12.5px; }
.wl-ev:last-child { border-bottom: 0; }
.wl-evt { font-family: var(--f-mono); font-size: 11px; color: var(--c-mut2); }
.wl-evtx { color: var(--c-txt); cursor: pointer; }
.wl-evtx:hover { color: var(--c-gold); }
</style>
