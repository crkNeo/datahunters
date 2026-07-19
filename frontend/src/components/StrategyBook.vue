<!--
  策略表(共用)—— 風控警語 + 統計列 + 止盈漏斗 + 進行中/已結束兩張表。

  冥王星與微策略組(逆勢/布林/乖離/布乖v2/布林EMA)的表格本來是逐字重複的兩份,
  這裡合成一份。三處差異用 props 表達,而不是在元件裡判斷是哪個策略:

    statsOrder  統計列的欄位順序(兩邊原本不同,保留各自的順序)
    canExit     是否顯示「手動出場」欄(冥王星限管理員,微策略一律顯示)
    emptyText   沒有任何單時的提示文字(各策略的進場條件不同)

  ⚠️ 雷達三本(星軌/超新星/銀河)沒有併進來:它多了時間範圍篩選與 CSV 匯出,
  且統計列欄位不同,硬合會變成一堆 flag。那是使用者實際下單的畫面,維持原狀。
-->
<script setup>
import { fmtPct, fmtPrice, fmtClock, lvlPct, pctOf, outcomeCN, outcomeCls } from '../lib/format'

defineProps({
  // PaperState: { open: [], closed: [], stats: {} }
  state: { type: Object, default: null },
  // 風控警語(由後台「顯示風控建議」控制)
  risky: { type: Boolean, default: false },
  // 策略類型標籤,例如 ['激進','高頻']
  tags: { type: Array, default: () => [] },
  // 統計列順序:type | win | avg | total
  statsOrder: { type: Array, default: () => ['type', 'win', 'avg', 'total'] },
  // 顯示「手動出場」欄
  canExit: { type: Boolean, default: false },
  emptyText: { type: String, default: '尚無訊號。' },
})

defineEmits(['coin', 'exit'])
</script>

<template>
  <div>
    <p v-if="risky" class="riskwarn">⚠️ 目前盤面使用此策略風險較大,請謹慎操作</p>

    <div v-if="state" class="pstats">
      <template v-for="k in statsOrder" :key="k">
        <div v-if="k === 'type'" class="pstat">
          <div class="stat-k">策略類型<span class="help" tabindex="0">?<span class="help-pop">此策略的操作屬性:激進/保守(風險)、高頻/低頻(開單頻率)、長線/短線(持倉時間)。由管理端設定。</span></span></div>
          <div class="stat-v stat-tags">{{ tags.join('・') || '—' }}</div>
        </div>
        <div v-else-if="k === 'win'" class="pstat">
          <div class="stat-k">勝率</div>
          <div class="stat-v" :class="state.stats.win_rate >= 50 ? 'long' : 'short'">{{ state.stats.win_rate }}%</div>
        </div>
        <div v-else-if="k === 'avg'" class="pstat">
          <div class="stat-k">平均損益</div>
          <div class="stat-v" :class="state.stats.avg_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(state.stats.avg_pnl) }}</div>
        </div>
        <div v-else-if="k === 'total'" class="pstat">
          <div class="stat-k">累計損益</div>
          <div class="stat-v" :class="state.stats.total_pnl >= 0 ? 'long' : 'short'">{{ fmtPct(state.stats.total_pnl) }}</div>
        </div>
      </template>
    </div>

    <div v-if="state && state.stats.closed && (state.stats.multi_tp || state.stats.tp1)" class="tpfunnel">
      <div class="tpf-title">止盈達成漏斗 · 共 {{ state.stats.closed }} 筆已結束</div>
      <div v-for="lv in [1, 2, 3]" :key="lv" class="tpf-row">
        <span class="tpf-lbl">TP{{ lv }} 達成</span>
        <span class="tpf-bar"><i :style="{ width: pctOf(state.stats['tp' + lv], state.stats.closed) + '%' }"></i></span>
        <span class="tpf-val">{{ state.stats['tp' + lv] }} 筆 · <b>{{ pctOf(state.stats['tp' + lv], state.stats.closed) }}%</b></span>
      </div>
    </div>

    <h3 class="psub" v-if="state && state.open.length">進行中 ({{ state.open.length }})</h3>
    <table v-if="state && state.open.length" class="grid">
      <thead><tr><th>幣種</th><th>方向</th><th class="r">進場</th><th class="r">現價</th><th class="r">損益%</th><th>進度</th><th class="r">動態止損</th><th class="r">進場時間</th><th v-if="canExit" class="r">操作</th></tr></thead>
      <tbody>
        <tr v-for="t in state.open" :key="t.coin + t.open_time" class="clickable" @click="$emit('coin', t.coin)">
          <td class="coin">{{ t.coin }}</td>
          <td><span class="dir" :class="t.dir === 'long' ? 'long' : 'short'">{{ t.dir === 'long' ? '做多' : '做空' }}</span></td>
          <td class="r">{{ fmtPrice(t.entry) }}</td>
          <td class="r">{{ fmtPrice(t.cur) }}</td>
          <td class="r" :class="t.pnl_pct >= 0 ? 'long' : 'short'"><b>{{ fmtPct(t.pnl_pct) }}</b></td>
          <td class="tsmall" :title="t.tp1 ? ('TP1 ' + fmtPrice(t.tp1) + ' (' + lvlPct(t, t.tp1) + ') · TP2 ' + fmtPrice(t.tp2) + ' (' + lvlPct(t, t.tp2) + ') · TP3 ' + fmtPrice(t.tp) + ' (' + lvlPct(t, t.tp) + ')') : ('止盈 ' + fmtPrice(t.tp) + ' (' + lvlPct(t, t.tp) + ')')">
            <template v-if="t.tp1">
              <span class="tppill" :class="{ hit: t.legs >= 1 }">TP1 {{ fmtPrice(t.tp1) }} <i class="tppct">{{ lvlPct(t, t.tp1) }}</i></span><span class="tppill" :class="{ hit: t.legs >= 2 }">TP2 {{ fmtPrice(t.tp2) }} <i class="tppct">{{ lvlPct(t, t.tp2) }}</i></span><span class="tppill" :class="{ hit: t.legs >= 3 }">TP3 {{ fmtPrice(t.tp) }} <i class="tppct">{{ lvlPct(t, t.tp) }}</i></span><span class="tsmall"> 剩 {{ Math.round((1 - (t.filled || 0)) * 100) }}%</span>
            </template>
            <template v-else>
              <span class="tsmall">單一 · {{ fmtPrice(t.tp) }} <i class="tppct">{{ lvlPct(t, t.tp) }}</i></span>
              <!-- 保本位:純提示,止盈止損不變(布林EMA) -->
              <span v-if="t.be_hit" class="betag" :title="'價格曾觸及保本位 ' + fmtPrice(t.be_price) + ';止盈止損維持不變'">🛡 已達保本位 {{ fmtPrice(t.be_price) }}</span>
            </template>
          </td>
          <td class="r short">{{ fmtPrice(t.sl) }}<small v-if="t.legs >= 2" class="vtag"> 鎖利</small><small v-else-if="t.legs >= 1" class="vtag"> 保本</small></td>
          <td class="r tsmall">{{ fmtClock(t.open_time) }}</td>
          <td v-if="canExit" class="r"><button class="exitbtn" @click.stop="$emit('exit', t.id)">手動出場</button></td>
        </tr>
      </tbody>
    </table>

    <h3 class="psub" v-if="state && state.closed.length">已結束 ({{ state.closed.length }})</h3>
    <table v-if="state && state.closed.length" class="grid">
      <thead><tr><th>幣種</th><th>方向</th><th class="r">進場</th><th class="r">出場</th><th>結果</th><th class="r">損益%</th><th class="r">出場時間</th></tr></thead>
      <tbody>
        <tr v-for="(t, i) in state.closed" :key="i" class="clickable" @click="$emit('coin', t.coin)">
          <td class="coin">{{ t.coin }}</td>
          <td><span class="dir" :class="t.dir === 'long' ? 'long' : 'short'">{{ t.dir === 'long' ? '做多' : '做空' }}</span></td>
          <td class="r">{{ fmtPrice(t.entry) }}</td>
          <td class="r">{{ fmtPrice(t.cur) }}</td>
          <td><span class="otag" :class="outcomeCls(t.outcome, t.pnl_pct)">{{ outcomeCN(t.outcome, t.pnl_pct) }}</span></td>
          <td class="r" :class="t.pnl_pct >= 0 ? 'long' : 'short'"><b>{{ fmtPct(t.pnl_pct) }}</b></td>
          <td class="r tsmall">{{ fmtClock(t.close_time) }}</td>
        </tr>
      </tbody>
    </table>

    <p v-if="state && !state.open.length && !state.closed.length" class="loading">{{ emptyText }}</p>
    <p v-else-if="!state" class="loading">載入中…</p>
  </div>
</template>
