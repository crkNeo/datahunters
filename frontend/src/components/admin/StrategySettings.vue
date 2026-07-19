<!--
  策略設定 —— 每個策略一張卡:開關、類型、止損上限、出場模式(分批/保本/單段)、
  各段位置與比例、保本參數、通知開關。

  自己載入自己的資料;存檔成功後 emit('changed'),讓外層重抓公開的策略 meta
  (類型標籤與風控警語會顯示在各策略頁上)。
-->
<script setup>
import { ref, onMounted } from 'vue'
import { authFetch } from '../../lib/api'

const emit = defineEmits(['changed', 'msg'])
const stratStates = ref([])
const stratBusy = ref(false)

// quiet=true 是掛載時的首次載入。手動刷新要給訊息 —— 重載後畫面通常長一樣,
// 沒有回饋就會以為按鈕壞了。
async function loadStratStates(quiet = false) {
  if (stratBusy.value) return
  stratBusy.value = true
  try {
    const res = await authFetch('/api/admin/strat-states')
    if (res.ok) {
      stratStates.value = await res.json()
      if (!quiet) emit('msg', '✓ 已重新載入策略狀態')
    } else if (!quiet) {
      emit('msg', '✗ 載入失敗:' + ((await res.text()).trim() || ('HTTP ' + res.status)))
    }
  } catch (e) {
    if (!quiet) emit('msg', '✗ 載入失敗:連線異常')
  }
  stratBusy.value = false
}
async function toggleStrat(st) {
  const res = await authFetch('/api/admin/strat-toggle', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: st.name, on: !st.enabled }),
  })
  if (res.ok) { st.enabled = !st.enabled; emit('msg', '✓ ' + st.label + (st.enabled ? ' 已開啟' : ' 已關閉(不再開新單)')) }
}
// admin: per-strategy config (類型 / 風控警語 / 最大止損% / 保本 / 分批止盈)
const STRAT_TAGS = ['激進', '保守', '高頻', '低頻', '長線', '短線']
function toggleStratTag(st, tag) {
  const tags = Array.isArray(st.tags) ? st.tags.slice() : []
  const i = tags.indexOf(tag)
  if (i >= 0) tags.splice(i, 1); else tags.push(tag)
  st.tags = tags
}
async function saveStratCfg(st) {
  const n = (v) => Number(v) || 0
  const cfg = {
    tags: st.tags || [], show_risk: !!st.show_risk, max_sl_pct: n(st.max_sl_pct),
    exit_mode: st.exit_mode || 'single',
    split_a: n(st.split_a), split_b: n(st.split_b),
    split_w1: n(st.split_w1), split_w2: n(st.split_w2), split_w3: n(st.split_w3),
    be_at_pct: n(st.be_at_pct), be_buf_pct: n(st.be_buf_pct), be_cue_pct: n(st.be_cue_pct),
    notify_open: !!st.notify_open, notify_close: !!st.notify_close,
    notify_tp: !!st.notify_tp, notify_be: !!st.notify_be,
  }
  const res = await authFetch('/api/admin/strat-config', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: st.name, cfg }),
  })
  emit('msg', res.ok ? '✓ ' + st.label + ' 設定已儲存(下一筆開單起生效)' : '✗ 儲存失敗')
  if (res.ok) emit('changed')
}
// 丟掉覆寫、回到程式內建的回測值(誤設時的退路)
async function resetStratCfg(st) {
  const res = await authFetch('/api/admin/strat-config', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name: st.name, reset: true }),
  })
  emit('msg', res.ok ? '✓ ' + st.label + ' 已恢復預設值' : '✗ 恢復失敗')
  if (res.ok) { loadStratStates(); emit('changed') }
}

onMounted(() => loadStratStates(true)) // 首次載入不出訊息
defineExpose({ load: loadStratStates })
</script>

<template>
<section class="card adminbox">
  <h3 class="psub">策略開關 <button class="minibtn" :disabled="stratBusy" @click="loadStratStates()">{{ stratBusy ? '刷新中…' : '刷新' }}</button></h3>
  <div class="strat-toggles">
    <div v-for="st in stratStates" :key="st.name" class="stratcfg">
      <div class="strat-row">
        <span class="strat-name">{{ st.label }}</span>
        <button class="toggle" :class="{ on: st.enabled }" @click="toggleStrat(st)">
          <span class="toggle-knob"></span>
        </button>
        <span class="strat-status" :class="st.enabled ? 'long' : 'short'">{{ st.enabled ? '開啟' : '關閉' }}</span>
      </div>
      <div class="stratcfg-line">
        <span class="stratcfg-k">類型</span>
        <button v-for="tg in STRAT_TAGS" :key="tg" class="tagchip" :class="{ on: (st.tags || []).includes(tg) }" @click="toggleStratTag(st, tg)">{{ tg }}</button>
      </div>
      <div class="stratcfg-line">
        <span class="stratcfg-k">最大止損%</span>
        <input v-model.number="st.max_sl_pct" type="number" min="0" max="100" step="0.5" class="stratcfg-num" />
        <span class="stratcfg-hint">0 = 不限制;止損距離超過此% 不開新單</span>
      </div>

      <!-- 出場模式三選一。分批與保本互斥:保本靠 TP1 觸發,兩者並存只會得到
           「開了保本卻永遠不觸發」的假設定,所以改成單選而不是兩個獨立開關。 -->
      <div class="stratcfg-line">
        <span class="stratcfg-k">出場模式</span>
        <button v-for="m in [['split', '分批止盈'], ['breakeven', '保本'], ['single', '單段']]" :key="m[0]"
          class="roleopt" :class="{ on: st.exit_mode === m[0] }" @click="st.exit_mode = m[0]">{{ m[1] }}</button>
      </div>

      <template v-if="st.exit_mode === 'split'">
        <div class="stratcfg-line">
          <span class="stratcfg-k">分段位置%</span>
          <label class="stratcfg-mini">TP1<input v-model.number="st.split_a" type="number" min="1" max="99" step="1" class="stratcfg-num sm" /></label>
          <label class="stratcfg-mini">TP2<input v-model.number="st.split_b" type="number" min="1" max="99" step="1" class="stratcfg-num sm" /></label>
          <span class="stratcfg-hint">距離最終止盈的百分比(TP2 需大於 TP1)</span>
        </div>
        <div class="stratcfg-line">
          <span class="stratcfg-k">分批比例%</span>
          <label class="stratcfg-mini">TP1<input v-model.number="st.split_w1" type="number" min="0" max="100" step="5" class="stratcfg-num sm" /></label>
          <label class="stratcfg-mini">TP2<input v-model.number="st.split_w2" type="number" min="0" max="100" step="5" class="stratcfg-num sm" /></label>
          <label class="stratcfg-mini">TP3<input v-model.number="st.split_w3" type="number" min="0" max="100" step="5" class="stratcfg-num sm" /></label>
          <span class="stratcfg-hint" :class="{ warn: (st.split_w1 + st.split_w2 + st.split_w3) !== 100 }">
            合計 {{ (st.split_w1 || 0) + (st.split_w2 || 0) + (st.split_w3 || 0) }}%<span v-if="(st.split_w1 + st.split_w2 + st.split_w3) !== 100">(非 100% 會自動等比正規化)</span>
          </span>
        </div>
      </template>

      <template v-else-if="st.exit_mode === 'breakeven'">
        <div class="stratcfg-line">
          <span class="stratcfg-k">保本觸發%</span>
          <input v-model.number="st.be_at_pct" type="number" min="1" max="99" step="1" class="stratcfg-num" />
          <span class="stratcfg-hint">走到最終止盈的此百分比時,把止損移到保本</span>
        </div>
        <p class="stratcfg-dep">⚠️ 回測顯示此模式表現較差:單段部位走到 1/3、1/2、2/3 移保本,分別把 +35.6% 打成 −37.5%、−26.3%、+0.7%(剪掉肥尾止盈)。</p>
      </template>

      <div v-if="st.exit_mode !== 'single'" class="stratcfg-line">
        <span class="stratcfg-k">保本緩衝%</span>
        <input v-model.number="st.be_buf_pct" type="number" min="0" max="5" step="0.01" class="stratcfg-num" />
        <span class="stratcfg-hint">保本價 = 進場價 ±此%(避免剛好掃在進場價)</span>
      </div>

      <div class="stratcfg-line">
        <span class="stratcfg-k">保本位提示%</span>
        <input v-model.number="st.be_cue_pct" type="number" min="0" max="99" step="1" class="stratcfg-num" />
        <span class="stratcfg-hint">0 = 不提示。<b>只發通知、不動止盈止損</b>,與上面的「保本」是兩回事</span>
      </div>

      <div class="stratcfg-line">
        <span class="stratcfg-k">通知</span>
        <label class="stratcfg-chk"><input v-model="st.notify_open" type="checkbox" /> 開倉</label>
        <label class="stratcfg-chk"><input v-model="st.notify_close" type="checkbox" /> 平倉</label>
        <label class="stratcfg-chk"><input v-model="st.notify_tp" type="checkbox" /> 分段止盈</label>
        <label class="stratcfg-chk"><input v-model="st.notify_be" type="checkbox" /> 保本位</label>
      </div>

      <div class="stratcfg-line">
        <label class="stratcfg-chk"><input v-model="st.show_risk" type="checkbox" /> 顯示風控建議</label>
        <button class="minibtn" @click="saveStratCfg(st)">儲存</button>
        <button class="minibtn" @click="resetStratCfg(st)" title="丟掉所有覆寫,回到程式內建的回測值">恢復預設</button>
      </div>
    </div>
  </div>
  <p class="loginhint">
    關閉 = 該策略<b>不再開新單</b>;進行中的單<b>不會被平掉</b>(照常跑到止盈止損)。<br />
    「顯示風控建議」開啟後,該策略頁會出現<b>風險警語</b>提醒使用者謹慎操作。<br />
    最大止損% / 保本 / 分批止盈 只影響<b>之後開的新單</b>,已進行中的單維持原本的止盈止損設定。<br />
    <b>保本依附於分批止盈</b>:止損是在 TP1 觸及時才移到保本價(進場±0.05%),所以分批止盈關閉時保本無效。<br />
    另:止盈距離不到 <b>0.8%</b> 的單會自動<b>不分批</b>(退回單段),那種單也不會有保本。
  </p>
</section>
</template>
