<!--
  Robinhood 上架看板(公開)。資料由外層持有(導覽列徽章要顯示新上架數)。
-->
<script setup>
import { fundClock } from "../lib/format"
defineProps({ robinhood: { type: Object, default: null } })
</script>

<template>
  <section>
  <div class="mk-head">
    <h2>Robinhood 上架<span class="help" tabindex="0">?<span class="help-pop">監控 Robinhood 支援的加密幣清單,偵測到<b>新增可交易</b>的幣就即時推播(TG + 軟體)。Robinhood 上架常帶動幣價。清單即為目前可在 Robinhood 交易的幣;<b class="new-dot">新</b> 標記為近期新增。⚠️ 來源為 Robinhood 公開端點,僅供參考、非投資建議。</span></span></h2>
    <span class="mk-count" v-if="robinhood && robinhood.updated_at">來源 Robinhood · {{ robinhood.coins.length }} 檔可交易 · {{ fundClock(new Date(robinhood.updated_at).getTime()) }} 更新</span>
  </div>
  <div v-if="robinhood && robinhood.coins.length" class="rh-grid">
    <div v-for="c in robinhood.coins" :key="c.code" class="rh-card" :class="{ isnew: c.new }">
      <div class="rh-code">{{ c.code }}<span v-if="c.new" class="rh-new">新</span></div>
      <div class="rh-name">{{ c.name }}</div>
      <div class="rh-sym">{{ c.symbol }}</div>
    </div>
  </div>
  <p v-else class="loading">載入 Robinhood 上架清單中…(首個週期後建立基準)</p>
  </section>
</template>
