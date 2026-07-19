<!--
  標籤權限 —— 設定各身分組看得到哪些分頁。

  自己載入自己的資料。改動成功後 emit('changed'),讓外層重新抓一次公開的
  /api/tab-perms 以即時更新自己的導覽列。
-->
<script setup>
import { ref, onMounted } from 'vue'
import { authFetch } from '../../lib/api'

const emit = defineEmits(['changed', 'msg'])

const ROLE_CN = { public: '公開', member: '會員', vip: 'VIP', admin: '管理員' }
const ROLES = ['public', 'member', 'vip', 'admin']
const rows = ref([])
const busy = ref(false)

// quiet=true 是掛載時的首次載入(不需要「已重新載入」這種回饋)。
// 手動按刷新一定要給訊息 —— 重載後資料通常和原本一樣,沒有訊息就像按了沒反應。
async function load(quiet = false) {
  if (busy.value) return
  busy.value = true
  try {
    const res = await authFetch('/api/admin/tab-perms')
    if (res.ok) {
      rows.value = await res.json()
      if (!quiet) emit('msg', '✓ 已重新載入(共 ' + rows.value.length + ' 個標籤)')
    } else if (!quiet) {
      emit('msg', '✗ 載入失敗:' + ((await res.text()).trim() || ('HTTP ' + res.status)))
    }
  } catch (e) {
    if (!quiet) emit('msg', '✗ 載入失敗:連線異常')
  }
  busy.value = false
}

async function setRole(row, role) {
  if (row.locked || row.role === role) return
  const res = await authFetch('/api/admin/tab-perms', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ tab: row.tab, role }),
  })
  if (res.ok) {
    row.role = role
    emit('msg', '✓ ' + row.label + ' 已改為「' + ROLE_CN[role] + '」可見')
    emit('changed')
  } else {
    emit('msg', '✗ ' + row.label + ' 設定失敗')
  }
}

onMounted(() => load(true)) // 首次載入不出訊息
defineExpose({ load })
</script>

<template>
  <section class="card adminbox">
    <!-- 一定要寫 load() 而不是 load —— 直接傳函式會把 MouseEvent 當成 quiet 參數,
         quiet 變成 truthy,按了就不會有任何訊息(看起來就像沒反應)。 -->
    <h3 class="psub">
      標籤權限
      <button class="minibtn" :disabled="busy" @click="load()">{{ busy ? '刷新中…' : '刷新' }}</button>
    </h3>
    <div class="tabperms">
      <div v-for="row in rows" :key="row.tab" class="tabperm-row">
        <span class="tabperm-name">{{ row.label }}<em v-if="row.locked" class="tabperm-lock">🔒</em></span>
        <div class="tabperm-opts">
          <button v-for="r in ROLES" :key="r" class="roleopt"
            :class="{ on: row.role === r, dim: row.locked }"
            :disabled="row.locked" @click="setRole(row, r)">{{ ROLE_CN[r] }}</button>
        </div>
      </div>
    </div>
    <p class="loginhint">
      設定「最低身分」:選<b>公開</b>代表所有訪客都看得到,選 <b>VIP</b> 代表只有 VIP 與管理員看得到。<br />
      🔒 的項目(後台、推廣管理)<b>不可調整</b>。<br />
      此設定<b>同時控管後端 API</b> —— 不是只把分頁藏起來,調降後該身分組是真的拿不到資料。<br />
      ⚠️ 反之,把 VIP 分頁調成公開,等於<b>把該策略的進出場點位開放給所有人</b>,請確認後再改。
    </p>
  </section>
</template>
