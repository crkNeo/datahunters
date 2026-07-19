<!--
  市場快訊(公開,GDELT 標題翻成 zh-TW)。資料由外層持有(導覽列徽章要用筆數);
  分類篩選是元件自己的狀態。
-->
<script setup>
import { ref, computed } from "vue"

const props = defineProps({ news: { type: Array, default: () => [] } })

// 新聞時間戳 → 本地顯示(與 Upbit 公告同格式)
function upbitTime(s) {
  if (!s) return ""
  const d = new Date(s)
  if (isNaN(d)) return s
  return d.toLocaleString("zh-TW", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false })
}

const newsCat = ref("")
const newsCatList = [
  { key: "figure", label: "🗣 人物" },
  { key: "cb", label: "🏦 央行" },
  { key: "trade", label: "📉 貿易" },
  { key: "geo", label: "⚔️ 地緣" },
  { key: "reg", label: "⚖️ 監管" },
  { key: "hack", label: "🚨 爆雷" },
  { key: "inst", label: "🏛 機構" },
  { key: "whale", label: "🐋 巨鯨" },
  { key: "crypto", label: "🪙 加密" },
  { key: "misc", label: "📰 綜合" },
]
const newsF = computed(() => (newsCat.value ? props.news.filter((n) => n.category === newsCat.value) : props.news))
</script>

<template>
  <section>
  <div class="mk-head">
    <h2>市場快訊<span class="help" tabindex="0">?<span class="help-pop">加密市場即時新聞頭條,依主題自動分類(人物/央行/貿易/地緣/監管/爆雷/機構/巨鯨/加密等)。英文原標題自動翻譯為繁中,點擊開原文。⚠️ 僅供風險參考,非投資建議。</span></span></h2>
    <span class="mk-count">共 {{ news.length }} 則 · 每 5 分更新</span>
  </div>
  <div class="timefilter" v-if="news.length">
    <button :class="{ on: newsCat === '' }" @click="newsCat = ''">全部</button>
    <button v-for="c in newsCatList" :key="c.key" :class="{ on: newsCat === c.key }" @click="newsCat = c.key">{{ c.label }}</button>
  </div>
  <table v-if="newsF.length" class="grid">
    <thead><tr><th>時間</th><th>類型</th><th>標題(繁中)</th><th>媒體</th></tr></thead>
    <tbody>
      <tr v-for="(n, i) in newsF" :key="i">
        <td class="tsmall">{{ upbitTime(n.time) }}</td>
        <td class="tsmall"><span class="newscat" :class="'nc-' + n.category">{{ n.label }}</span></td>
        <td>
          <a :href="n.url" target="_blank" rel="noopener" class="upbit-link">{{ n.title }}</a>
          <div v-if="n.title_en" class="upbit-orig">{{ n.title_en }}</div>
        </td>
        <td class="tsmall">{{ n.domain }}</td>
      </tr>
    </tbody>
  </table>
  <p v-else-if="news.length" class="empty">此分類暫無快訊</p>
  <p v-else class="loading">載入市場快訊中…(首次載入需翻譯,請稍候)</p>
  </section>
</template>
