// 共用的顯示格式化。App.vue 與各元件都會用到,所以放 lib 而不是各自複製一份。
//
// ⚠️ 這些是從 App.vue 原封不動搬過來的,不要「順手改良」——
// 例如 fmtPrice 會自己加上 $、null 回傳 '-',全站顯示都依賴這些細節。

// 百分比,永遠帶正負號(+1.23% / -1.23%)
export function fmtPct(n) {
  return (n >= 0 ? '+' : '') + n.toFixed(2) + '%'
}

// 價格:自帶 $,依數量級調整小數位,免得小幣顯示成 $0.00
export function fmtPrice(n) {
  if (n == null) return '-'
  if (n >= 1000) return '$' + n.toLocaleString('en-US', { maximumFractionDigits: 2 })
  if (n >= 1) return '$' + n.toFixed(n >= 100 ? 2 : 4)
  return '$' + n.toPrecision(4)
}

// 大數字縮寫(1.2B / 3.4M / 5.6K)
export function fmtNum(n) {
  const a = Math.abs(n)
  if (a >= 1e9) return (n / 1e9).toFixed(2) + 'B'
  if (a >= 1e6) return (n / 1e6).toFixed(1) + 'M'
  if (a >= 1e3) return (n / 1e3).toFixed(1) + 'K'
  return n.toFixed(2)
}

// 月/日 時:分(24 小時制)
export function fundClock(ms) {
  if (!ms) return '—'
  return new Date(ms).toLocaleString('zh-TW', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })
}

// ---- 策略表共用 ----

// 進場時間 / 出場時間
export function fmtClock(iso) {
  if (!iso) return '-'
  return new Date(iso).toLocaleString('zh-TW', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })
}

// 止盈位相對進場的幅度。順著單子方向算,所以空單的止盈(價格更低)一樣是正數。
export function lvlPct(t, lvl) {
  if (!t || !lvl || !t.entry) return ''
  const p = t.dir === 'short' ? ((t.entry - lvl) / t.entry) * 100 : ((lvl - t.entry) / t.entry) * 100
  return fmtPct(p)
}

// 漏斗百分比(小數一位)
export function pctOf(n, d) { return d > 0 ? Math.round((n / d) * 1000) / 10 : 0 }

// 出場結果 → 中文
export function outcomeCN(o) {
  return o === 'tp' ? '止盈 TP' : o === 'tp3' ? 'TP3 完整'
    : o === 'tp2sl' ? 'TP2後出場' : o === 'tp1sl' ? 'TP1後保本'
      : o === 'sl' ? '止損 SL' : o === 'trail' ? '移動止損'
        : o === 'reversed' ? '反向出場' : o === 'hedge' ? '套保出場'
          : o === 'momdead' ? '動能衰弱' : o === 'expired' ? '逾時' : o
}

// 出場結果 → 樣式類別
export function outcomeCls(o) {
  if (o === 'tp' || o === 'tp3' || o === 'tp2sl') return 'tp'
  if (o === 'tp1sl') return 'reversed'
  if (o === 'sl') return 'sl'
  if (o === 'trail' || o === 'reversed' || o === 'hedge' || o === 'momdead') return 'reversed'
  return 'expired'
}
