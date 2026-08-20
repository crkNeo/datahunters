<!--
  爆發型態 —— A/B 兩種型態的即時偵測紀錄與命中率。

  偵測由 cmd/collector 每分鐘寫進 pattern_hits,結果由另一支排程在事後回填。
  這個分離是這張表可信的關鍵:訊號在結果出現「之前」就已經落盤,無法事後
  挑選。因此這裡的命中率是真正的樣本外統計,不是回頭整理出來的漂亮數字。

  預設只有管理員看得到:這兩個型態目前各只有 1~2 個觀察案例,尚未驗證,
  放給會員看會被當成推薦。權限走後台「標籤權限」那張表,所以之後命中率
  真的站得住腳時,開放給 VIP 是改設定而不是改程式。
-->
<script setup>
import { ref, onMounted, onUnmounted, computed } from 'vue'
import { authFetch } from '../lib/api'

const emit = defineEmits(['msg'])

const data = ref(null)
const busy = ref(false)
const filter = ref('all')
let timer = null

const PATTERN_CN = {
  A: '投降反彈',
  B: 'OI 擴張',
}
const PATTERN_DESC = {
  A: '連續下跌 + OI 被清算 + 賣壓主導 → 買盤翻正、基差轉正的第一根',
  B: '持倉與價格同步走高 + 主動買佔優 + 基差為正且上升 → 爆量點火',
}

async function load(quiet = false) {
  if (busy.value) return
  busy.value = true
  try {
    const res = await authFetch('/api/patterns')
    if (res.ok) {
      data.value = await res.json()
      if (!quiet) emit('msg', '✓ 已重新載入(共 ' + (data.value.hits || []).length + ' 筆)')
    } else if (!quiet) {
      emit('msg', '✗ 載入失敗:' + ((await res.text()).trim() || 'HTTP ' + res.status))
    }
  } catch (e) {
    if (!quiet) emit('msg', '✗ 載入失敗:連線異常')
  }
  busy.value = false
}

const hits = computed(() => {
  const all = data.value?.hits || []
  return filter.value === 'all' ? all : all.filter((h) => h.pattern === filter.value)
})

function sign(n) { return n > 0 ? 'pos' : n < 0 ? 'neg' : '' }
function num(n, d = 2) { return (n ?? 0).toFixed(d) }

onMounted(() => {
  load(true)
  // 偵測是每分鐘一次,所以這裡也用一分鐘 —— 更快只是徒增請求
  timer = setInterval(() => load(true), 60000)
})
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <div class="pb">
    <div class="pb-head">
      <h3>爆發型態偵測</h3>
      <button class="pb-btn" :disabled="busy" @click="load(false)">重新載入</button>
    </div>

    <p class="pb-warn">
      ⚠ 這是<strong>未驗證的假說</strong>。型態 A 目前只有 1 個觀察案例、B 只有 2 個,
      條件是從逐分鐘回放讀出來的。這張表存在的目的就是累積樣本外紀錄來檢驗它們 ——
      在「平均淨報酬」穩定為正之前,不要照著下單。
    </p>

    <!-- 命中率統計:這才是重點,不是下面那張明細 -->
    <div class="pb-stats">
      <div v-for="s in data?.stats || []" :key="s.pattern" class="pb-card">
        <div class="pb-card-h">
          型態 {{ s.pattern }} · {{ PATTERN_CN[s.pattern] }}
          <span class="pb-n">{{ s.done }} / {{ s.total }} 已結算</span>
        </div>
        <p class="pb-desc">{{ PATTERN_DESC[s.pattern] }}</p>
        <div v-if="s.done > 0" class="pb-grid">
          <div><span>命中率(5m 內 ≥ +1%)</span><b>{{ num(s.win_pct, 1) }}%</b></div>
          <div><span>平均淨報酬(扣 0.3%)</span><b :class="sign(s.avg_ret_5m)">{{ num(s.avg_ret_5m) }}%</b></div>
          <div><span>MFE 中位</span><b class="pos">{{ num(s.med_mfe_5m) }}%</b></div>
          <div><span>MAE 中位</span><b class="neg">{{ num(s.med_mae_5m) }}%</b></div>
          <div><span>最佳一次</span><b class="pos">{{ num(s.best_mfe_5m) }}%</b></div>
        </div>
        <p v-else class="pb-empty">尚無已結算的樣本(觸發後約 20 分鐘才會回填結果)</p>
      </div>
    </div>

    <nav class="pb-filter">
      <button :class="{ on: filter === 'all' }" @click="filter = 'all'">全部</button>
      <button :class="{ on: filter === 'A' }" @click="filter = 'A'">型態 A</button>
      <button :class="{ on: filter === 'B' }" @click="filter = 'B'">型態 B</button>
    </nav>

    <p v-if="data?.note" class="pb-note">{{ data.note }}</p>

    <div class="pb-tablewrap">
      <table class="pb-table">
        <thead>
          <tr>
            <th>時間(UTC)</th><th>幣種</th><th>型態</th><th class="r">價格</th>
            <th class="r" title="持倉量 5 分鐘變化">ΔOI5m</th>
            <th class="r" title="成交量對前 60 分鐘的 z 分數">volZ</th>
            <th class="r">主買%</th><th class="r">基差bp</th><th class="r">資費bp</th>
            <th class="r" title="觸發前的連續走勢">走勢</th>
            <th class="r" title="觸發後 5 分鐘最大有利波動">MFE5</th>
            <th class="r" title="觸發後 5 分鐘最大不利波動">MAE5</th>
            <th class="r">結果</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(h, i) in hits" :key="i">
            <td>{{ h.time }}</td>
            <td class="sym">{{ h.symbol }}</td>
            <td><span class="tag" :class="'t' + h.pattern">{{ h.pattern }}</span></td>
            <td class="r">{{ h.price }}</td>
            <td class="r" :class="sign(h.oi_chg_5m)">{{ num(h.oi_chg_5m) }}%</td>
            <td class="r">{{ num(h.vol_z, 1) }}</td>
            <td class="r">{{ num(h.taker_pct, 0) }}</td>
            <td class="r" :class="sign(h.basis_bps)">{{ num(h.basis_bps, 1) }}</td>
            <td class="r">{{ num(h.funding_bps, 2) }}</td>
            <td class="r" :class="sign(h.run_pct)">{{ num(h.run_pct) }}%</td>
            <td class="r"><span v-if="h.done" class="pos">{{ num(h.mfe_5m) }}%</span><span v-else class="pend">—</span></td>
            <td class="r"><span v-if="h.done" class="neg">{{ num(h.mae_5m) }}%</span><span v-else class="pend">—</span></td>
            <td class="r">
              <span v-if="!h.done" class="pend">等待中</span>
              <span v-else-if="h.mfe_5m >= 1" class="pos">✓ {{ num(h.ret_5m) }}%</span>
              <span v-else class="neg">✗ {{ num(h.ret_5m) }}%</span>
            </td>
          </tr>
          <tr v-if="!hits.length"><td colspan="13" class="pb-none">尚無紀錄</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.pb { font-size: 13px; }
.pb-head { display: flex; align-items: center; gap: 12px; margin-bottom: 8px; }
.pb-head h3 { margin: 0; font-size: 15px; }
.pb-btn { padding: 4px 10px; cursor: pointer; }
.pb-warn { background: #2a1f10; border-left: 3px solid #c88; padding: 8px 10px; margin: 8px 0 14px; line-height: 1.6; }
.pb-stats { display: flex; flex-wrap: wrap; gap: 12px; margin-bottom: 14px; }
.pb-card { flex: 1 1 320px; border: 1px solid #333; border-radius: 6px; padding: 10px 12px; }
.pb-card-h { font-weight: 600; margin-bottom: 4px; display: flex; justify-content: space-between; gap: 8px; }
.pb-n { font-weight: 400; opacity: .7; }
.pb-desc { margin: 0 0 8px; opacity: .75; line-height: 1.5; }
.pb-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 6px 12px; }
.pb-grid div { display: flex; justify-content: space-between; gap: 6px; }
.pb-grid span { opacity: .7; }
.pb-empty { margin: 0; opacity: .6; }
.pb-filter { display: flex; gap: 6px; margin-bottom: 8px; }
.pb-filter button { padding: 3px 12px; cursor: pointer; }
.pb-filter button.on { font-weight: 700; }
.pb-note { opacity: .7; margin: 6px 0; }
.pb-tablewrap { overflow-x: auto; }
.pb-table { width: 100%; border-collapse: collapse; white-space: nowrap; }
.pb-table th, .pb-table td { padding: 4px 8px; border-bottom: 1px solid #2a2a2a; }
.pb-table th { text-align: left; opacity: .8; font-weight: 600; }
.pb-table .r { text-align: right; }
.pb-table .sym { font-weight: 600; }
.tag { display: inline-block; min-width: 18px; text-align: center; border-radius: 3px; padding: 0 5px; font-weight: 700; }
.tag.tA { background: #1d3a2a; }
.tag.tB { background: #1d2c3a; }
.pos { color: #4caf7d; }
.neg { color: #d9534f; }
.pend { opacity: .45; }
.pb-none { text-align: center; opacity: .6; padding: 14px; }
</style>
