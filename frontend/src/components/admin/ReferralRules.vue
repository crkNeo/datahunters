<!--
  後台:推廣規則與獎勵制度 —— 會員在「我的推廣」點入口看到的說明文。

  和登入公告的差別是這裡沒有到期時間,改用明確的「發佈」開關:未發佈時會員那邊
  拿到的是空的(後端擋掉,草稿不外流),入口也會自動隱藏。所以可以先存草稿慢慢改。
-->
<script setup>
import { ref, onMounted, computed } from 'vue'
import { authFetch } from '../../lib/api'

const emit = defineEmits(['msg', 'toast'])

const form = ref({ title: '', text: '', published: false })
const saved = ref({ title: '', text: '', published: false, ver: 0 })
const busy = ref(false)

// 有沒有未儲存的變更 —— 避免管理員改了字卻忘了按儲存就離開分頁。
const dirty = computed(() =>
  form.value.title !== saved.value.title ||
  form.value.text !== saved.value.text ||
  form.value.published !== saved.value.published)

function fmtVer(ms) {
  if (!ms) return '尚未儲存過'
  return new Date(ms).toLocaleString('zh-TW', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })
}

async function load() {
  try {
    const res = await authFetch('/api/admin/referral-rules')
    if (!res.ok) return
    const d = await res.json()
    saved.value = { title: d.title || '', text: d.text || '', published: !!d.published, ver: d.ver || 0 }
    form.value = { title: saved.value.title, text: saved.value.text, published: saved.value.published }
  } catch (e) { /* secondary */ }
}
onMounted(load)

// publish 傳 true/false 覆寫發佈狀態(發佈鈕/下架鈕),不傳就沿用目前的開關。
async function save(publish) {
  if (busy.value) return
  const published = publish === undefined ? form.value.published : publish
  if (published && !form.value.text.trim()) { emit('toast', '內容是空的,無法發佈', 'err'); return }
  busy.value = true
  try {
    const res = await authFetch('/api/admin/referral-rules', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title: form.value.title, text: form.value.text, published }),
    })
    if (res.ok) {
      form.value.published = published
      await load()
      emit('msg', published ? '✓ 已發佈,會員現在看得到' : '✓ 已儲存草稿(會員看不到)')
    } else emit('toast', (await res.text()).trim() || '儲存失敗', 'err')
  } catch (e) { emit('toast', '儲存失敗', 'err') }
  busy.value = false
}
</script>

<template>
<section class="card adminbox">
  <div class="mk-head">
    <h3 class="psub">
      📜 推廣規則與獎勵制度
      <em class="rrstate" :class="saved.published ? 'on' : 'off'">{{ saved.published ? '已發佈' : '草稿' }}</em>
    </h3>
    <span class="mk-count">最後更新 {{ fmtVer(saved.ver) }}</span>
  </div>
  <p class="refhint">
    會員在「我的推廣」標題下方會看到入口按鈕,點開是彈窗。未發佈時入口自動隱藏,
    草稿內容也不會傳到前台。內容支援換行,空一行等於分段。
  </p>

  <div class="cfg-row">
    <span class="cfg-k">標題</span>
    <input class="authin" v-model="form.title" maxlength="60" placeholder="例:推廣規則與獎勵制度" />
  </div>
  <div class="cfg-row">
    <span class="cfg-k">內容</span>
    <textarea class="authin rrtext" v-model="form.text" maxlength="8000" rows="16"
      placeholder="🟢 兌換門檻:&#10;每累積滿 10 積分(即 10 位合格受邀戶),即可於後台申請兌換 30 USDT 獎金。&#10;..."></textarea>
  </div>
  <div class="rrfoot">
    <span class="tsmall">{{ form.text.length }} / 8000 字<em v-if="dirty" class="rrdirty">· 有未儲存的變更</em></span>
    <div class="rrbtns">
      <button class="minibtn" :disabled="busy" @click="save(false)">存成草稿</button>
      <button v-if="saved.published" class="minibtn warn" :disabled="busy" @click="save(false)">下架</button>
      <button class="okbtn" :disabled="busy" @click="save(true)">發佈</button>
    </div>
  </div>

  <template v-if="form.text.trim()">
    <h4 class="refh4">預覽(會員看到的樣子)</h4>
    <div class="rrpreview">
      <h5 v-if="form.title">{{ form.title }}</h5>
      <p v-for="(para, i) in form.text.split(/\n{2,}/)" :key="i" class="rrpara">{{ para }}</p>
    </div>
  </template>
</section>
</template>

<style scoped>
.rrstate { font-size: 11px; padding: 2px 8px; border-radius: 6px; margin-left: 8px; font-style: normal; }
.rrstate.on { background: #103a24; color: #2ec26b; }
.rrstate.off { background: #2a2410; color: #f4d774; }
.rrtext { font-family: inherit; line-height: 1.7; resize: vertical; }
.rrfoot { display: flex; justify-content: space-between; align-items: center; gap: 10px; flex-wrap: wrap; margin-top: 8px; }
.rrbtns { display: flex; gap: 8px; }
.rrdirty { color: #f4d774; font-style: normal; margin-left: 6px; }
.minibtn.warn { color: #ff9b9b; border-color: #4a2020; }
.rrpreview { background: #14171d; border: 1px solid #2a2f3a; border-radius: 10px; padding: 14px 16px; margin-top: 8px; }
.rrpreview h5 { margin: 0 0 10px; font-size: 15px; color: #e8eaee; }
/* white-space: pre-line 讓單一換行也保留,段落之間才靠 <p> 的間距 */
.rrpara { margin: 0 0 10px; font-size: 13px; line-height: 1.8; color: #c8ccd4; white-space: pre-line; }
.rrpara:last-child { margin-bottom: 0; }
</style>
