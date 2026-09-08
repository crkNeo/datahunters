<!--
  BattleField — 首頁「BTC 多空交戰」卡(黑金改版)。

  資料維持「瀏覽器直連」Binance 公開 WS/REST(免 key;WS 不受 CORS),
  唯一後端呼叫是 /api/btc-sr(固定公開,不需登入)。
  ⚠️ 舊版的士兵/坦克/轟炸機 canvas 戰場已依設計 mock(homeA2opt)改為「長條卡」:
     多空帳戶比長條 + 主動買賣/淨額/爆倉 + 支撐壓力城牆。
     資料蒐集邏輯(多空帳戶比、主動買賣量、強制平倉)完全保留,只換視覺層。
     canvas 版本仍保存在 git 歷史(optimize 分支改版前),需要可還原。
-->
<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'

// 多空戰況即時值。全部由瀏覽器直連交易所公開端點取得,不打自己的後端。
const bfLive = ref({ price: 0, longPct: 50, oi: 0, liqLong: 0, liqShort: 0, buy: 0, sell: 0, conn: false })
const bfSR = ref(null) // 城牆(支撐/壓力)來自 /api/btc-sr
let bfWS = null, bfStatTimer = null, bfRetry = 0
// aggTrade 串流在部分網路環境收不到 → 看門狗改用 REST 輪詢補上,WS 恢復就停掉。
let bfWsTradeAt = 0, bfPoll = null, bfDog = null, bfLastAggId = 0

async function loadBtcSR() {
  try { const res = await fetch('/api/btc-sr'); if (res.ok) bfSR.value = await res.json() } catch (e) { /* 城牆缺席不影響其他部分 */ }
}

// 兵力對比(多空帳戶比)+ 部隊規模(OI)
async function bfLoadStats() {
  try {
    const [lsr, oir] = await Promise.all([
      fetch('https://fapi.binance.com/futures/data/globalLongShortAccountRatio?symbol=BTCUSDT&period=5m&limit=1'),
      fetch('https://fapi.binance.com/fapi/v1/openInterest?symbol=BTCUSDT'),
    ])
    if (lsr.ok) { const a = await lsr.json(); if (a && a[0]) bfLive.value.longPct = +a[0].longAccount * 100 }
    if (oir.ok) { const o = await oir.json(); if (o && o.openInterest) bfLive.value.oi = +o.openInterest }
  } catch (e) { /* 非關鍵 */ }
}

function bfConnect() {
  if (bfWS) return
  const streams = ['btcusdt@aggTrade', 'btcusdt@forceOrder', 'btcusdt@markPrice@1s']
  try { bfWS = new WebSocket('wss://fstream.binance.com/stream?streams=' + streams.join('/')) } catch (e) { return }
  bfWS.onopen = () => { bfRetry = 0; bfLive.value.conn = true }
  bfWS.onclose = () => {
    bfLive.value.conn = false; bfWS = null
    bfRetry = Math.min(bfRetry + 1, 6); setTimeout(bfConnect, 1000 * bfRetry) // 退避重連
  }
  bfWS.onerror = () => { try { bfWS && bfWS.close() } catch (e) {} }
  bfWS.onmessage = (ev) => {
    let m; try { m = JSON.parse(ev.data) } catch (e) { return }
    const d = m.data; if (!d) return
    if (d.e === 'aggTrade') { bfWsTradeAt = Date.now(); bfOnTrade(+d.p, +d.q, d.m) }
    else if (d.e === 'forceOrder' && d.o) bfOnLiq(+d.o.ap || +d.o.p, +d.o.q, d.o.S)
    else if (d.e === 'markPriceUpdate') { bfLive.value.price = +d.p }
  }
}
function bfDisconnect() {
  if (bfWS) { try { bfWS.onclose = null; bfWS.close() } catch (e) {} bfWS = null }
  bfLive.value.conn = false
}

// 一筆主動成交 → 累計買/賣金額(USD)。m=isBuyerMaker:true 代表主動方是「賣家」。
function bfOnTrade(px, qty, buyerMaker) {
  bfLive.value.price = px
  if (!buyerMaker) bfLive.value.buy += qty * px
  else bfLive.value.sell += qty * px
}
// 強制平倉:side=SELL → 多單被清算;BUY → 空單被清算
function bfOnLiq(px, qty, side) {
  const usd = px * qty
  if (side === 'SELL') bfLive.value.liqLong += usd
  else bfLive.value.liqShort += usd
}

// REST 成交備援:WS 的 aggTrade 沒供應時改用輪詢,以 aggTrade id (a) 去重。
async function bfPollTrades() {
  try {
    const r = await fetch('https://fapi.binance.com/fapi/v1/aggTrades?symbol=BTCUSDT&limit=100'); if (!r.ok) return
    const arr = await r.json(); if (!Array.isArray(arr) || !arr.length) return
    const seed = bfLastAggId === 0
    for (const t of arr) { if (t.a <= bfLastAggId) continue; bfLastAggId = t.a; if (!seed) bfOnTrade(+t.p, +t.q, t.m) }
    if (seed) bfLive.value.price = +arr[arr.length - 1].p // 首輪只取現價,不灌歷史成交
  } catch (e) { /* 限流/離線 → 下一輪再試 */ }
}
function bfWatchdog() {
  const wsAlive = Date.now() - bfWsTradeAt < 6000
  if (!wsAlive && !bfPoll) { bfPoll = setInterval(bfPollTrades, 2000); bfPollTrades() }
  else if (wsAlive && bfPoll) { clearInterval(bfPoll); bfPoll = null } // WS 恢復 → 收掉輪詢
}

function bfStart() {
  bfConnect(); bfLoadStats(); loadBtcSR()
  bfStatTimer = setInterval(() => { bfLoadStats(); loadBtcSR() }, 60000)
  bfDog = setInterval(bfWatchdog, 3000)
  setTimeout(bfWatchdog, 4000) // 開場先給 WS 一點時間
}
function bfStop() {
  clearInterval(bfStatTimer); clearInterval(bfDog); clearInterval(bfPoll); bfPoll = null
  bfDisconnect()
}

const bfLongPct = computed(() => Math.max(1, Math.min(99, Math.round(bfLive.value.longPct))))
const bfNet = computed(() => bfLive.value.buy - bfLive.value.sell)
function bfUsd(v) {
  if (!v) return '0'
  const a = Math.abs(v)
  if (a >= 1e6) return (a / 1e6).toFixed(1) + 'M'
  if (a >= 1e3) return (a / 1e3).toFixed(0) + 'K'
  return a.toFixed(0)
}

// 分頁切到背景時停掉(不燒電、不佔連線),回到前景再續。
function bfOnVisibility() {
  if (document.visibilityState === 'hidden') bfStop()
  else bfStart()
}
onMounted(() => { bfStart(); document.addEventListener('visibilitychange', bfOnVisibility) })
onUnmounted(() => { document.removeEventListener('visibilitychange', bfOnVisibility); bfStop() })
</script>

<template>
<!-- 多空交戰(長條卡,即時,瀏覽器直連交易所) -->
<section class="battle">
  <div class="bt-top">
    <div class="bt-t">🔥 BTC 多空交戰</div>
    <div class="bt-price">現價 <b>{{ bfLive.price ? Math.round(bfLive.price).toLocaleString() : '—' }}</b></div>
    <div class="bt-live" :class="{ off: !bfLive.conn }"><i></i>{{ bfLive.conn ? 'LIVE' : '連線中' }}</div>
  </div>
  <div class="bt-war">
    <div class="bt-side"><span class="lab long">多方</span><span class="pct long">{{ bfLongPct }}%</span></div>
    <div class="bt-bar">
      <div class="l" :style="{ width: bfLongPct + '%' }"></div>
      <div class="s" :style="{ width: (100 - bfLongPct) + '%' }"></div>
      <div class="mid" :style="{ left: bfLongPct + '%' }"></div>
    </div>
    <div class="bt-side"><span class="lab short">空方</span><span class="pct short">{{ 100 - bfLongPct }}%</span></div>
  </div>
  <div class="bt-meta">
    <span>主動買 <b class="long">${{ bfUsd(bfLive.buy) }}</b></span>
    <span>主動賣 <b class="short">${{ bfUsd(bfLive.sell) }}</b></span>
    <span>淨額 <b :class="bfNet >= 0 ? 'long' : 'short'">{{ bfNet >= 0 ? '+' : '−' }}${{ bfUsd(bfNet) }}</b></span>
    <span>爆倉 <b>${{ bfUsd(bfLive.liqLong + bfLive.liqShort) }}</b></span>
  </div>
  <div class="bt-walls" v-if="bfSR && (bfSR.sup_ok || bfSR.res_ok)">
    <span v-if="bfSR.sup_ok">🛡 支撐城牆 <b class="long">${{ Math.round(bfSR.support).toLocaleString() }}</b></span>
    <span v-if="bfSR.res_ok">⚔ 壓力城牆 <b class="short">${{ Math.round(bfSR.resistance).toLocaleString() }}</b></span>
  </div>
  <p class="bt-note">盤面視覺化 · 資料直連交易所公開端點 · 僅供參考,非投資建議</p>
</section>
</template>

<!-- 不加 scoped:全站 CSS 皆全域;.bt-live 借用 App.vue 的 maipulse 動畫。全部以 .battle 命名空間避免衝突。 -->
<style>
.battle{position:relative;overflow:hidden;background:linear-gradient(150deg,#13151d,var(--c-surf));border:1px solid var(--c-line);border-radius:var(--r-lg);padding:16px 18px;display:flex;flex-direction:column;gap:14px;margin-bottom:14px}
.battle .bt-top{display:flex;align-items:center;gap:11px}
.battle .bt-t{font-family:var(--f-disp);font-weight:700;font-size:15px;letter-spacing:.3px;display:flex;align-items:center;gap:8px}
.battle .bt-price{font-family:var(--f-mono);font-size:12.5px;color:var(--c-mut)}
.battle .bt-price b{color:var(--c-txt);font-size:15px}
.battle .bt-live{margin-left:auto;display:flex;align-items:center;gap:6px;font-family:var(--f-mono);font-size:10.5px;color:var(--c-up)}
.battle .bt-live i{width:6px;height:6px;border-radius:50%;background:var(--c-up);box-shadow:0 0 6px var(--c-up);animation:maipulse 1.5s infinite}
.battle .bt-live.off{color:var(--c-mut2)}
.battle .bt-live.off i{background:var(--c-mut2);box-shadow:none;animation:none}
.battle .bt-war{display:flex;align-items:center;gap:14px}
.battle .bt-side{display:flex;flex-direction:column;align-items:center;min-width:64px}
.battle .bt-side .lab{font-family:var(--f-disp);font-size:12px;font-weight:600}
.battle .bt-side .pct{font-family:var(--f-mono);font-size:30px;font-weight:600;line-height:1}
.battle .long{color:var(--c-up)}
.battle .short{color:var(--c-dn)}
.battle .bt-bar{flex:1;height:24px;border-radius:8px;overflow:hidden;display:flex;border:1px solid var(--c-line);position:relative}
.battle .bt-bar .l{background:linear-gradient(90deg,rgba(55,214,138,.3),var(--c-up));transition:width .5s ease}
.battle .bt-bar .s{background:linear-gradient(90deg,var(--c-dn),rgba(255,92,108,.3));transition:width .5s ease}
.battle .bt-bar .mid{position:absolute;top:-3px;bottom:-3px;width:2px;background:var(--c-gold);box-shadow:0 0 8px var(--c-gold);z-index:2;transition:left .5s ease}
.battle .bt-bar::after{content:"";position:absolute;top:0;bottom:0;left:-30%;width:22%;background:linear-gradient(100deg,transparent,rgba(255,255,255,.14),transparent);animation:bfsweep 3.6s ease-in-out infinite;z-index:1}
@keyframes bfsweep{0%{left:-30%}55%,100%{left:120%}}
.battle .bt-meta{display:flex;gap:18px;font-size:12px;color:var(--c-mut);font-family:var(--f-mono);flex-wrap:wrap}
.battle .bt-meta b{color:var(--c-txt)}
.battle .bt-walls{display:flex;gap:18px;font-size:12px;color:var(--c-mut);font-family:var(--f-mono);flex-wrap:wrap;border-top:1px solid var(--c-line);padding-top:10px}
.battle .bt-note{margin:0;font-size:10.5px;color:var(--c-mut2)}
@media(max-width:560px){.battle .bt-side{min-width:42px}.battle .bt-side .pct{font-size:21px}.battle .bt-war{gap:8px}.battle .bt-meta{gap:10px 14px}}
</style>
