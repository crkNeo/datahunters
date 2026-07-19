<!--
  後台:即時推播 —— 立即發 Web Push 給指定用戶組,可選擇點擊後跳轉到某篇文章。

  文章清單由外層傳入(App.vue 本來就會載),避免重複打一次 /api/articles。
-->
<script setup>
import { ref } from 'vue'
import { authFetch } from '../../lib/api'

defineProps({ articles: { type: Array, default: () => [] } })
const emit = defineEmits(['toast'])

const bcTitle = ref('')
const bcBody = ref('')
const bcGroup = ref('admin')
const bcArticle = ref('') // '' = 不跳轉;否則帶文章 id,點推播直接開該篇

async function sendBroadcast() {
  if (!bcTitle.value.trim() || !bcBody.value.trim()) { emit('toast', '標題與內容必填', 'err'); return }
  const res = await authFetch('/api/admin/push-broadcast', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title: bcTitle.value.trim(), body: bcBody.value.trim(), group: bcGroup.value, article: bcArticle.value }),
  })
  if (!res.ok) { emit('toast', (await res.text()).trim() || '推播失敗', 'err'); return }
  const d = await res.json()
  emit('toast', '已推播給 ' + d.sent + ' 個裝置', 'ok')
  bcTitle.value = ''; bcBody.value = ''
}
</script>

<template>
<section class="card adminbox">
  <h3 class="psub">即時推播</h3>
  <div class="cfg-row">
    <span class="cfg-k">標題</span>
    <input class="authin" v-model="bcTitle" maxlength="20" placeholder="例:測試" />
  </div>
  <div class="cfg-row">
    <span class="cfg-k">內容</span>
    <input class="authin" v-model="bcBody" maxlength="20" placeholder="20 字內,例:我只是個測試文" />
  </div>
  <div class="cfg-row">
    <span class="cfg-k">用戶組</span>
    <select class="authin cfg-sel" v-model="bcGroup">
      <option value="all">全部</option>
      <option value="member">會員</option>
      <option value="vip">VIP</option>
      <option value="admin">管理員</option>
    </select>
  </div>
  <div class="cfg-row">
    <span class="cfg-k">點擊跳轉</span>
    <select class="authin cfg-sel" v-model="bcArticle">
      <option value="">不跳轉(開啟首頁)</option>
      <option v-for="a in articles" :key="a.id" :value="String(a.id)">📄 {{ a.title }}</option>
    </select>
    <button class="loginbtn" @click="sendBroadcast">發送推播</button>
  </div>
  <p class="loginhint">立即發送 Web Push 給選定用戶組(標題/內容各上限 20 字);可選點擊後跳轉到指定文章。僅發給已開啟通知的裝置。</p>
</section>
</template>
