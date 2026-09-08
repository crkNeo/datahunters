<!--
  財經事件(公開)。資料由外層持有 —— 導覽列徽章要顯示「未公布」筆數,
  分頁沒開的時候也得算得出來,所以不搬進元件。
-->
<script setup>
import { computed } from 'vue'
import { fmtClock } from '../lib/format'

const props = defineProps({
  eventList: { type: Array, default: () => [] },
  risk: { type: Object, default: null }, // 風險背景燈(美股風險/紐約盤/VIX)
})

// 6 小時內要發布的事件加高亮(倒數只有分鐘時 parseInt 得 0,一樣算「快到了」)
function evSoon(e) {
  if (e.released || !e.countdown) return false
  const h = e.countdown.includes('h') ? parseInt(e.countdown) : 0
  return h < 6
}

// 風險背景燈(依 special.html mock):風險偏好/趨避 + 紐約盤 + VIX + 下一筆重要數據
const riskText = computed(() => {
  const r = props.risk && props.risk.risk
  return r === 'risk-on' ? '風險偏好' : r === 'risk-off' ? '風險趨避' : '中性'
})
const riskLight = computed(() => {
  const r = props.risk && props.risk.risk
  return r === 'risk-off' ? 'off' : r === 'risk-on' ? 'on' : 'mid'
})
const vixItem = computed(() => (props.risk && props.risk.items || []).find((i) => i.name === 'VIX') || null)
const nextEvent = computed(() => props.eventList.find((e) => !e.released) || null)
</script>

<template>
  <section>
  <div class="mk-head">
    <h2>財經事件(高影響 · 美國)<span class="help" tabindex="0">?<span class="help-pop">高影響美國經濟事件。這是唯一能「事前」的——<b>事件前可降風險、預期波動</b>。釋出後顯示「實際 vs 預期」(實際優於預期通常利多風險資產)。時間為你的本地時區。⚠️ 約 30 分鐘更新一次。</span></span></h2>
    <span class="mk-count">CPI / FOMC / 非農… · 共 {{ eventList.length }} 筆</span>
  </div>
  <!-- 風險背景燈(依 mock):美股風險 + 紐約盤 + VIX + 下一筆重要數據 -->
  <div v-if="risk" class="ev-riskbar">
    <span class="ev-light" :class="riskLight"><span class="d"></span>美股風險:{{ riskText }}</span>
    <span class="ev-rr">紐約盤 <b>{{ risk.us_status || '—' }}</b><template v-if="risk.countdown"> · {{ risk.countdown }}</template></span>
    <span v-if="vixItem" class="ev-rr">VIX <b :class="vixItem.chg_pct >= 0 ? 'short' : 'long'">{{ vixItem.chg_pct >= 0 ? '+' : '' }}{{ vixItem.chg_pct }}%</b></span>
    <span v-if="risk.high_impact" class="ev-rr hot">⚠️ 高影響時段</span>
    <span v-if="nextEvent" class="ev-next">📅 下一筆 <b>{{ nextEvent.title }}</b> · <span class="cd">{{ nextEvent.countdown }}</span></span>
  </div>
  <table v-if="eventList.length" class="grid">
    <thead><tr><th>時間</th><th>事件</th><th class="r">狀態</th><th class="r">前值</th><th class="r">預期</th><th class="r">實際</th></tr></thead>
    <tbody>
      <tr v-for="(e, i) in eventList" :key="i" :class="{ 'ev-done': e.released, 'ev-soon': evSoon(e) }">
        <td class="tsmall">{{ fmtClock(e.time) }}</td>
        <td>{{ e.title }}</td>
        <td class="r">
          <span v-if="e.released" class="otag expired">已釋出</span>
          <span v-else class="ev-cd">⏳ {{ e.countdown }}</span>
        </td>
        <td class="r tsmall">{{ e.previous || '—' }}</td>
        <td class="r tsmall">{{ e.forecast || '—' }}</td>
        <td class="r"><b v-if="e.actual" :class="e.actual === e.forecast ? '' : 'hot'">{{ e.actual }}</b><span v-else>—</span></td>
      </tr>
    </tbody>
  </table>
  <p v-else class="loading">載入經濟行事曆中…(若持續空白,可能本週無高影響美國事件)</p>
  </section>
</template>

<style scoped>
/* 風險背景燈列(依 special.html mock)*/
.ev-riskbar{ display:flex; align-items:center; gap:12px 16px; background:var(--c-bg2); border:1px solid var(--c-line); border-radius:var(--r-md); padding:10px 15px; margin-bottom:14px; flex-wrap:wrap; }
.ev-light{ display:flex; align-items:center; gap:7px; font-size:12.5px; font-weight:600; font-family:var(--f-disp); }
.ev-light .d{ width:10px; height:10px; border-radius:50%; }
.ev-light.on{ color:var(--c-up); } .ev-light.on .d{ background:var(--c-up); box-shadow:0 0 8px var(--c-up); }
.ev-light.off{ color:var(--c-dn); } .ev-light.off .d{ background:var(--c-dn); box-shadow:0 0 8px var(--c-dn); }
.ev-light.mid{ color:var(--c-gold-b); } .ev-light.mid .d{ background:var(--c-gold); box-shadow:0 0 8px var(--c-gold); }
.ev-rr{ font-size:12px; color:var(--c-mut); }
.ev-rr b{ color:var(--c-txt); font-family:var(--f-mono); }
.ev-rr b.long{ color:var(--c-up); } .ev-rr b.short{ color:var(--c-dn); }
.ev-rr.hot{ color:var(--c-dn); font-weight:600; }
.ev-next{ margin-left:auto; font-size:12px; color:var(--c-mut); }
.ev-next b{ color:var(--c-txt); font-family:var(--f-disp); }
.ev-next .cd{ font-family:var(--f-mono); color:var(--c-gold-b); font-weight:600; }
@media (max-width:768px){ .ev-next{ margin-left:0; } }
</style>
