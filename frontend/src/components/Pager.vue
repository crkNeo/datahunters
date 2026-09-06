<!--
  Pager —— 前端分頁(切已載入/篩選後的陣列)。
  用法:<Pager :items="rows" :size="50" v-slot="{ items }"> …用 items 渲染這一頁… </Pager>
  底部固定顯示「第 X / Y 頁 · 共 N 筆」+ 上/下一頁 + 跳頁。資料換了(例如時間濾網)自動回第 1 頁。
-->
<script setup>
import { ref, computed, watch } from 'vue'

const props = defineProps({
  items: { type: Array, default: () => [] },
  size: { type: Number, default: 50 },
})

const page = ref(1)
const total = computed(() => props.items.length)
const pages = computed(() => Math.max(1, Math.ceil(total.value / props.size)))
const slice = computed(() => props.items.slice((page.value - 1) * props.size, page.value * props.size))

watch(() => props.items, () => { page.value = 1 }) // 資料變(篩選/重載)→ 回第 1 頁
watch(pages, (p) => { if (page.value > p) page.value = p })

function go(p) {
  const n = Number(p)
  if (!Number.isFinite(n)) return
  page.value = Math.min(pages.value, Math.max(1, Math.floor(n)))
}
</script>

<template>
  <slot :items="slice" :page="page" :pages="pages" :total="total" />
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
