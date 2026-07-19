<!--
  後台:站台設定 —— 品牌 logo、社群連結、首頁 QR。

  config / socialLinks 由外層傳入(前台頁尾與首頁也要用同一份);存檔後
  emit('saved') 讓外層重新載入 /api/config。
-->
<script setup>
import { ref, watch } from 'vue'
import { authFetch } from '../../lib/api'
import { uploadImage } from '../../lib/upload'
import { socialInfo, socialSvg } from '../../lib/social'

const props = defineProps({
  config: { type: Object, default: () => ({}) },
  socialLinks: { type: Array, default: () => [] },
})
const emit = defineEmits(['saved', 'msg', 'toast'])

const cfgSocial = ref([])
watch(() => props.socialLinks, (v) => { cfgSocial.value = (v || []).map((s) => ({ ...s })) }, { immediate: true, deep: true })

function addSocial() { cfgSocial.value.push({ platform: 'youtube', url: '' }) }
function removeSocial(i) { cfgSocial.value.splice(i, 1) }

async function setConfig(key, value) {
  const res = await authFetch('/api/admin/config', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ key, value }),
  })
  if (res.ok) { emit('saved'); emit('msg', '✓ 已儲存設定') }
  return res.ok
}
async function saveSocial() { await setConfig('social', JSON.stringify(cfgSocial.value.filter((s) => s.url))) }

const fail = (m) => emit('toast', m, 'err')
async function onLogoPick(e) {
  const f = e.target.files && e.target.files[0]
  if (f) { const p = await uploadImage(f, 'logo', fail); if (p) await setConfig('logo', p) }
}
async function onQrPick(e) {
  const f = e.target.files && e.target.files[0]
  if (f) { const p = await uploadImage(f, 'qr', fail); if (p) await setConfig('qr', p) }
}
</script>

<template>
<section class="card adminbox">
  <h3 class="psub">站台設定</h3>
  <div class="cfg-row">
    <span class="cfg-k">品牌 Logo</span>
    <img v-if="config.logo" :src="config.logo" class="cfg-logo" />
    <label class="authfile cfg-file"><span>上傳 Logo</span><input type="file" accept="image/*,.heic,.heif" hidden @change="onLogoPick" /></label>
    <button v-if="config.logo" class="delbtn" @click="setConfig('logo', '')">清除</button>
  </div>
  <div class="cfg-row">
    <span class="cfg-k">首頁 QR</span>
    <img v-if="config.qr" :src="config.qr" class="cfg-qr" />
    <label class="authfile cfg-file"><span>上傳 QR 圖</span><input type="file" accept="image/*,.heic,.heif" hidden @change="onQrPick" /></label>
    <button v-if="config.qr" class="delbtn" @click="setConfig('qr', '')">清除</button>
  </div>
  <div class="cfg-row">
    <span class="cfg-k">QR 點擊連結</span>
    <input class="authin" :value="config.qr_link || ''" @change="setConfig('qr_link', $event.target.value)" placeholder="選填:點擊 QR 開啟的網址" />
  </div>

  <h4 class="cfg-sub">社群連結 <button class="minibtn" @click="loadCfgEditor">載入目前</button></h4>
  <div v-for="(s, i) in cfgSocial" :key="i" class="cfg-social">
    <span class="cfg-ico" :style="{ background: socialInfo(s.platform).color }"
          v-html="socialSvg(s.platform) || socialInfo(s.platform).icon"></span>
    <select v-model="s.platform">
      <option value="youtube">YouTube</option>
      <option value="telegram">Telegram</option>
      <option value="instagram">Instagram</option>
      <option value="facebook">Facebook</option>
      <option value="line">LINE</option>
      <option value="custom">其他連結</option>
    </select>
    <input class="authin" v-model="s.url" placeholder="https://…" />
    <button class="minibtn del" @click="removeSocial(i)">✕</button>
  </div>
  <div class="ae-addrow">
    <button class="regbtn" @click="addSocial">＋ 新增社群</button>
    <button class="loginbtn" @click="saveSocial">儲存社群</button>
  </div>
  <p class="loginhint">社群會顯示在頁尾(logo 引導跳轉);QR 懸浮在首頁右下角。</p>
</section>
</template>
