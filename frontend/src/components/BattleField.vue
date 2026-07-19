<!--
  BattleField — 首頁「BTC 多空交戰」戰場。

  完全自給自足:資料由瀏覽器直連 Binance 公開 WS/REST 取得,唯一的後端呼叫是
  /api/btc-sr(固定公開,不需登入),所以這個元件不依賴任何 auth 狀態。
  WS 連線與 requestAnimationFrame 迴圈由元件自己在掛載/卸載時起停。
-->
<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'

// ---- 戰場: BTC 多空交戰 (public, 首頁大盤分析上方) ----
// 資料全部由「瀏覽器直連」Binance 公開 WS/REST(免 key;WS 不受 CORS 限制)。
// 我們的後端只提供城牆位置 /api/btc-sr — 所以看的人再多也不會打爆自己的伺服器,
// 流量算在每個訪客自己的 IP 額度上。
//
// 座標系: X 軸 = 價格。戰線=現價;右=壓力(空方要塞,賣單在上)、左=支撐(多方要塞,買單在下)。
// 綠兵(主動買)往右衝要攻破壓力,紅兵(主動賣)往左衝要攻破支撐。
const bfOpen = ref(true) // 收合(省電)
const bfCanvas = ref(null)
const bfSR = ref(null)
const bfLive = ref({ price: 0, longPct: 50, oi: 0, liqLong: 0, liqShort: 0, buy: 0, sell: 0, conn: false })
let bfWS = null, bfRAF = null, bfFarTimer = null, bfStatTimer = null, bfRetry = 0
let bfSoldiers = [], bfBlasts = [], bfSparks = [], bfNear = { bids: [], asks: [] }, bfFar = { bids: [], asks: [] }
// 戰場氛圍(純裝飾,不代表任何盤面數據):飄塵 + 戰線餘燼。
// 平靜盤成交稀疏時,光靠真實成交撐不起畫面,這層讓場景不會死掉。
let bfDust = [], bfEmbers = []
// 戰場實體:砲彈(砲兵齊射)、飛機(巨鯨成交 / 清算空襲)、殘骸(陣亡堆積)
let bfShells = [], bfPlanes = [], bfWrecks = [], bfBombs = []
let bfArtyT = 0 // 砲兵齊射計時器
// 士兵/坦克 icon:離屏預渲染一次,之後只 drawImage。220 個單位若每格重畫路徑會拖垮手機。
let bfSprites = null
function bfMakeSprites() {
  const mk = (w, h, fn) => {
    const c = document.createElement('canvas')
    c.width = w; c.height = h
    fn(c.getContext('2d'))
    return c
  }
  // 一律畫成「面向右」,畫的時候用 scale(-1,1) 翻給空方用
  const soldier = (col, dark) => mk(18, 25, (c) => {
    c.fillStyle = dark
    c.fillRect(5, 18, 3, 7); c.fillRect(10, 18, 3, 7)      // 腿
    c.fillStyle = col
    c.fillRect(5, 9, 8, 10)                                 // 軀幹
    c.beginPath(); c.arc(9, 6.2, 3.2, 0, 7); c.fill()       // 頭
    c.fillStyle = dark
    c.beginPath(); c.arc(9, 5.6, 4.5, Math.PI, 0); c.fill() // 鋼盔
    c.fillRect(11, 10.6, 7, 1.8)                            // 步槍
    c.fillStyle = col
    c.fillRect(9, 10, 4, 2.6)                               // 持槍手臂
  })
  const tank = (col, dark) => mk(34, 19, (c) => {
    c.fillStyle = dark
    c.fillRect(2, 11, 25, 6)                                // 履帶
    c.fillStyle = 'rgba(0,0,0,0.35)'
    for (let i = 0; i < 5; i++) { c.beginPath(); c.arc(5 + i * 5, 14, 1.6, 0, 7); c.fill() } // 輪
    c.fillStyle = col
    c.fillRect(4, 6, 22, 5.5)                               // 車體
    c.fillRect(11, 2, 10, 5)                                // 砲塔
    c.fillRect(20, 3.4, 13, 2.2)                            // 砲管
  })
  // 裝甲車:比坦克小、比步兵重 — 中等單量
  const apc = (col, dark) => mk(26, 15, (c) => {
    c.fillStyle = dark
    c.fillRect(2, 9, 20, 4)
    c.fillStyle = 'rgba(0,0,0,0.35)'
    for (let i = 0; i < 3; i++) { c.beginPath(); c.arc(6 + i * 6, 12, 2.1, 0, 7); c.fill() } // 輪
    c.fillStyle = col
    c.fillRect(3, 4, 17, 5.5)                               // 車體
    c.beginPath(); c.moveTo(20, 4); c.lineTo(25, 7); c.lineTo(20, 9.5); c.closePath(); c.fill() // 斜前緣
    c.fillStyle = dark
    c.fillRect(14, 1.5, 5, 3)                               // 機槍塔
  })
  // 砲兵:駐守後方,朝敵方拋射 — 掛單量的具象化
  // 砲兵。底下墊一塊深色陣地,否則綠砲兵擺在綠地上根本看不見(實測)。
  const arty = (col, dark) => mk(26, 20, (c) => {
    c.fillStyle = 'rgba(0,0,0,0.45)'                        // 陣地土堆 → 拉開與地面的對比
    c.beginPath(); c.ellipse(12, 17, 12, 3.4, 0, 0, 7); c.fill()
    c.fillStyle = dark
    c.fillRect(3, 12, 15, 3.4)                              // 底座
    c.beginPath(); c.arc(6, 15.4, 2.6, 0, 7); c.fill()
    c.beginPath(); c.arc(13, 15.4, 2.6, 0, 7); c.fill()
    c.fillStyle = col
    c.fillRect(5, 8, 10, 4.8)                               // 機身
    c.save(); c.translate(13, 9); c.rotate(-0.62)           // 仰角砲管
    c.fillStyle = dark; c.fillRect(0, -2.1, 14, 4.2)        // 砲管外框(深色描邊)
    c.fillStyle = col; c.fillRect(0, -1.1, 13, 2.2)
    c.restore()
  })
  // 轟炸機:巨鯨成交 / 清算空襲 — 從天上飛過來投彈
  const plane = (col, dark) => mk(42, 16, (c) => {
    c.fillStyle = col
    c.beginPath(); c.ellipse(20, 8, 19, 3.2, 0, 0, 7); c.fill() // 機身
    c.fillStyle = dark
    c.beginPath(); c.moveTo(18, 7); c.lineTo(8, 1); c.lineTo(14, 7); c.closePath(); c.fill()   // 上翼
    c.beginPath(); c.moveTo(18, 9); c.lineTo(8, 15); c.lineTo(14, 9); c.closePath(); c.fill()  // 下翼
    c.beginPath(); c.moveTo(2, 8); c.lineTo(0, 3); c.lineTo(6, 7); c.closePath(); c.fill()     // 尾翼
    c.fillStyle = 'rgba(255,255,255,0.5)'
    c.beginPath(); c.arc(31, 7.4, 2, 0, 7); c.fill()        // 駕駛艙
  })
  // 殘骸:陣亡留在戰場上,看得出剛剛哪一段打得最兇
  const wreck = (dark) => mk(14, 7, (c) => {
    c.fillStyle = dark
    c.fillRect(1, 4, 11, 2.5)
    c.fillRect(3, 2, 3, 2.5); c.fillRect(8, 1.5, 2.5, 3)
  })
  bfSprites = {
    bull: soldier('#3ddb84', '#1a7a45'), bear: soldier('#ff6b6b', '#8f2f2f'),
    bullApc: apc('#3ddb84', '#146b3c'), bearApc: apc('#ff6b6b', '#7d2a2a'),
    bullTank: tank('#3ddb84', '#126034'), bearTank: tank('#ff6b6b', '#6f2424'),
    bullArty: arty('#48c98a', '#125c33'), bearArty: arty('#e07a7a', '#6b2626'),
    bullPlane: plane('#5fe3a0', '#1a7a45'), bearPlane: plane('#ff8a8a', '#8f2f2f'),
    bullWreck: wreck('rgba(20,70,45,0.85)'), bearWreck: wreck('rgba(80,28,28,0.85)'),
  }
}
let bfPrice = 0, bfLastT = 0, bfLastTrade = 0
// aggTrade 串流在部分網路環境收不到(實測:depth20 正常、aggTrade 一筆都沒有,
// 但 REST /aggTrades 正常)。沒有成交就沒有士兵 → 戰場只剩城堡。
// 對策:看門狗偵測 WS 沒送成交就自動改用 REST 輪詢補上,WS 恢復就停掉輪詢。
let bfWsTradeAt = 0, bfPoll = null, bfDog = null, bfLastAggId = 0, bfQueue = []

async function loadBtcSR() {
  try {
    const res = await fetch('/api/btc-sr')
    if (res.ok) bfSR.value = await res.json()
  } catch (e) { /* 城牆缺席不影響戰場其他部分 */ }
}

// 遠方城牆: depth20 只涵蓋現價附近很窄一帶,摸不到 SR 位置的大牆 → 每 20s 補一張深快照。
async function bfLoadFar() {
  try {
    const r = await fetch('https://fapi.binance.com/fapi/v1/depth?symbol=BTCUSDT&limit=1000')
    if (!r.ok) return
    const d = await r.json()
    bfFar = { bids: d.bids.map((x) => [+x[0], +x[1]]), asks: d.asks.map((x) => [+x[0], +x[1]]) }
  } catch (e) { /* CORS/限流 → 就只用近端 depth20,畫面仍可運作 */ }
}

// 兵力對比(多空帳戶比)+ 部隊規模(OI)
async function bfLoadStats() {
  try {
    const [lsr, oir] = await Promise.all([
      fetch('https://fapi.binance.com/futures/data/globalLongShortAccountRatio?symbol=BTCUSDT&period=5m&limit=1'),
      fetch('https://fapi.binance.com/fapi/v1/openInterest?symbol=BTCUSDT'),
    ])
    if (lsr.ok) {
      const a = await lsr.json()
      if (a && a[0]) bfLive.value.longPct = +a[0].longAccount * 100
    }
    if (oir.ok) {
      const o = await oir.json()
      if (o && o.openInterest) bfLive.value.oi = +o.openInterest
    }
  } catch (e) { /* 非關鍵 */ }
}

function bfConnect() {
  if (bfWS) return
  const streams = ['btcusdt@aggTrade', 'btcusdt@forceOrder', 'btcusdt@depth20@100ms', 'btcusdt@markPrice@1s']
  try {
    bfWS = new WebSocket('wss://fstream.binance.com/stream?streams=' + streams.join('/'))
  } catch (e) { return }
  bfWS.onopen = () => { bfRetry = 0; bfLive.value.conn = true }
  bfWS.onclose = () => {
    bfLive.value.conn = false
    bfWS = null
    if (!bfOpen.value) return
    bfRetry = Math.min(bfRetry + 1, 6)
    setTimeout(bfConnect, 1000 * bfRetry) // 退避重連
  }
  bfWS.onerror = () => { try { bfWS && bfWS.close() } catch (e) {} }
  bfWS.onmessage = (ev) => {
    let m
    try { m = JSON.parse(ev.data) } catch (e) { return }
    const d = m.data
    if (!d) return
    if (d.e === 'aggTrade') { bfWsTradeAt = Date.now(); bfOnTrade(+d.p, +d.q, d.m) }
    else if (d.e === 'forceOrder' && d.o) bfOnLiq(+d.o.ap || +d.o.p, +d.o.q, d.o.S)
    else if (d.e === 'depthUpdate') {
      if (d.b) bfNear.bids = d.b.map((x) => [+x[0], +x[1]])
      if (d.a) bfNear.asks = d.a.map((x) => [+x[0], +x[1]])
      // 戰線備援:aggTrade 若沒進來(冷門時段,或個別串流被網路/防火牆擋掉),
      // 用最佳買賣中價當現價,免得整個戰場卡在「連線中…」。
      const bb = bfNear.bids[0], ba = bfNear.asks[0]
      if (bb && ba && Date.now() - bfLastTrade > 2000) {
        bfPrice = (bb[0] + ba[0]) / 2
        bfLive.value.price = bfPrice
      }
    } else if (d.e === 'markPriceUpdate') {
      if (!bfPrice) { bfPrice = +d.p; bfLive.value.price = bfPrice }
    }
  }
}
function bfDisconnect() {
  if (bfWS) { try { bfWS.onclose = null; bfWS.close() } catch (e) {} bfWS = null }
  bfLive.value.conn = false
}

// 一筆主動成交 → 一個士兵。m=isBuyerMaker: true 代表主動方是「賣家」(紅兵)。
// 高波動時每秒可達上百筆 → 小單聚合、總量設上限,免得中低階手機發燙掉幀。
function bfOnTrade(px, qty, buyerMaker) {
  bfPrice = px
  bfLive.value.price = px
  bfLastTrade = Date.now()
  const bull = !buyerMaker
  if (bull) bfLive.value.buy += qty
  else bfLive.value.sell += qty
  // 門檻實測校準:BTC 多數成交量都很小,原本 0.004(≈$260)會砍掉約 3/4 的兵,
  // 戰場看起來空無一人;坦克門檻 1.5 BTC(≈$97k)更是幾乎不會觸發。
  if (qty < 0.0008) return // 只濾真正的塵埃
  if (bfSoldiers.length > 260) return // 上限保護:高波動時每秒上百筆,免得中低階手機發燙掉幀
  // 兵種分級 ∝ 成交量:量級一眼看得出來,不再只有「兵/坦克」兩檔。
  //   步兵 <0.05 ｜ 裝甲車 0.05–0.5 ｜ 坦克 0.5–2 ｜ 轟炸機 ≥2(巨鯨,走空中)
  if (qty >= 2) { bfPlanes.push(bfMkPlane(bull, 'whale', qty)); return }
  const kind = qty >= 0.5 ? 'tank' : qty >= 0.05 ? 'apc' : 'inf'
  const tank = kind === 'tank'
  // 一筆大單 = 一個班,不是一個兵:兵力 ∝ 成交量,大錢進場才看得出份量。
  const squad = tank ? Math.min(3, 1 + Math.floor(Math.sqrt(qty / 0.5))) : kind === 'apc' ? 2 : 1
  // 自穩定的停留時間。BTC 成交率忽高忽低(實測穩態在 2 兵到 54 兵之間跳),
  // 固定壽命的結果就是冷清時空無一人、熱絡時糊成一坨色塊。改成「人少活久、
  // 人多活短」把場上人數收斂到 ~28:每個兵仍然是一筆真實成交,只是停留時間隨場面調整。
  const lifeMul = Math.max(0.5, Math.min(4.5, 26 / Math.max(4, bfSoldiers.length)))
  for (let i = 0; i < squad; i++) {
    bfSoldiers.push({
      bull, tank, kind,
      scale: tank ? Math.min(1.5, 0.85 + Math.sqrt(qty) * 0.5) : Math.min(1.15, 0.6 + Math.sqrt(qty) * 2.4),
      u: bull ? BF_SUPX : BF_RESX, // 從自家城堡出發
      state: 'march',
      spd: (0.0055 + Math.random() * 0.004) * (tank ? 0.65 : 1), // 坦克較慢
      // 交戰壽命。實測穩態只有 3–6 兵(不是註解原本說的 50–70):BTC 平靜時
      // 每秒才 3–5 筆成交,舊的 1–2 秒壽命根本堆不出肉搏帶。拉長到 3–6 秒後
      // 穩態約 15–30 兵,平靜盤也看得到交戰(上限 260 仍是效能保險絲)。
      hp: (tank ? 220 + Math.random() * 160 : 140 + Math.random() * 170) * lifeMul,
      ph: Math.random() * 6.28, // 刺擊動畫相位
      lunge: 0,
      lane: Math.random(), // 0..1 縱深車道
    })
  }
}
// bfMkPlane 造一架轟炸機。whale = 巨鯨成交(≥2 BTC),從自家方向飛入戰線投彈;
// liq = 清算空襲,由「贏的那方」飛過來炸被強平的一方。
function bfMkPlane(bull, kind, mag) {
  return {
    bull, kind,
    u: bull ? -0.25 : 1.25,
    sky: 0.28 + Math.random() * 0.34,          // 0..1:天空高度(0=地平線)
    spd: 0.0042 + Math.random() * 0.002, // 慢一點才看得清楚:0.010 時整趟只有 3 秒
    dropU: 0.5 + (Math.random() - 0.5) * 0.12, // 投彈點(接近戰線)
    dropped: false,
    mag: Math.min(60, 16 + Math.sqrt(mag) * 8), // 爆炸半徑
  }
}

function bfOnLiq(px, qty, side) {
  // side=SELL → 多單被清算(強制賣出);BUY → 空單被清算
  const long = side === 'SELL'
  const usd = px * qty
  if (long) bfLive.value.liqLong += usd
  else bfLive.value.liqShort += usd
  // 爆炸打在戰線附近(帶點隨機偏移),不是絕對價格位置
  bfBlasts.push({ long, off: (Math.random() - 0.5) * 0.1, r: 0, max: Math.min(52, 12 + Math.sqrt(usd) / 20), t: 0 })
  // 夠大的清算 → 加派空襲:贏家(被清算方的對面)飛過來投彈,比原地爆一團有戲
  if (usd > 20000 && bfPlanes.length < 4) bfPlanes.push(bfMkPlane(!long, 'liq', usd / 12000))
}

// REST 成交備援:WS 的 aggTrade 沒供應時改用輪詢。以 aggTrade id (a) 去重,
// 新成交丟進佇列由畫格慢慢放兵,避免每 2 秒一次爆量湧現的脈衝感。
async function bfPollTrades() {
  try {
    const r = await fetch('https://fapi.binance.com/fapi/v1/aggTrades?symbol=BTCUSDT&limit=100')
    if (!r.ok) return
    const arr = await r.json()
    if (!Array.isArray(arr) || !arr.length) return
    const seed = bfLastAggId === 0
    for (const t of arr) {
      if (t.a <= bfLastAggId) continue
      bfLastAggId = t.a
      if (!seed) bfQueue.push([+t.p, +t.q, t.m])
    }
    if (seed) for (const t of arr.slice(-12)) bfQueue.push([+t.p, +t.q, t.m]) // 首輪只放最後幾筆,不要一口氣倒 100 個兵
    if (bfQueue.length > 300) bfQueue = bfQueue.slice(-300)
  } catch (e) { /* 限流/離線 → 下一輪再試 */ }
}
function bfWatchdog() {
  const wsAlive = Date.now() - bfWsTradeAt < 6000
  if (!wsAlive && !bfPoll) { bfPoll = setInterval(bfPollTrades, 2000); bfPollTrades() }
  else if (wsAlive && bfPoll) { clearInterval(bfPoll); bfPoll = null } // WS 恢復 → 收掉輪詢
}

// 戰場座標系。⚠️ X 軸「不是」絕對價格 — 那是第一版的致命錯誤:SR 城牆常離現價
// ±1%,但訂單簿與成交都擠在 ±0.01% 內,尺度差近百倍 → 畫面 95% 是空的、城牆還會
// 疊在戰線上。改成「價格在 支撐↔壓力 之間的相對位置」:城牆固定釘在畫面兩側,
// 戰線在中間移動。畫面永遠是滿的,而且戰線位置直接回答「離攻破還有多遠」。
const BF_SUPX = 0.14, BF_RESX = 0.86 // 兩座城牆的戰場座標 (0..1)

function bfWalls() {
  const p = bfPrice || bfLive.value.price
  const sr = bfSR.value
  let sup = sr && sr.sup_ok ? sr.support : 0
  let res = sr && sr.res_ok ? sr.resistance : 0
  const supReal = sup > 0, resReal = res > 0
  if (!supReal) sup = p * 0.9965 // 沒有實測城牆時用假想防線,畫面才不會退化
  if (!resReal) res = p * 1.0035
  if (res <= sup) return { sup: p * 0.9965, res: p * 1.0035, supReal: false, resReal: false }
  return { sup, res, supReal, resReal }
}
// 戰線 0..1(0=支撐牆、1=壓力牆);已攻破時允許溢出一小段以示突破
function bfFrontU() {
  const p = bfPrice || bfLive.value.price
  const w = bfWalls()
  if (!p) return 0.5
  const n = (p - w.sup) / (w.res - w.sup)
  return Math.max(-0.1, Math.min(1.1, BF_SUPX + n * (BF_RESX - BF_SUPX)))
}
// 城牆後方的「後備軍」= 該側掛單量(訂單簿 = 靜止的防禦工事)
function bfReserves() {
  const p = bfPrice || bfLive.value.price
  const w = bfWalls()
  let bid = 0, ask = 0
  for (const [q, v] of bfFar.bids) if (q >= w.sup && q <= p) bid += v
  for (const [q, v] of bfFar.asks) if (q <= w.res && q >= p) ask += v
  if (!bid) for (const [, v] of bfNear.bids) bid += v
  if (!ask) for (const [, v] of bfNear.asks) ask += v
  return { bid, ask }
}

function bfDraw() {
  const cv = bfCanvas.value
  if (!cv) return
  const ctx = cv.getContext('2d')
  const dpr = Math.min(window.devicePixelRatio || 1, 2)
  const W = cv.clientWidth, H = cv.clientHeight
  if (!W || !H) return
  if (cv.width !== W * dpr || cv.height !== H * dpr) { cv.width = W * dpr; cv.height = H * dpr }
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  ctx.clearRect(0, 0, W, H)
  const p = bfPrice || bfLive.value.price
  if (!p) { ctx.fillStyle = '#8b909a'; ctx.font = '13px sans-serif'; ctx.textAlign = 'center'; ctx.fillText('連線中…', W / 2, H / 2); return }

  const horizon = H * 0.26
  const groundH = H - horizon
  // 等距投影:u = 戰場橫向座標 (0..1),t = 縱深 (0=地平線, 1=最前緣)
  const PX = (u, t) => W / 2 + (u - 0.5) * W * (0.5 + 0.5 * t)
  const PY = (t) => horizon + groundH * t
  // 車道 → 縱深。士兵、火花、餘燼共用同一條換算:火花的 lane 是從陣亡的士兵複製過來的,
  // 兩邊若用不同公式,爆點就會跟屍體錯開。
  const LT = (lane) => 0.58 + lane * 0.40

  const w = bfWalls()
  const fu = bfFrontU()
  const now = performance.now()
  const dt = bfLastT ? Math.min(64, now - bfLastT) : 16
  bfLastT = now
  const step = dt / 16
  // REST 備援的成交佇列 → 每格放少量兵,避免每輪輪詢一次的脈衝感
  for (let i = 0; i < 3 && bfQueue.length; i++) { const q = bfQueue.shift(); bfOnTrade(q[0], q[1], q[2]) }

  // ---- 天空:依主動買賣優勢染色 ----
  const tot = bfLive.value.buy + bfLive.value.sell
  const dom = tot > 0 ? (bfLive.value.buy - bfLive.value.sell) / tot : 0
  const sky = ctx.createLinearGradient(0, 0, 0, horizon)
  sky.addColorStop(0, dom >= 0 ? `rgba(46,194,107,${0.06 + dom * 0.2})` : `rgba(226,74,74,${0.06 - dom * 0.2})`)
  sky.addColorStop(1, 'rgba(13,15,20,0)')
  ctx.fillStyle = sky; ctx.fillRect(0, 0, W, horizon)

  // ---- 地面梯形:戰線左邊多方領土、右邊空方領土 ----
  const band = (u0, u1, col) => {
    ctx.beginPath()
    ctx.moveTo(PX(u0, 0), PY(0)); ctx.lineTo(PX(u1, 0), PY(0))
    ctx.lineTo(PX(u1, 1), PY(1)); ctx.lineTo(PX(u0, 1), PY(1))
    ctx.closePath(); ctx.fillStyle = col; ctx.fill()
  }
  band(-0.15, fu, 'rgba(35,120,70,0.5)')
  band(fu, 1.15, 'rgba(150,50,50,0.5)')
  ctx.strokeStyle = 'rgba(255,255,255,0.05)'; ctx.lineWidth = 1
  for (let i = 1; i < 5; i++) { const y = PY(i / 5); ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(W, y); ctx.stroke() }

  // ---- 飄塵(裝飾):讓大片空地不至於是死的色塊 ----
  while (bfDust.length < 26) bfDust.push({ u: Math.random() * 1.3 - 0.15, t: 0.3 + Math.random() * 0.7, sp: 0.0004 + Math.random() * 0.0012, r: 0.6 + Math.random() * 1.6, a: 0.05 + Math.random() * 0.13 })
  bfDust = bfDust.filter((d) => {
    if (d.life) { d.t2 = (d.t2 || 0) + step; if (d.t2 > d.life) return false } // 衝鋒揚塵:有壽命
    d.u += d.sp * step * (dom >= 0 ? 1 : -1) // 常駐飄塵:順著主動買賣的優勢方向飄
    if (d.u > 1.2) d.u = -0.15
    if (d.u < -0.2) d.u = 1.15
    const a = d.life ? d.a * (1 - (d.t2 || 0) / d.life) : d.a
    ctx.fillStyle = `rgba(200,208,220,${a})`
    ctx.beginPath(); ctx.arc(PX(d.u, d.t), PY(d.t), d.r, 0, 7); ctx.fill()
    return true
  })

  // ---- 城堡 ----
  const rsv = bfReserves()
  const rsvMax = Math.max(rsv.bid, rsv.ask, 1e-9)
  const castle = (u, touches, bull, broken, real, price, reserve) => {
    const t = 0.66
    const x = PX(u, t), y = PY(t)
    const th = Math.min(30, 12 + touches * 3.4) // 牆厚 ∝ 觸及次數
    const hgt = groundH * 0.46
    const body = broken ? '#5b6068' : bull ? '#1f7a45' : '#8f2f2f'
    const face = broken ? '#767c86' : bull ? '#2ec26b' : '#d94a4a'
    if (!real) ctx.globalAlpha = 0.45 // 假想防線 → 半透明
    ctx.fillStyle = body
    ctx.fillRect(x - th, y - hgt, th * 2, hgt)
    ctx.fillStyle = face
    ctx.fillRect(x - th, y - hgt, th * 2, 5)
    for (let i = -2; i <= 2; i++) ctx.fillRect(x + i * th * 0.42 - 3, y - hgt - 7, 6, 8) // 城垛
    if (broken) {
      ctx.strokeStyle = '#ffbe50'; ctx.lineWidth = 2.5 // 裂縫
      ctx.beginPath(); ctx.moveTo(x - 2, y); ctx.lineTo(x + 6, y - hgt * 0.45); ctx.lineTo(x - 5, y - hgt * 0.75); ctx.lineTo(x + 3, y - hgt); ctx.stroke()
    } else { // 旗幟
      ctx.strokeStyle = face; ctx.lineWidth = 1.5
      ctx.beginPath(); ctx.moveTo(x, y - hgt - 7); ctx.lineTo(x, y - hgt - 20); ctx.stroke()
      ctx.fillStyle = face; ctx.beginPath()
      ctx.moveTo(x, y - hgt - 20); ctx.lineTo(x + (bull ? 12 : -12), y - hgt - 16); ctx.lineTo(x, y - hgt - 12)
      ctx.closePath(); ctx.fill()
    }
    // 後備軍(掛單量)條:城牆的「血量」
    const bw = th * 2, bh = 4
    ctx.fillStyle = 'rgba(255,255,255,0.12)'; ctx.fillRect(x - th, y + 4, bw, bh)
    ctx.fillStyle = face; ctx.fillRect(x - th, y + 4, bw * Math.min(1, reserve / rsvMax), bh)
    ctx.globalAlpha = 1
    ctx.textAlign = 'center'
    ctx.fillStyle = broken ? '#ffbe50' : '#e8e9ec'
    ctx.font = 'bold 11px sans-serif'
    ctx.fillText(broken ? '⚔ 已攻破' : '×' + touches + ' 城牆', x, y - hgt - 26)
    ctx.fillStyle = '#8b909a'; ctx.font = '10px sans-serif'
    ctx.fillText('$' + Math.round(price).toLocaleString(), x, y + 18)
  }
  const sr = bfSR.value
  castle(BF_SUPX, sr && sr.sup_ok ? sr.sup_touches : 0, true, !!(sr && sr.status === 'break_down'), w.supReal, w.sup, rsv.bid)
  castle(BF_RESX, sr && sr.res_ok ? sr.res_touches : 0, false, !!(sr && sr.status === 'break_up'), w.resReal, w.res, rsv.ask)

  if (!bfSprites) bfMakeSprites() // 殘骸/砲兵/飛機都要用,必須在它們之前備好

  // ---- 殘骸:躺在地上慢慢淡掉,看得出剛剛哪一段打得最兇 ----
  bfWrecks = bfWrecks.filter((k) => {
    k.t += step
    if (k.t > k.life) return false
    const t = LT(k.lane)
    const sp = k.bull ? bfSprites.bullWreck : bfSprites.bearWreck
    const sc = (k.big ? 1.5 : 1) * (0.75 + t * 0.55)
    ctx.globalAlpha = Math.min(0.7, (1 - k.t / k.life) * 1.4)
    ctx.drawImage(sp, PX(k.u, t) - (sp.width * sc) / 2, PY(t) - sp.height * sc, sp.width * sc, sp.height * sc)
    ctx.globalAlpha = 1
    return true
  })
  if (bfWrecks.length > 70) bfWrecks = bfWrecks.slice(-70)

  // ---- 砲兵:駐守自家城牆後方,朝戰線拋射。齊射頻率 ∝ 掛單量(後備軍力) ----
  const artyDraw = (u, bull, reserve) => {
    const t = 0.52
    const sp = bull ? bfSprites.bullArty : bfSprites.bearArty
    const k = 0.85 * (0.75 + t * 0.55)
    const x = PX(u, t), y = PY(t)
    ctx.save(); ctx.translate(x, y); if (!bull) ctx.scale(-1, 1)
    ctx.drawImage(sp, (-sp.width * k) / 2, -sp.height * k, sp.width * k, sp.height * k)
    ctx.restore()
    return { x, y, t, ratio: reserve / rsvMax }
  }
  const aB = artyDraw(BF_SUPX - 0.06, true, rsv.bid)
  const aR = artyDraw(BF_RESX + 0.06, false, rsv.ask)
  bfArtyT += step
  if (bfArtyT > 26 && bfShells.length < 14) { // 節流:齊射不能無限制,手機吃不消
    bfArtyT = 0
    const fire = (from, bull, ratio) => {
      if (Math.random() > 0.35 + ratio * 0.5) return // 後備軍越厚,開火越勤
      bfShells.push({ bull, u0: bull ? BF_SUPX - 0.06 : BF_RESX + 0.06, t0: 0.52,
        u1: fu + (bull ? -1 : 1) * (0.02 + Math.random() * 0.06), t1: 0.72 + Math.random() * 0.2, k: 0, sp: 0.020 + Math.random() * 0.012 })
    }
    fire(aB, true, aB.ratio); fire(aR, false, aR.ratio)
  }
  bfShells = bfShells.filter((sh) => {
    sh.k += sh.sp * step
    const u = sh.u0 + (sh.u1 - sh.u0) * sh.k
    const t = sh.t0 + (sh.t1 - sh.t0) * sh.k
    const arc = Math.sin(Math.PI * Math.min(1, sh.k)) * 46 // 拋物線
    const x = PX(u, t), y = PY(t) - arc
    if (sh.k >= 1) { // 落地 → 小爆點 + 揚塵
      bfSparks.push({ u, lane: (t - 0.58) / 0.40, t: 0, life: 16, bull: sh.bull, death: true })
      for (let i = 0; i < 3; i++) bfEmbers.push({ u: u + (Math.random() - 0.5) * 0.02, lane: (t - 0.58) / 0.40, t: 0, life: 26 + Math.random() * 20, dx: (Math.random() - 0.5) * 0.4 })
      return false
    }
    ctx.fillStyle = sh.bull ? 'rgba(150,255,200,0.95)' : 'rgba(255,180,180,0.95)'
    ctx.beginPath(); ctx.arc(x, y, 2.1, 0, 7); ctx.fill()
    ctx.fillStyle = sh.bull ? 'rgba(90,220,150,0.25)' : 'rgba(230,120,120,0.25)' // 彈道殘影
    ctx.beginPath(); ctx.arc(x - (sh.u1 > sh.u0 ? 4 : -4), y + 3, 1.4, 0, 7); ctx.fill()
    return true
  })

  // ---- 士兵:行軍 → 抵達戰線 → 停下對砍 → 陣亡 ----
  // 兩軍都停在戰線上肉搏,所以中間會自然堆出一條交戰帶(這才是「多空交戰」)。
  if (!bfSprites) bfMakeSprites()
  bfSoldiers.sort((a, b) => a.lane - b.lane) // 遠的先畫,近的疊在上面
  bfSoldiers = bfSoldiers.filter((s) => {
    const dir = s.bull ? 1 : -1
    if (s.state === 'march') {
      // 衝鋒波:主動買賣極度失衡時,優勢方整批加速推進
      const charge = Math.abs(dom) > 0.35 && ((dom > 0) === s.bull) ? 1.9 : 1
      s.u += dir * s.spd * charge * step
      if (charge > 1 && Math.random() < 0.06 * step) { // 衝鋒揚塵
        bfDust.push({ u: s.u, t: LT(s.lane), sp: 0, r: 1.2 + Math.random(), a: 0.22, life: 30 + Math.random() * 20 })
      }
      // 觸及戰線 → 進入交戰(留一點點間隙,兩軍才會面對面而不是重疊)
      // 交戰帶要有「厚度」:全部擠在同一條線上會糊成一坨色塊,散開才看得出是兩軍列陣
      if ((s.bull && s.u >= fu - 0.014) || (!s.bull && s.u <= fu + 0.014)) {
        s.state = 'fight'
        s.u = fu - dir * (0.014 + Math.random() * 0.075)
      }
    } else {
      s.hp -= step
      if (s.hp <= 0) { // 陣亡 → 留下殘骸
        bfSparks.push({ u: s.u, lane: s.lane, t: 0, life: 14, bull: s.bull, death: true })
        bfWrecks.push({ u: s.u, lane: s.lane, bull: s.bull, t: 0, life: 520, big: s.kind !== 'inf' })
        return false
      }
      s.u += (fu - dir * (0.016 + s.lane * 0.06) - s.u) * 0.05 * step // 戰線移動時交戰帶跟著推移(保留厚度)
      s.ph += 0.34 * step
      s.lunge = Math.sin(s.ph) * 0.005 * dir // 刺擊
      if (Math.random() < 0.05 * step) { // 兵器碰撞火花
        bfSparks.push({ u: s.u + dir * 0.008, lane: s.lane, t: 0, life: 8, bull: s.bull })
      }
    }
    const t = LT(s.lane)
    const x = PX(s.u + s.lunge, t), y = PY(t)
    const sp = s.kind === 'tank' ? (s.bull ? bfSprites.bullTank : bfSprites.bearTank)
      : s.kind === 'apc' ? (s.bull ? bfSprites.bullApc : bfSprites.bearApc)
        : (s.bull ? bfSprites.bull : bfSprites.bear)
    const k = s.scale * (0.75 + t * 0.55) // 近大遠小
    const w = sp.width * k, h = sp.height * k
    ctx.save()
    ctx.translate(x, y)
    if (!s.bull) ctx.scale(-1, 1) // sprite 一律面向右 → 空方翻面
    ctx.drawImage(sp, -w / 2, -h, w, h)
    ctx.restore()
    return true
  })

  // ---- 交戰火花 / 陣亡 ----
  bfSparks = bfSparks.filter((k) => {
    k.t += step
    if (k.t > k.life) return false
    const t = LT(k.lane)
    const x = PX(k.u, t), y = PY(t)
    const a = 1 - k.t / k.life
    if (k.death) {
      ctx.fillStyle = k.bull ? `rgba(61,219,132,${a * 0.7})` : `rgba(255,107,107,${a * 0.7})`
      ctx.beginPath(); ctx.arc(x, y - 4, 5 * (1 + k.t / k.life), 0, 7); ctx.fill()
    } else {
      ctx.fillStyle = `rgba(255,235,150,${a})`
      ctx.fillRect(x - 1.5, y - 9 - k.t * 0.3, 3, 3)
    }
    return true
  })
  if (bfSparks.length > 140) bfSparks = bfSparks.slice(-140)

  // ---- 清算爆炸(打在戰線上) ----
  bfBlasts = bfBlasts.filter((b) => {
    b.t += step; b.r += (b.max - b.r) * 0.16 * step
    const a = Math.max(0, 1 - b.t / 40)
    if (a <= 0) return false
    const x = PX(fu + b.off, 0.8), y = PY(0.8)
    const g = ctx.createRadialGradient(x, y - 8, 0, x, y - 8, Math.max(1, b.r))
    g.addColorStop(0, `rgba(255,255,220,${a})`)
    g.addColorStop(0.4, b.long ? `rgba(255,120,40,${a * 0.9})` : `rgba(90,200,255,${a * 0.9})`)
    g.addColorStop(1, 'rgba(0,0,0,0)')
    ctx.fillStyle = g; ctx.beginPath(); ctx.arc(x, y - 8, Math.max(1, b.r), 0, 7); ctx.fill()
    return true
  })
  if (bfBlasts.length > 30) bfBlasts = bfBlasts.slice(-30)

  // ---- 交戰帶光暈 + 餘燼:強度 ∝ 正在肉搏的兵力,所以打得越兇燒得越旺 ----
  const melee = bfSoldiers.reduce((n, s) => n + (s.state === 'fight' ? 1 : 0), 0)
  if (melee > 0) {
    const heat = Math.min(1, melee / 26)
    const gx = PX(fu, 0.8), gy = PY(0.8)
    const gg = ctx.createRadialGradient(gx, gy - 6, 0, gx, gy - 6, 26 + heat * 74)
    gg.addColorStop(0, `rgba(255,206,120,${0.10 + heat * 0.20})`)
    gg.addColorStop(0.45, `rgba(255,150,60,${0.05 + heat * 0.10})`)
    gg.addColorStop(1, 'rgba(0,0,0,0)')
    ctx.fillStyle = gg; ctx.beginPath(); ctx.arc(gx, gy - 6, 26 + heat * 74, 0, 7); ctx.fill()
    if (Math.random() < 0.55 * heat * step) {
      bfEmbers.push({ u: fu + (Math.random() - 0.5) * 0.05, lane: Math.random(), t: 0, life: 40 + Math.random() * 40, dx: (Math.random() - 0.5) * 0.3 })
    }
  }
  bfEmbers = bfEmbers.filter((e) => {
    e.t += step
    if (e.t > e.life) return false
    const t = LT(e.lane)
    const a = (1 - e.t / e.life) * 0.75
    ctx.fillStyle = `rgba(255,${170 + Math.round(60 * (1 - e.t / e.life))},90,${a})`
    ctx.beginPath(); ctx.arc(PX(e.u, t) + e.dx * e.t, PY(t) - 6 - e.t * 0.55, 1.3, 0, 7); ctx.fill()
    return true
  })
  if (bfEmbers.length > 90) bfEmbers = bfEmbers.slice(-90)

  // ---- 轟炸機:巨鯨成交 / 清算空襲。飛過戰線投彈,落地接爆炸 ----
  bfPlanes = bfPlanes.filter((pl) => {
    pl.u += (pl.bull ? 1 : -1) * pl.spd * step
    if (pl.u > 1.45 || pl.u < -0.45) return false
    const skyY = horizon * (1 - pl.sky) + 6
    const x = W / 2 + (pl.u - 0.5) * W * 0.92
    if (!pl.dropped && ((pl.bull && pl.u >= pl.dropU) || (!pl.bull && pl.u <= pl.dropU))) {
      pl.dropped = true
      bfBombs.push({ u: pl.u, y: skyY, bull: pl.bull, mag: pl.mag, kind: pl.kind })
    }
    const sp = pl.bull ? bfSprites.bullPlane : bfSprites.bearPlane
    ctx.save(); ctx.translate(x, skyY); if (!pl.bull) ctx.scale(-1, 1)
    ctx.drawImage(sp, -sp.width / 2, -sp.height / 2)
    ctx.restore()
    return true
  })
  if (bfPlanes.length > 5) bfPlanes = bfPlanes.slice(-5)
  // 落下的炸彈 → 觸地變爆炸
  bfBombs = bfBombs.filter((b) => {
    b.y += 3.4 * step
    const gy = PY(0.8)
    const x = W / 2 + (b.u - 0.5) * W * 0.92
    if (b.y >= gy - 8) {
      bfBlasts.push({ long: !b.bull, off: b.u - fu, r: 0, max: b.mag, t: 0 })
      return false
    }
    ctx.fillStyle = '#ffd479'
    ctx.beginPath(); ctx.ellipse(x, b.y, 2.2, 4, 0, 0, 7); ctx.fill()
    return true
  })

  // ---- 戰線(現價) ----
  const x0 = PX(fu, 0), x1 = PX(fu, 1)
  const grad = ctx.createLinearGradient(x0, horizon, x1, H)
  grad.addColorStop(0, 'rgba(216,173,72,0.35)')
  grad.addColorStop(1, 'rgba(216,173,72,1)')
  ctx.strokeStyle = grad; ctx.lineWidth = 3
  ctx.beginPath(); ctx.moveTo(x0, horizon); ctx.lineTo(x1, H); ctx.stroke()
  ctx.fillStyle = '#d8ad48'; ctx.font = 'bold 13px sans-serif'; ctx.textAlign = 'center'
  ctx.fillText('$' + p.toLocaleString(undefined, { maximumFractionDigits: 0 }), Math.max(34, Math.min(W - 34, x0)), horizon - 8)
}
function bfLoop() { bfDraw(); bfRAF = requestAnimationFrame(bfLoop) }
function bfStart() {
  if (bfRAF) return
  bfConnect()
  bfLoadFar(); bfLoadStats(); loadBtcSR()
  bfFarTimer = setInterval(bfLoadFar, 20000)
  bfStatTimer = setInterval(() => { bfLoadStats(); loadBtcSR() }, 60000)
  bfDog = setInterval(bfWatchdog, 3000) // WS 沒送成交 → 自動切 REST 輪詢
  setTimeout(bfWatchdog, 4000)          // 開場先給 WS 一點時間
  bfLastT = 0
  bfRAF = requestAnimationFrame(bfLoop)
}
function bfStop() {
  if (bfRAF) { cancelAnimationFrame(bfRAF); bfRAF = null }
  clearInterval(bfFarTimer); clearInterval(bfStatTimer); clearInterval(bfDog)
  clearInterval(bfPoll); bfPoll = null
  bfDisconnect()
  bfSoldiers = []; bfBlasts = []; bfSparks = []; bfQueue = []; bfDust = []; bfEmbers = []
  bfShells = []; bfPlanes = []; bfWrecks = []; bfBombs = []; bfArtyT = 0
}
function bfToggle() { bfOpen.value = !bfOpen.value; bfOpen.value ? bfStart() : bfStop() }
const bfPressure = computed(() => {
  const b = bfLive.value.buy, s = bfLive.value.sell
  return b + s > 0 ? (b / (b + s)) * 100 : 50
})
function bfUsd(v) {
  if (!v) return '0'
  if (v >= 1e6) return (v / 1e6).toFixed(1) + 'M'
  if (v >= 1e3) return (v / 1e3).toFixed(0) + 'K'
  return v.toFixed(0)
}


// ---- 生命週期:自己管 WS 與動畫迴圈 ----
// 分頁切到背景時停掉(不燒電、不佔連線),回到前景再續。
function bfOnVisibility() {
  if (document.visibilityState === 'hidden') bfStop()
  else if (bfOpen.value) bfStart()
}
onMounted(() => {
  if (bfOpen.value) bfStart()
  document.addEventListener('visibilitychange', bfOnVisibility)
})
onUnmounted(() => {
  document.removeEventListener('visibilitychange', bfOnVisibility)
  bfStop()
})
</script>

<template>
<!-- 戰場: BTC 多空交戰 (即時,瀏覽器直連交易所) -->
<div class="bf">
  <div class="bf-top">
    <span class="bf-title">
      <span class="bf-dot" :class="{ off: !bfLive.conn }"></span>BTC 多空交戰
      <span class="help" tabindex="0">?<span class="help-pop">
        戰場即時反映真實買賣:<b>X 軸就是價格</b>。中央金線是<b>現價戰線</b>;
        右邊紅色要塞是<b>壓力位</b>(空方堡壘)、左邊綠色要塞是<b>支撐位</b>(多方堡壘),
        <b>牆的厚度 = 該價位被測試過幾次</b>,被跌破/突破時會裂開並標記「已攻破」。
        地形起伏是<b>訂單簿掛單量</b>;綠兵向右衝=主動買、紅兵向左衝=主動賣;
        爆炸是<b>強制平倉(清算)</b>。<br><br>
        ⚠️ 這是盤面視覺化,僅供參考,不構成投資建議。
      </span></span>
    </span>
    <button class="bf-fold" @click="bfToggle">{{ bfOpen ? '收合' : '展開' }}</button>
  </div>
  <template v-if="bfOpen">
    <canvas ref="bfCanvas" class="bf-cv"></canvas>
    <div class="bf-bar" :title="'主動買 ' + bfPressure.toFixed(0) + '%'">
      <div class="bf-bar-fill" :style="{ width: bfPressure + '%' }"></div>
      <span class="bf-bar-l">主動買 {{ bfPressure.toFixed(0) }}%</span>
      <span class="bf-bar-r">{{ (100 - bfPressure).toFixed(0) }}% 主動賣</span>
    </div>
    <div class="bf-stats">
      <span><i class="bf-k">多方帳戶</i>{{ bfLive.longPct.toFixed(0) }}%</span>
      <span><i class="bf-k">未平倉</i>{{ bfUsd(bfLive.oi) }} BTC</span>
      <span><i class="bf-k bull">多單陣亡</i>${{ bfUsd(bfLive.liqLong) }}</span>
      <span><i class="bf-k bear">空單陣亡</i>${{ bfUsd(bfLive.liqShort) }}</span>
      <span v-if="bfSR && bfSR.sup_ok"><i class="bf-k">支撐城牆</i>${{ Math.round(bfSR.support).toLocaleString() }}</span>
      <span v-if="bfSR && bfSR.res_ok"><i class="bf-k">壓力城牆</i>${{ Math.round(bfSR.resistance).toLocaleString() }}</span>
    </div>
  </template>
</div>

</template>

<!-- 不加 scoped:全站 CSS 都是全域的,且 .bf-dot 借用了 App.vue 的 maipulse 動畫 -->
<style>
/* 戰場 (BTC 多空交戰) */
.bf { background: #14161c; border: 1px solid #23262f; border-radius: 12px; padding: 11px 12px 12px; margin-bottom: 14px; }
.bf-top { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 8px; }
.bf-title { font-size: 14px; font-weight: 700; color: #d8ad48; display: inline-flex; align-items: center; gap: 7px; }
.bf-dot { width: 8px; height: 8px; border-radius: 50%; background: #2ec26b; animation: maipulse 1.8s infinite; }
.bf-dot.off { background: #6b7280; animation: none; }
.bf-fold { background: #1b1e26; border: 1px solid #2b2f3a; color: #b9bdc4; border-radius: 7px; padding: 3px 10px; font-size: 12px; cursor: pointer; }
.bf-cv { display: block; width: 100%; height: 260px; border-radius: 9px; background: linear-gradient(#0d0f14, #101319); }
.bf-bar { position: relative; height: 22px; background: linear-gradient(90deg, rgba(226,74,74,0.20), rgba(226,74,74,0.42)); border-radius: 6px; margin-top: 9px; overflow: hidden; }
/* 推進條:漸層 + 前緣發光,讓「誰在推」一眼看得出來,不是一塊死色 */
.bf-bar-fill { position: relative; height: 100%; background: linear-gradient(90deg, rgba(46,194,107,0.30), rgba(46,194,107,0.62)); box-shadow: 0 0 12px rgba(46,194,107,0.45); transition: width .5s ease; }
.bf-bar-fill::after { content: ''; position: absolute; top: 0; right: -1px; width: 2px; height: 100%; background: #7defb0; box-shadow: 0 0 8px #2ec26b; animation: bfedge 1.6s ease-in-out infinite; }
@keyframes bfedge { 0%, 100% { opacity: .45 } 50% { opacity: 1 } }
.bf-bar-l, .bf-bar-r { position: absolute; top: 0; line-height: 22px; font-size: 11px; font-weight: 700; color: #fff; text-shadow: 0 1px 3px rgba(0,0,0,0.7); }
.bf-bar-l { left: 8px; } .bf-bar-r { right: 8px; }
/* 數據列:改成小卡片,數字放大,不再是一排灰字 */
.bf-stats { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 9px; font-size: 13px; color: #e8e9ec; }
.bf-stats span { display: inline-flex; align-items: baseline; gap: 6px; background: #1a1d24; border: 1px solid #262a33; border-radius: 7px; padding: 4px 9px; font-weight: 700; }
.bf-k { font-style: normal; font-size: 11px; color: #8b909a; font-weight: 600; }
.bf-k.bull { color: #3ddb84; } .bf-k.bear { color: #ff6b6b; }
@media (max-width: 560px) { .bf-cv { height: 200px; } .bf-stats { font-size: 11px; gap: 5px 10px; } }
</style>
