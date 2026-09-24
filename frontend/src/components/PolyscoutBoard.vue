<!--
  跟單篩選(polyscout · 管理員)。自動抓 Polymarket CRYPTO 排行榜「當前前 10 名」,
  做跨區間一致性判定,並像名人動向一樣用標籤切換人 —— 點名字看那個錢包目前的預測市場持倉。
  ⚠️ 台灣部分 ISP 依法院命令封鎖 Polymarket 網域,後端已自帶 1.1.1.1 解析繞過。
  純資訊呈現,非投資建議、跟單風險自負。
-->
<script setup>
import { ref, computed, onMounted } from "vue"
import { authFetch } from "../lib/api"

const rows = ref([])
const loading = ref(false)
const err = ref("")
async function load() {
  loading.value = true; err.value = ""
  try {
    const res = await authFetch("/api/admin/polyscout?limit=10", { timeout: 60000 })
    if (res.ok) { rows.value = (await res.json()).reports || [] }
    else { err.value = (await res.text()).trim() || ("HTTP " + res.status); rows.value = [] }
  } catch (e) { err.value = "" + e } finally { loading.value = false }
}
onMounted(load)

const hasPos = (r) => !!(r && r.positions && r.positions.length)
function posVal(r) { return (r.positions || []).reduce((s, p) => s + p.cur_value, 0) }
// 標籤順序:有公開持倉的排前面(依持倉市值),其餘依後端排序(ALL 損益)在後
const ordered = computed(() => {
  const live = rows.value.filter(hasPos).sort((a, b) => posVal(b) - posVal(a))
  const flat = rows.value.filter((r) => !hasPos(r))
  return [...live, ...flat]
})
const selected = ref("")
const sel = computed(() => {
  const list = ordered.value
  if (!list.length) return null
  return list.find((r) => r.wallet === selected.value) || list[0]
})
const nameOf = (r) => r.name || (r.wallet.slice(0, 6) + "…" + r.wallet.slice(-4))

function fmtUsd(v) {
  const a = Math.abs(v); const s = v < 0 ? "-" : ""
  if (a >= 1e6) return s + "$" + (a / 1e6).toFixed(2) + "M"
  if (a >= 1e3) return s + "$" + (a / 1e3).toFixed(1) + "K"
  return s + "$" + a.toFixed(0)
}
const cents = (v) => (v * 100).toFixed(0) + "¢" // Polymarket 價格 0~1,以「分」呈現
const pct = (v) => (v >= 0 ? "+" : "") + v.toFixed(1) + "%"

// end_date 只有日期(YYYY-MM-DD),補 00:00:00 成標準時間格式呈現
function fmtTime(d) {
  return /^\d{4}-\d{2}-\d{2}$/.test(d || "") ? d + " 00:00:00" : (d || "—")
}
// 把英文市場名整理成看得懂的短標題;認不得的(如非幣市場)就保留原文
const COIN = { bitcoin: "BTC", ethereum: "ETH", solana: "SOL", ripple: "XRP", xrp: "XRP", dogecoin: "DOGE", cardano: "ADA", avalanche: "AVAX", chainlink: "LINK", polkadot: "DOT", litecoin: "LTC", "binance coin": "BNB", bnb: "BNB" }
function prettyTitle(t) {
  if (!t) return t
  const low = t.toLowerCase()
  let sym = null
  for (const k in COIN) if (low.includes(k)) { sym = COIN[k]; break }
  if (/up or down/i.test(t)) return (sym || "?") + " 短線漲跌"
  const m = t.match(/\$[\d,]+(?:\.\d+)?[KMB]?(?![A-Za-z])/i) // 價位;避免吃到後面單字的字母
  if (sym && m) {
    if (/\b(dip|drop|fall|below|under)\b/i.test(t)) return `${sym} 會跌到 ${m[0]}?`
    if (/\b(reach|hit|hits|above|exceed|over)\b/i.test(t)) return `${sym} 會漲到 ${m[0]}?`
  }
  return t
}
const vclass = { "長期穩定": "v-good", "短期爆發": "v-warn", "近期轉弱": "v-bad", "資料不足": "v-mut", "觀察中": "v-mut" }
</script>

<template>
  <section>
    <div class="mk-head">
      <h2>跟單篩選 <span class="ps-tag">Polymarket · CRYPTO</span>
        <span class="help" tabindex="0">?<span class="help-pop">自動抓 CRYPTO 排行榜<b>當前前 10 名</b>,比對 DAY/WEEK/MONTH/ALL 四區間損益做<b>一致性判定</b>(長期穩定 vs 短期爆發)。點名字看那個錢包<b>目前的預測市場持倉</b>(押哪個市場、方向、進場價、損益)。<br>⚠️ 純資訊呈現,<b>非投資建議、跟單風險自負</b>。</span></span>
      </h2>
      <button class="ps-refresh" :disabled="loading" @click="load">{{ loading ? '更新中…' : '↻ 重新整理' }}</button>
    </div>

    <p v-if="err" class="ps-err">✕ {{ err }}</p>
    <p v-else-if="loading && !rows.length" class="loading">載入排行榜與持倉中…（約需數秒）</p>

    <!-- 標籤切換人:綠點=有公開持倉;點名字切換,下方只顯示這一位 -->
    <div v-if="rows.length" class="wl-picker">
      <button v-for="r in ordered" :key="r.wallet" class="wl-pill" :class="{ on: sel && sel.wallet === r.wallet, live: hasPos(r) }" @click="selected = r.wallet">
        <i v-if="hasPos(r)" class="wl-dot"></i>{{ nameOf(r) }}
      </button>
    </div>

    <div v-if="sel" class="wl-card">
      <div class="wl-top">
        <div class="wl-who">
          <span class="wl-name">{{ nameOf(sel) }}<span class="ps-v" :class="vclass[sel.verdict]">{{ sel.verdict }}</span></span>
          <a class="wl-note" :href="'https://polymarket.com/profile/' + sel.wallet" target="_blank" rel="noopener">{{ sel.wallet.slice(0, 10) }}…{{ sel.wallet.slice(-6) }} ↗</a>
        </div>
        <div class="ps-stats">
          <span><i>ALL</i><b :class="sel.all_pnl >= 0 ? 'up' : 'dn'">{{ fmtUsd(sel.all_pnl) }}</b></span>
          <span><i>MONTH</i><b :class="sel.month_pnl >= 0 ? 'up' : 'dn'">{{ fmtUsd(sel.month_pnl) }}</b></span>
          <span><i>獲利區間</i><b>{{ sel.profitable_count }}/4</b></span>
          <span><i>近期佔比</i><b>{{ sel.all_pnl > 0 ? (sel.recent_share * 100).toFixed(0) + '%' : '—' }}</b></span>
        </div>
      </div>
      <p v-if="(sel.reasons || []).length" class="ps-reasons">{{ (sel.reasons || []).join('；') }}</p>

      <div v-if="hasPos(sel)" class="tblwrap">
        <table class="grid ps-pos">
          <thead><tr><th>市場</th><th>到期</th><th>方向</th><th class="r">部位</th><th class="r">進場</th><th class="r">現價</th><th class="r">損益</th></tr></thead>
          <tbody>
            <tr v-for="(p, i) in sel.positions" :key="i">
              <td class="ps-mkt"><img v-if="p.icon" :src="p.icon" class="ps-icon" alt="" /><span :title="p.title">{{ prettyTitle(p.title) }}</span></td>
              <td class="ps-date mono tsmall">{{ fmtTime(p.end_date) }}</td>
              <td><span class="dir" :class="/^(yes|up|long)$/i.test(p.outcome) ? 'short' : 'long'">{{ p.outcome }}</span></td>
              <td class="r mono">{{ fmtUsd(p.cur_value) }}</td>
              <td class="r mono tsmall">{{ cents(p.avg_price) }}</td>
              <td class="r mono tsmall">{{ cents(p.cur_price) }}</td>
              <td class="r mono" :class="p.cash_pnl >= 0 ? 'short' : 'long'">{{ fmtUsd(p.cash_pnl) }} <small>{{ pct(p.percent_pnl) }}</small></td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="wl-empty">目前無公開持倉（可能已結算、或倉位不在 Polymarket）</p>
    </div>

    <p class="ps-note">⚠️ 僅供研究,非投資建議。價格單位為「分」(Polymarket 每股 0~1 美元)。</p>
  </section>
</template>

<style scoped>
/* 本站無 CSS 變數,全部實色(對齊 App.vue:金 #e8b84b、綠 #2ec26b、紅 #ff5c5c) */
.ps-tag { margin-left: 8px; font-size: 11px; font-weight: 700; color: #e8b84b; background: rgba(232,184,75,.13); border: 1px solid #4a412a; border-radius: 6px; padding: 2px 7px; vertical-align: middle; }
.ps-refresh { background: #1b1e25; color: #cdd0d6; border: 1px solid #23262d; border-radius: 8px; padding: 6px 13px; font-weight: 700; font-size: 12.5px; cursor: pointer; }
.ps-refresh:disabled { opacity: .55; cursor: default; }
.ps-err { font-size: 13px; color: #ff5c5c; background: #241419; border: 1px solid #4a2027; border-radius: 8px; padding: 10px 12px; }
.loading { font-size: 13px; color: #8b909a; padding: 16px 4px; }
/* 標籤切換人 */
.wl-picker { display: flex; align-items: center; gap: 7px; flex-wrap: wrap; margin-bottom: 12px; }
.wl-pill { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; font-weight: 600; cursor: pointer;
  color: #8b909a; background: #1b1e25; border: 1px solid #23262d; border-radius: 20px; padding: 5px 13px; transition: all .12s; }
.wl-pill:hover { color: #e8eaed; border-color: #33383f; }
.wl-pill.live { color: #cdd0d6; }
.wl-pill.on { background: rgba(232,184,75,.14); border-color: #e8b84b; color: #e8b84b; font-weight: 800; }
.wl-dot { width: 7px; height: 7px; border-radius: 50%; background: #2ec26b; box-shadow: 0 0 6px rgba(46,194,107,.7); flex: 0 0 auto; }
.wl-card { background: #14161b; border: 1px solid #23262d; border-radius: 14px; padding: 14px 16px; min-width: 0; }
.wl-top { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; flex-wrap: wrap; margin-bottom: 8px; }
.wl-who { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.wl-name { display: inline-flex; align-items: center; gap: 8px; font-weight: 800; font-size: 17px; color: #e8b84b; }
.wl-note { font-size: 11px; color: #8b909a; text-decoration: none; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.wl-note:hover { color: #e8b84b; }
.ps-v { font-size: 11px; font-weight: 700; border-radius: 5px; padding: 2px 8px; }
.v-good { color: #2ec26b; background: #10261c; }
.v-warn { color: #e8b84b; background: rgba(232,184,75,.12); }
.v-bad { color: #ff5c5c; background: #241419; }
.v-mut { color: #8b909a; background: #1b1e25; }
.ps-stats { display: flex; gap: 16px; flex-wrap: wrap; text-align: right; }
.ps-stats span { display: flex; flex-direction: column; }
.ps-stats i { font-size: 10.5px; color: #6a6f7a; font-style: normal; }
.ps-stats b { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 14px; color: #e8eaed; }
.ps-reasons { font-size: 12px; color: #8b909a; margin: 0 0 12px; }
.up { color: #2ec26b; } .dn { color: #ff5c5c; }
.tblwrap { overflow-x: auto; -webkit-overflow-scrolling: touch; max-width: 100%; }
.ps-pos { width: 100%; min-width: 680px; }
.ps-pos .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.ps-pos th, .ps-pos td { white-space: nowrap; }
.ps-mkt { white-space: normal; min-width: 180px; max-width: 300px; display: flex; align-items: center; gap: 7px; color: #e8eaed; }
.ps-date { color: #8b909a; }
.ps-icon { width: 18px; height: 18px; border-radius: 4px; flex: 0 0 auto; object-fit: cover; }
.ps-pos small { color: #6a6f7a; font-size: 10.5px; }
.wl-empty { font-size: 13px; color: #8b909a; padding: 14px 4px; margin: 0; }
.ps-note { font-size: 11px; color: #6a6f7a; margin: 12px 0 0; }
</style>
