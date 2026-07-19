<!--
  財經事件(公開)。資料由外層持有 —— 導覽列徽章要顯示「未公布」筆數,
  分頁沒開的時候也得算得出來,所以不搬進元件。
-->
<script setup>
defineProps({ eventList: { type: Array, default: () => [] } })
</script>

<template>
  <section>
  <div class="mk-head">
    <h2>財經事件(高影響 · 美國)<span class="help" tabindex="0">?<span class="help-pop">高影響美國經濟事件。這是唯一能「事前」的——<b>事件前可降風險、預期波動</b>。釋出後顯示「實際 vs 預期」(實際優於預期通常利多風險資產)。時間為你的本地時區。⚠️ 約 30 分鐘更新一次。</span></span></h2>
    <span class="mk-count">CPI / FOMC / 非農… · 共 {{ eventList.length }} 筆</span>
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
