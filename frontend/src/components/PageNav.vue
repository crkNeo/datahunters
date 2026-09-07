<!--
  PageNav —— 純分頁列(伺服器分頁用):顯示「第 X / Y 頁 · 共 N 筆」+ 首/上/下/末頁。
  自己不切資料;父層拿到 page 事件去跟後端要那一頁。
-->
<script setup>
const props = defineProps({
  page: { type: Number, default: 1 },
  pages: { type: Number, default: 1 },
  total: { type: Number, default: 0 },
})
const emit = defineEmits(['go'])
function go(p) {
  const n = Math.min(props.pages, Math.max(1, Math.floor(Number(p) || 1)))
  if (n !== props.page) emit('go', n)
}
</script>

<template>
  <div v-if="total > 0" class="pager">
    <button class="pgbtn" :disabled="page <= 1" @click="go(1)">«</button>
    <button class="pgbtn" :disabled="page <= 1" @click="go(page - 1)">‹</button>
    <span class="pginfo">第 <b>{{ page }}</b> / {{ pages }} 頁 · 共 <b>{{ total }}</b> 筆</span>
    <button class="pgbtn" :disabled="page >= pages" @click="go(page + 1)">›</button>
    <button class="pgbtn" :disabled="page >= pages" @click="go(pages)">»</button>
  </div>
</template>

<style scoped>
.pager { display: flex; align-items: center; justify-content: center; gap: 8px; margin: 12px 0 4px; flex-wrap: wrap; }
.pgbtn { min-width: 34px; height: 30px; padding: 0 8px; border-radius: 7px; background: #1b1e25; border: 1px solid #2f3540; color: #c8ccd4; font-size: 14px; font-weight: 700; cursor: pointer; }
.pgbtn:disabled { opacity: .35; cursor: default; }
.pgbtn:not(:disabled):hover { background: #262b34; border-color: #3a4150; }
.pginfo { font-size: 12.5px; color: #9aa0aa; padding: 0 4px; }
.pginfo b { color: #e8eaed; }
</style>
