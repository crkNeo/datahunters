<!--
  後台:登入公告彈窗 —— 設定會員登入時跳出的公告與到期時間。

  公告本體(notice)由外層維護,因為前台的彈窗也要用同一份;這裡只負責編輯,
  存檔後 emit('saved') 讓外層重新載入。
-->
<script setup>
import { ref, watch, inject } from 'vue'
import { authFetch } from '../../lib/api'

// App 提供的 in-app 確認框(手機/PWA 相容);拿不到時退回原生 confirm。
const askConfirm = inject('askConfirm', (m) => Promise.resolve(window.confirm(m)))

const props = defineProps({ notice: { type: Object, default: null } })
const emit = defineEmits(['saved', 'msg', 'toast'])

const noticeForm = ref({ title: '', text: '', until: '' })

function fundClock(ms) {
  if (!ms) return '—'
  return new Date(ms).toLocaleString('zh-TW', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })
}
// datetime-local 需要本地時區的 YYYY-MM-DDTHH:mm
function localInput(ms) {
  if (!ms) return ''
  return new Date(ms - new Date().getTimezoneOffset() * 60000).toISOString().slice(0, 16)
}
function loadNoticeEditor() {
  const n = props.notice || {}
  noticeForm.value = { title: n.title || '', text: n.text || '', until: localInput(n.expiry) }
}
watch(() => props.notice, loadNoticeEditor, { immediate: true })

async function saveNotice() {
  const f = noticeForm.value
  if (!f.text.trim()) { emit('toast', '內容必填', 'err'); return }
  const expiry = f.until ? new Date(f.until).getTime() : 0
  const res = await authFetch('/api/admin/notice', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title: f.title, text: f.text, expiry }),
  })
  if (res.ok) { emit('saved'); emit('msg', '✓ 已更新登入公告') }
  else emit('toast', (await res.text()).trim() || '儲存失敗', 'err')
}
async function clearNotice() {
  if (!(await askConfirm('確定停用登入公告?'))) return
  const res = await authFetch('/api/admin/notice', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title: '', text: '', expiry: 0 }),
  })
  if (res.ok) { noticeForm.value = { title: '', text: '', until: '' }; emit('saved'); emit('msg', '✓ 已停用登入公告') }
}
</script>

<template>
<section class="card adminbox">
  <h3 class="psub">登入公告彈窗 <button class="minibtn" @click="loadNoticeEditor">載入目前</button></h3>
  <div class="cfg-row">
    <span class="cfg-k">標題</span>
    <input class="authin" v-model="noticeForm.title" maxlength="60" placeholder="例:系統更新公告(選填)" />
  </div>
  <div class="cfg-row">
    <span class="cfg-k">內容</span>
    <textarea class="authin nb-edit" v-model="noticeForm.text" maxlength="2000" placeholder="輸入公告內容,例如更新事項、維護時間、活動通知…(支援換行)"></textarea>
  </div>
  <div class="cfg-row">
    <span class="cfg-k">顯示到</span>
    <input class="authin" type="datetime-local" v-model="noticeForm.until" />
    <span class="loginhint" style="margin:0">留空=不設期限;到期後自動不再顯示</span>
  </div>
  <div class="ae-addrow">
    <button class="loginbtn" @click="saveNotice">儲存並發佈</button>
    <button class="delbtn" @click="clearNotice">停用</button>
  </div>
  <p class="loginhint">
    會員登入時彈出;每位用戶可勾「不再顯示此則」關閉。
    <span v-if="notice && notice.active">目前<b class="long">顯示中</b><span v-if="notice.expiry"> · 到 {{ fundClock(notice.expiry) }}</span>。</span>
    <span v-else>目前<b class="short">未啟用</b>。</span>
    修改內容後會重新對所有人顯示一次。
  </p>
</section>
</template>
