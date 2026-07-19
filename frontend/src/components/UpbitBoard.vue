<!--
  Upbit 公告(公開,標題已翻成 zh-TW)。資料由外層持有(導覽列徽章要用筆數)。
-->
<script setup>
defineProps({ upbitNotices: { type: Array, default: () => [] } })

// 韓國時間字串 → 本地顯示;解析失敗就原樣顯示
function upbitTime(s) {
  if (!s) return ""
  const d = new Date(s)
  if (isNaN(d)) return s
  return d.toLocaleString("zh-TW", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false })
}
</script>

<template>
  <section>
  <div class="mk-head">
    <h2>Upbit 公告<span class="help" tabindex="0">?<span class="help-pop">韓國 Upbit 交易所官方公告。原文為韓文,已自動翻譯為繁體中文;點擊可開啟原公告。<b>「上架」標記代表新幣上架/交易支援類公告</b>,通常對該幣種有較大影響。</span></span></h2>
    <span class="mk-count">共 {{ upbitNotices.length }} 筆</span>
  </div>
  <table v-if="upbitNotices.length" class="grid">
    <thead><tr><th>時間</th><th>標題(繁中)</th><th class="r">類型</th></tr></thead>
    <tbody>
      <tr v-for="n in upbitNotices" :key="n.id" :class="{ 'ev-soon': n.listing }">
        <td class="tsmall">{{ upbitTime(n.listed_at) }}</td>
        <td>
          <a :href="n.url" target="_blank" rel="noopener" class="upbit-link">{{ n.title_zh }}</a>
          <div class="upbit-orig">{{ n.title }}</div>
        </td>
        <td class="r">
          <span v-if="n.listing" class="otag tp">🚀 上架</span>
          <span v-else class="otag expired">公告</span>
        </td>
      </tr>
    </tbody>
  </table>
  <p v-else class="loading">載入 Upbit 公告中…(首次載入需翻譯,請稍候)</p>
  </section>
</template>
