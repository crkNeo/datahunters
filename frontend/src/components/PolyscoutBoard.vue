<!--
  跟單篩選(polyscout · 管理員)。Polymarket CRYPTO 排行榜前 N 名的「跨區間一致性」篩選。
  按需觸發(整份掃描 ~N×5 次請求偏慢)。⚠️ Polymarket 對美國 IP 封鎖,後端連不到會回錯誤。
-->
<script setup>
import { ref } from "vue"
import { authFetch } from "../lib/api"

const limit = ref(20)
const rows = ref([])
const loading = ref(false)
const err = ref("")
async function run() {
  loading.value = true; err.value = ""
  try {
    const res = await authFetch("/api/admin/polyscout?limit=" + limit.value)
    if (res.ok) { rows.value = (await res.json()).reports || [] }
    else { err.value = (await res.text()).trim() || ("HTTP " + res.status); rows.value = [] }
  } catch (e) { err.value = "" + e } finally { loading.value = false }
}
function fmtUsd(v) {
  const a = Math.abs(v)
  const s = v < 0 ? "-" : ""
  if (a >= 1e6) return s + "$" + (a / 1e6).toFixed(2) + "M"
  if (a >= 1e3) return s + "$" + (a / 1e3).toFixed(1) + "K"
  return s + "$" + a.toFixed(0)
}
const vclass = { "長期穩定": "v-good", "短期爆發": "v-warn", "近期轉弱": "v-bad", "資料不足": "v-mut", "觀察中": "v-mut" }
</script>

<template>
  <section>
    <div class="ps-head">
      <h2>跟單篩選 <span class="ps-tag">Polymarket · CRYPTO</span></h2>
      <div class="ps-ctrl">
        <label>前 <input type="number" v-model.number="limit" min="1" max="50" class="ps-num" /> 名</label>
        <button class="ps-btn" :disabled="loading" @click="run">{{ loading ? '篩選中…' : '開始篩選' }}</button>
      </div>
    </div>
    <p class="ps-note">依加密貨幣類排行榜跨 DAY/WEEK/MONTH/ALL 的一致性判定。⚠️ 僅供研究,非投資建議。</p>

    <p v-if="err" class="ps-err">✕ {{ err }}</p>

    <div v-if="rows.length" class="tblwrap">
      <table class="ps-tbl">
        <thead><tr><th>#</th><th>名稱 / 錢包</th><th class="r">ALL 損益</th><th class="r">MONTH 損益</th><th class="r">獲利區間</th><th class="r">近期佔比</th><th>判定</th><th>原因</th></tr></thead>
        <tbody>
          <tr v-for="(r, i) in rows" :key="r.wallet">
            <td class="mut">{{ i + 1 }}</td>
            <td><span v-if="r.name" class="ps-name">{{ r.name }}</span><span class="ps-addr">{{ r.wallet.slice(0, 6) }}…{{ r.wallet.slice(-4) }}</span></td>
            <td class="r mono" :class="r.all_pnl >= 0 ? 'up' : 'dn'">{{ fmtUsd(r.all_pnl) }}</td>
            <td class="r mono" :class="r.month_pnl >= 0 ? 'up' : 'dn'">{{ fmtUsd(r.month_pnl) }}</td>
            <td class="r mono">{{ r.profitable_count }}/4</td>
            <td class="r mono">{{ r.all_pnl ? (r.recent_share * 100).toFixed(0) + '%' : '—' }}</td>
            <td><span class="ps-v" :class="vclass[r.verdict]">{{ r.verdict }}</span></td>
            <td class="ps-reasons">{{ (r.reasons || []).join('；') }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-else-if="!loading && !err" class="ps-empty">按「開始篩選」抓取排行榜並判定。</p>
  </section>
</template>

<style scoped>
.ps-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; margin-bottom: 6px; }
.ps-head h2 { font-size: 18px; font-weight: 800; color: #e8eaed; margin: 0; }
.ps-tag { margin-left: 8px; font-size: 11px; font-weight: 700; color: #e8b84b; background: rgba(232,184,75,.13); border: 1px solid #4a412a; border-radius: 6px; padding: 2px 7px; vertical-align: middle; }
.ps-ctrl { display: flex; align-items: center; gap: 10px; font-size: 13px; color: #cdd0d6; }
.ps-num { width: 54px; background: #1b1e25; border: 1px solid #23262d; color: #e8eaed; border-radius: 7px; padding: 4px 8px; font-family: ui-monospace, monospace; }
.ps-btn { background: #e8b84b; color: #1a1408; border: none; border-radius: 8px; padding: 7px 15px; font-weight: 800; font-size: 13px; cursor: pointer; }
.ps-btn:disabled { opacity: .55; cursor: default; }
.ps-note { font-size: 12px; color: #8b909a; margin: 0 0 12px; }
.ps-err { font-size: 13px; color: #ff5c5c; background: #241419; border: 1px solid #4a2027; border-radius: 8px; padding: 10px 12px; }
.ps-empty { font-size: 13px; color: #8b909a; padding: 18px 4px; margin: 0; }
.tblwrap { overflow-x: auto; -webkit-overflow-scrolling: touch; max-width: 100%; }
.ps-tbl { width: 100%; min-width: 720px; border-collapse: collapse; }
.ps-tbl th { text-align: left; font-size: 11px; font-weight: 600; color: #8b909a; padding: 6px 10px; border-bottom: 1px solid #23262d; white-space: nowrap; }
.ps-tbl th.r { text-align: right; }
.ps-tbl td { padding: 8px 10px; border-bottom: 1px solid #1b1e25; font-size: 12.5px; color: #cdd0d6; white-space: nowrap; }
.ps-tbl td.r { text-align: right; }
.ps-tbl .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.mut { color: #6a6f7a; } .up { color: #2ec26b; } .dn { color: #ff5c5c; }
.ps-name { font-weight: 700; color: #e8eaed; margin-right: 6px; }
.ps-addr { font-family: ui-monospace, monospace; font-size: 11px; color: #8b909a; }
.ps-v { font-size: 12px; font-weight: 700; border-radius: 5px; padding: 2px 8px; }
.v-good { color: #2ec26b; background: #10261c; }
.v-warn { color: #e8b84b; background: rgba(232,184,75,.12); }
.v-bad { color: #ff5c5c; background: #241419; }
.v-mut { color: #8b909a; background: #1b1e25; }
.ps-reasons { color: #8b909a; font-size: 11.5px; white-space: normal; min-width: 200px; }
</style>
