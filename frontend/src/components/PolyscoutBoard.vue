<!--
  跟單篩選 · 聰明錢共識(polyscout · 管理員)。
  每小時後端用 AI 彙整 Polymarket CRYPTO 前 20 名交易者「目前的看法」:
  上方=整體大綱 + 信心,下方=前 20 名個別看法(點開看佐證倉位)。
  重點是「他們現在怎麼看盤」,不看進場價。純資訊,非投資建議、跟單風險自負。
-->
<script setup>
import { ref, computed, onMounted } from "vue"
import { authFetch } from "../lib/api"

const emit = defineEmits(["toast"])

const d = ref(null)
const loading = ref(false)
const err = ref("")
async function load() {
  loading.value = true; err.value = ""
  try {
    const res = await authFetch("/api/admin/polyscout", { timeout: 30000 })
    if (res.ok) { d.value = await res.json() }
    else { err.value = (await res.text()).trim() || ("HTTP " + res.status) }
  } catch (e) { err.value = "" + e } finally { loading.value = false }
}
onMounted(load)

const views = computed(() => (d.value && d.value.views) || [])
const seeding = computed(() => !loading.value && !err.value && views.value.length === 0)

const pushOn = ref(false)
async function togglePush() {
  const next = !pushOn.value
  try {
    const res = await authFetch("/api/admin/polyscout-push", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ on: next }) })
    if (res.ok) { pushOn.value = !!(await res.json()).on; emit("toast", pushOn.value ? "聰明錢共識推播已開啟" : "聰明錢共識推播已關閉") }
  } catch (e) { emit("toast", "設定失敗") }
}

const open = ref("") // 展開中的 wallet
const toggle = (w) => { open.value = open.value === w ? "" : w }
const nameOf = (v) => v.name || (v.wallet.slice(0, 6) + "…" + v.wallet.slice(-4))

function fmtUsd(v) {
  const a = Math.abs(v); const s = v < 0 ? "-" : ""
  if (a >= 1e6) return s + "$" + (a / 1e6).toFixed(2) + "M"
  if (a >= 1e3) return s + "$" + (a / 1e3).toFixed(1) + "K"
  return s + "$" + a.toFixed(0)
}
const cents = (v) => (v * 100).toFixed(0) + "¢"
const pct = (v) => (v >= 0 ? "+" : "") + v.toFixed(1) + "%"
// 到期顯示:A+B。市場原名若含盤中時段(短線 K 棒,如「…10:30AM-10:35AM ET」),
// 取區間「結束時刻」= 真正結算時間,顯示「日期 + 時刻 ET」;長天期市場原名沒有時刻,
// 就只給日期(不再補假的 00:00:00)。
function fmtExpiry(p) {
  const date = /^\d{4}-\d{2}-\d{2}$/.test(p.end_date || "") ? p.end_date : (p.end_date || "")
  const times = (p.title || "").match(/\d{1,2}(?::\d{2})?\s*[AP]M/gi)
  if (times && times.length) {
    const t = times[times.length - 1].replace(/\s+/g, "").toUpperCase() // 取結束時刻,如 10:35AM
    const et = /\bET\b/i.test(p.title) ? " ET" : ""
    return date ? `${date} ${t}${et}` : t + et
  }
  return date || "—"
}
const vclass = { "長期穩定": "v-good", "短期爆發": "v-warn", "近期轉弱": "v-bad", "資料不足": "v-mut", "觀察中": "v-mut" }
// 方向配色:偏多綠、偏空紅、區間金、中性灰
const leanClass = (l) => ({ "偏多": "l-up", "偏空": "l-dn", "區間": "l-range", "中性": "l-mut" }[l] || "l-mut")

const COIN = { bitcoin: "BTC", ethereum: "ETH", solana: "SOL", ripple: "XRP", xrp: "XRP", dogecoin: "DOGE", cardano: "ADA", avalanche: "AVAX", chainlink: "LINK", polkadot: "DOT", litecoin: "LTC", "binance coin": "BNB", bnb: "BNB" }
function prettyTitle(t) {
  if (!t) return t
  const low = t.toLowerCase(); let sym = null
  for (const k in COIN) if (low.includes(k)) { sym = COIN[k]; break }
  if (/up or down/i.test(t)) return (sym || "?") + " 短線漲跌"
  const m = t.match(/\$[\d,]+(?:\.\d+)?[KMB]?(?![A-Za-z])/i)
  if (sym && m) {
    if (/\b(dip|drop|fall|below|under)\b/i.test(t)) return `${sym} 會跌到 ${m[0]}?`
    if (/\b(reach|hit|hits|above|exceed|over)\b/i.test(t)) return `${sym} 會漲到 ${m[0]}?`
  }
  return t
}
</script>

<template>
  <section>
    <div class="mk-head">
      <h2>跟單篩選 · 聰明錢共識
        <span class="help" tabindex="0">?<span class="help-pop">每小時由 AI 彙整 Polymarket CRYPTO 排行榜<b>前 20 名交易者目前的持倉</b>,判讀他們「現在覺得市場會怎麼走」:<b>上方=整體大綱與信心,下方=每個人的個別看法</b>(點開看佐證倉位)。<br>重點是<b>方向與看法</b>而非進場價 —— 等看到倉,現價常已貼近結算、跟進沒空間。<br>⚠️ 純資訊,<b>非投資建議、跟單風險自負</b>。</span></span>
      </h2>
      <div class="pc-head-r">
        <span v-if="d && d.updated_at" class="mk-count">{{ d.source }} · {{ d.updated_at }}</span>
        <button class="ps-refresh" :disabled="loading" @click="load">{{ loading ? '…' : '↻' }}</button>
      </div>
    </div>

    <div class="wl-admin">
      <label class="wl-toggle"><input type="checkbox" :checked="pushOn" @change="togglePush" /> 聰明錢共識推播<small>{{ pushOn ? '(已開啟)' : '(預設關閉)' }}</small></label>
      <span class="wl-adnote">每小時彙整更新時推播整體大綱</span>
    </div>

    <p v-if="err" class="ps-err">✕ {{ err }}</p>
    <p v-else-if="loading && !d" class="loading">載入中…</p>
    <p v-else-if="seeding" class="loading">AI 正在彙整前 20 名的看法,首份約在啟動後一分鐘內產生,稍候重新整理…</p>

    <!-- 整體大綱 -->
    <div v-if="d && d.summary" class="pc-overall">
      <div class="pc-badges">
        <span class="pc-lean" :class="leanClass(d.lean)">整體 {{ d.lean || '—' }}</span>
        <span class="pc-conf">信心 <b>{{ d.confidence || '—' }}</b></span>
      </div>
      <p class="pc-summary">{{ d.summary }}</p>
    </div>

    <!-- 前 20 名個別看法 -->
    <template v-if="views.length">
      <h3 class="psub">前 {{ views.length }} 名 · 個別看法<span class="wl-sub">點一列展開佐證倉位</span></h3>
      <div class="pc-list">
        <div v-for="(v, i) in views" :key="v.wallet" class="pc-item">
          <div class="pc-row" :class="{ open: open === v.wallet }" @click="toggle(v.wallet)">
            <span class="pc-idx">{{ i + 1 }}</span>
            <span class="pc-name">{{ nameOf(v) }}</span>
            <span class="ps-v" :class="vclass[v.verdict]">{{ v.verdict }}</span>
            <span class="pc-leanchip" :class="leanClass(v.lean)">{{ v.lean || '—' }}</span>
            <span class="pc-view">{{ v.view || '—' }}</span>
            <span class="pc-caret">{{ (v.positions && v.positions.length) ? (open === v.wallet ? '▲' : '▼ ' + v.positions.length) : '' }}</span>
          </div>
          <div v-if="open === v.wallet && v.positions && v.positions.length" class="tblwrap">
            <table class="grid pc-pos">
              <thead><tr><th>市場</th><th>到期</th><th>方向</th><th class="r">部位</th><th class="r">現價</th><th class="r">損益</th></tr></thead>
              <tbody>
                <tr v-for="(p, j) in v.positions" :key="j">
                  <td class="pc-mkt"><img v-if="p.icon" :src="p.icon" class="pc-icon" alt="" /><span :title="p.title">{{ prettyTitle(p.title) }}</span></td>
                  <td class="pc-date mono tsmall">{{ fmtExpiry(p) }}</td>
                  <td><span class="dir" :class="/^(yes|up|long)$/i.test(p.outcome) ? 'short' : 'long'">{{ p.outcome }}</span></td>
                  <td class="r mono">{{ fmtUsd(p.cur_value) }}</td>
                  <td class="r mono tsmall">{{ cents(p.cur_price) }}</td>
                  <td class="r mono" :class="p.cash_pnl >= 0 ? 'short' : 'long'">{{ fmtUsd(p.cash_pnl) }} <small>{{ pct(p.percent_pnl) }}</small></td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </template>

    <p class="ps-note">⚠️ 僅供研究,非投資建議。方向由 AI 依公開倉位判讀,可能有誤;價格單位為「分」(每股 0~1 美元)。</p>
  </section>
</template>

<style scoped>
/* 本站無 CSS 變數,全部實色(對齊 App.vue:金 #e8b84b、綠 #2ec26b、紅 #ff5c5c) */
.mk-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap; }
.pc-head-r { display: flex; align-items: center; gap: 10px; }
.mk-count { font-size: 11px; color: #6a6f7a; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.ps-refresh { background: #1b1e25; color: #cdd0d6; border: 1px solid #23262d; border-radius: 8px; padding: 5px 11px; font-weight: 700; font-size: 13px; cursor: pointer; }
.ps-refresh:disabled { opacity: .55; cursor: default; }
.wl-admin { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; background: #0f1116; border: 1px solid #23262d; border-radius: 10px; padding: 9px 14px; margin: 10px 0 14px; }
.wl-toggle { display: flex; align-items: center; gap: 7px; font-size: 13px; font-weight: 600; color: #e8eaed; cursor: pointer; }
.wl-toggle small { color: #8b909a; font-weight: 400; }
.wl-adnote { font-size: 11px; color: #6a6f7a; }
.ps-err { font-size: 13px; color: #ff5c5c; background: #241419; border: 1px solid #4a2027; border-radius: 8px; padding: 10px 12px; }
.loading { font-size: 13px; color: #8b909a; padding: 16px 4px; }
/* 整體大綱 */
.pc-overall { background: #14161b; border: 1px solid #23262d; border-left: 3px solid #e8b84b; border-radius: 12px; padding: 14px 16px; margin-bottom: 16px; }
.pc-badges { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; flex-wrap: wrap; }
.pc-lean { font-size: 14px; font-weight: 800; border-radius: 7px; padding: 3px 12px; }
.pc-conf { font-size: 12px; color: #8b909a; } .pc-conf b { color: #e8eaed; font-size: 13px; }
.pc-summary { font-size: 14px; line-height: 1.7; color: #e8eaed; margin: 0; white-space: pre-wrap; }
.l-up { color: #2ec26b; background: #10261c; } .l-dn { color: #ff5c5c; background: #241419; }
.l-range { color: #e8b84b; background: rgba(232,184,75,.12); } .l-mut { color: #8b909a; background: #1b1e25; }
/* 個別列表 */
.psub { margin: 0 0 10px; } .wl-sub { margin-left: 10px; font-size: 11px; font-weight: 400; color: #6a6f7a; }
.pc-list { display: flex; flex-direction: column; gap: 6px; }
.pc-item { background: #14161b; border: 1px solid #23262d; border-radius: 10px; overflow: hidden; }
.pc-row { display: flex; align-items: center; gap: 10px; padding: 9px 12px; cursor: pointer; }
.pc-row:hover { background: #191c22; }
.pc-row.open { background: #191c22; border-bottom: 1px solid #23262d; }
.pc-idx { font-size: 11px; color: #6a6f7a; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; width: 20px; flex: 0 0 auto; text-align: right; }
.pc-name { font-weight: 700; color: #e8eaed; font-size: 13.5px; white-space: nowrap; max-width: 150px; overflow: hidden; text-overflow: ellipsis; flex: 0 0 auto; }
.ps-v { font-size: 11px; font-weight: 700; border-radius: 5px; padding: 2px 7px; white-space: nowrap; flex: 0 0 auto; }
.v-good { color: #2ec26b; background: #10261c; } .v-warn { color: #e8b84b; background: rgba(232,184,75,.12); }
.v-bad { color: #ff5c5c; background: #241419; } .v-mut { color: #8b909a; background: #1b1e25; }
.pc-leanchip { font-size: 11px; font-weight: 700; border-radius: 5px; padding: 2px 8px; white-space: nowrap; flex: 0 0 auto; }
.pc-view { font-size: 13px; color: #cdd0d6; flex: 1 1 auto; min-width: 0; }
.pc-caret { font-size: 11px; color: #8b909a; white-space: nowrap; flex: 0 0 auto; }
.tblwrap { overflow-x: auto; -webkit-overflow-scrolling: touch; max-width: 100%; padding: 4px 12px 10px; }
.pc-pos { width: 100%; min-width: 560px; }
.pc-pos .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.pc-pos th, .pc-pos td { white-space: nowrap; }
.pc-mkt { white-space: normal; min-width: 170px; max-width: 290px; display: flex; align-items: center; gap: 7px; color: #e8eaed; }
.pc-icon { width: 18px; height: 18px; border-radius: 4px; flex: 0 0 auto; object-fit: cover; }
.pc-date { color: #8b909a; }
.pc-pos small { color: #6a6f7a; font-size: 10.5px; }
.up { color: #2ec26b; } .dn { color: #ff5c5c; }
.ps-note { font-size: 11px; color: #6a6f7a; margin: 14px 0 0; }
/* 窄螢幕:個別列自動換行,不擠爆 */
@media (max-width: 640px) {
  .pc-row { flex-wrap: wrap; }
  .pc-view { flex-basis: 100%; order: 5; }
}
</style>
