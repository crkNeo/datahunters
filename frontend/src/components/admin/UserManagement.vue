<!--
  後台:使用者管理 —— 待審核、名單(篩選/排序)、手動新增。

  users 由外層持有並傳入:導覽列的徽章與輪詢迴圈也要用同一份,不適合搬進來。
  這個元件負責畫面與各種異動,改完 emit('reload') 讓外層重抓。
-->
<script setup>
import { ref, computed, inject } from 'vue'
import { authFetch } from '../../lib/api'

// App 提供的 in-app 確認框(手機/PWA 相容);拿不到時退回原生 confirm。
const askConfirm = inject('askConfirm', (m) => Promise.resolve(window.confirm(m)))

const props = defineProps({
  users: { type: Array, default: () => [] },
  currentUser: { type: String, default: '' },
})
const emit = defineEmits(['reload', 'msg', 'proof', 'refof'])

const newUser = ref({ u: '', p: '', role: 'member', status: 'active' })

async function createUser() {
  if (!newUser.value.u || !newUser.value.p) { emit('msg', '帳號與密碼必填'); return }
  const res = await authFetch('/api/admin/users', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      username: newUser.value.u, password: newUser.value.p,
      role: newUser.value.role, status: newUser.value.status,
    }),
  })
  if (res.ok) {
    emit('msg', '✓ 已新增 ' + newUser.value.u)
    newUser.value = { u: '', p: '', role: 'member', status: 'active' }
    emit('reload')
  } else {
    emit('msg', '✗ ' + (await res.text()))
  }
}

async function updateUser(u) {
  const res = await authFetch('/api/admin/users', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: u.username, role: u.role, status: u.status }),
  })
  emit('msg', res.ok ? '✓ 已更新 ' + u.username : '✗ 更新失敗')
  emit('reload')
}

async function approveUser(u) { u.status = 'active'; await updateUser(u) }
async function rejectUser(u) { u.status = 'banned'; await updateUser(u) }
async function toggleEnabled(u) { u.status = u.status === 'active' ? 'banned' : 'active'; await updateUser(u) }
async function toggleVip(u) {
  if (u.role === 'admin') return
  u.role = u.role === 'vip' ? 'member' : 'vip'
  await updateUser(u)
}
async function deleteUser(u) {
  if (u.username === props.currentUser) return // 不讓管理員刪掉自己
  if (!(await askConfirm('確定刪除帳號「' + u.username + '」?此動作無法復原。'))) return
  const res = await authFetch('/api/admin/users?username=' + encodeURIComponent(u.username), { method: 'DELETE' })
  emit('msg', res.ok ? '✓ 已刪除 ' + u.username : '✗ 刪除失敗')
  emit('reload')
}

const pendingUsers = computed(() => props.users.filter((u) => u.status === 'pending'))

// ---- 名單篩選 ----
const userRoleFilter = ref('all') // all | member | vip | admin
const userFrom = ref('')          // YYYY-MM-DD(註冊時間 >= 當日,含)
const userTo = ref('')            // YYYY-MM-DD(註冊時間 <= 當日,含)
const userSort = ref('new')       // new | old

function ymd(d) {
  const p = (x) => String(x).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}
// 快捷:填成最近 n 天(0 = 清空)
function setUserDays(n) {
  if (!n) { userFrom.value = ''; userTo.value = ''; return }
  userFrom.value = ymd(new Date(Date.now() - n * 86400000))
  userTo.value = ymd(new Date())
}
const filteredUsers = computed(() => {
  let list = props.users.slice()
  if (userRoleFilter.value !== 'all') list = list.filter((u) => u.role === userRoleFilter.value)
  if (userFrom.value) {
    const from = new Date(userFrom.value + 'T00:00:00').getTime()
    list = list.filter((u) => (u.created || 0) >= from)
  }
  if (userTo.value) {
    const to = new Date(userTo.value + 'T23:59:59').getTime()
    list = list.filter((u) => (u.created || 0) <= to)
  }
  list.sort((a, b) => (userSort.value === 'new' ? (b.created || 0) - (a.created || 0) : (a.created || 0) - (b.created || 0)))
  return list
})
function fmtReg(ms) {
  if (!ms) return '—'
  const d = new Date(ms)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}
</script>

<template>
<section v-if="pendingUsers.length" class="card adminbox">
  <h3 class="psub">🟡 待審核 ({{ pendingUsers.length }})</h3>
  <div class="reviewgrid">
    <div v-for="u in pendingUsers" :key="u.username" class="reviewcard">
      <div v-if="u.proof" class="reviewproof" @click="$emit('proof', u.proof)"><img :src="u.proof" alt="資產證明" /></div>
      <div v-else class="reviewproof empty">無證明圖</div>
      <div class="reviewinfo">
        <div class="ri-name">{{ u.username }}</div>
        <div class="ri-row">UID:<b>{{ u.uid || '—' }}</b></div>
        <div class="ri-row">交易所:<b>{{ u.notes || '—' }}</b></div>
        <div class="ri-row"><small>{{ u.created ? new Date(u.created).toLocaleString() : '' }}</small></div>
        <div class="reviewact">
          <button class="okbtn" @click="approveUser(u)">✓ 通過</button>
          <button class="nobtn" @click="rejectUser(u)">✕ 駁回</button>
        </div>
      </div>
    </div>
  </div>
</section>

<!-- 所有使用者 -->
<section class="card">
  <h3 class="psub">所有使用者 <span class="mk-count">{{ filteredUsers.length }} / {{ users.length }}</span></h3>
  <div class="userfilter">
    <span class="tf-label">階級</span>
    <button v-for="r in [['all','全部'],['member','會員'],['vip','VIP'],['admin','管理']]" :key="r[0]"
      :class="{ on: userRoleFilter === r[0] }" @click="userRoleFilter = r[0]">{{ r[1] }}</button>
    <span class="tf-label">註冊</span>
    <input type="date" class="datein" v-model="userFrom" :max="userTo || undefined" title="起始日期" />
    <span class="tf-sep">~</span>
    <input type="date" class="datein" v-model="userTo" :min="userFrom || undefined" title="結束日期" />
    <button v-for="t in [[7,'近7天'],[30,'近30天'],[90,'近90天']]" :key="t[0]" @click="setUserDays(t[0])">{{ t[1] }}</button>
    <button v-if="userFrom || userTo" @click="setUserDays(0)">清除</button>
    <span class="tf-label">排序</span>
    <button :class="{ on: userSort === 'new' }" @click="userSort = 'new'">新→舊</button>
    <button :class="{ on: userSort === 'old' }" @click="userSort = 'old'">舊→新</button>
  </div>
  <table class="grid">
    <thead><tr><th>證明</th><th>帳號</th><th>UID / 交易所</th><th>角色</th><th class="r">註冊時間</th><th class="r">VIP</th><th class="r">啟用</th><th class="r">刪除</th></tr></thead>
    <tbody>
      <tr v-for="u in filteredUsers" :key="u.username">
        <td><img v-if="u.proof" :src="u.proof" class="proofthumb" @click="$emit('proof', u.proof)" /><span v-else>—</span></td>
        <td class="coin"><button class="namebtn" @click="$emit('refof', u.username)" title="查看推廣名單">{{ u.username }}</button>
          <em v-if="u.status === 'pending'" class="qtag warn">審核中</em>
          <em v-else-if="u.status === 'banned'" class="qtag bad">停用</em>
        </td>
        <td class="rl-text"><div>{{ u.uid || '—' }}</div><small>{{ u.notes || '—' }}</small></td>
        <td>{{ u.role }}</td>
        <td class="r tsmall">{{ fmtReg(u.created) }}</td>
        <td class="r">
          <button v-if="u.role !== 'admin'" class="regbtn" :class="{ on: u.role === 'vip' }" @click="toggleVip(u)">{{ u.role === 'vip' ? 'VIP ✓' : '設 VIP' }}</button>
          <em v-else class="qtag">admin</em>
        </td>
        <td class="r">
          <label v-if="u.username !== username && u.role !== 'admin'" class="switch">
            <input type="checkbox" :checked="u.status === 'active'" @change="toggleEnabled(u)" />
            <span class="sw-track"></span>
          </label>
          <em v-else class="qtag good">{{ u.username === username ? '本人' : '—' }}</em>
        </td>
        <td class="r">
          <button v-if="u.username !== username" class="delbtn" @click="deleteUser(u)" title="刪除帳號">🗑</button>
          <span v-else>—</span>
        </td>
      </tr>
    </tbody>
  </table>
  <p v-if="!users.length" class="empty">尚無使用者。</p>
  <p v-else-if="!filteredUsers.length" class="empty">此條件下無符合的使用者。</p>
</section>

<!-- 手動新增 -->
<section class="card adminbox">
  <h3 class="psub">手動新增使用者</h3>
  <div class="newuser">
    <input v-model="newUser.u" placeholder="帳號" />
    <input v-model="newUser.p" type="text" placeholder="密碼" />
    <select v-model="newUser.role">
      <option value="member">member</option>
      <option value="vip">vip</option>
      <option value="admin">admin</option>
    </select>
    <select v-model="newUser.status">
      <option value="active">active</option>
      <option value="pending">pending</option>
      <option value="banned">banned</option>
    </select>
    <button class="loginbtn" @click="createUser">新增</button>
  </div>
</section>

</template>
