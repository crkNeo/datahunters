// 圖片驗證與上傳。註冊的資產證明、文章封面/內文圖、站台 logo/QR 都走這裡,
// 所以放 lib 而不是塞在某個元件裡。
//
// 前端這層只是先擋掉明顯不合法的檔案、少一趟往返;伺服器端有同樣的規則,
// 真正的把關在後端。
import { authFetch } from './api'

const IMG_MAX = 3 * 1024 * 1024 // 3MB
const IMG_EXT = /\.(png|jpe?g|webp|gif|heic|heif)$/i // heic/heif = iPhone 拍的照片

// validImage 回傳 false 時,onError(訊息) 會被呼叫,由呼叫端決定怎麼提示。
export function validImage(f, onError) {
  const fail = (m) => { if (onError) onError(m); return false }
  if (!f) return false
  if (!IMG_EXT.test(f.name)) return fail('僅接受圖片檔(png / jpg / webp / gif / heic)')
  if (f.size > IMG_MAX) return fail('圖片過大,上限 3MB')
  return true
}

// uploadImage 成功回傳伺服器上的路徑,失敗回傳空字串。
export async function uploadImage(file, sub, onError) {
  if (!validImage(file, onError)) return ''
  const fd = new FormData()
  fd.append('file', file)
  fd.append('sub', sub)
  const res = await authFetch('/api/admin/upload', { method: 'POST', body: fd })
  if (!res.ok) {
    if (onError) onError((await res.text()).trim() || '上傳失敗')
    return ''
  }
  return (await res.json()).path
}
