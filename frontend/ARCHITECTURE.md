# 前端架構(JMCH)

Vue 3 + Vite,`npm run build` 打包到 `dist/`,由 Go 後端的 `withStatic` 一併服務。

## 目錄

```
src/
  main.js              進入點:掛 router + 全域錯誤處理(白畫面自動重載一次)
  router.js            SPA 路由:網址 ↔ 分頁,含舊版 /?tab= 相容轉址
  lib/
    api.js             axios 實例 + token + fetch 相容層(authFetch)
  lib/
    upload.js          圖片驗證 + 上傳(註冊證明 / 文章圖 / logo / QR 共用)
    format.js          顯示格式化(fmtPct / fmtPrice / fmtNum / fundClock)
  components/
    BattleField.vue           首頁「BTC 多空交戰」戰場(自給自足)
    SectorBoard.vue           板塊強弱 / 輪動(自己抓 + 5 分輪詢)
    FundingBoard.vue          資金費率(自己抓 + 1 分輪詢)
    UnlockBoard.vue           代幣解鎖(自己抓 + 10 分輪詢)
    LiquidationBoard.vue      清算(自己抓 + 30 秒輪詢)
    EventsBoard.vue           財經事件(資料由外層傳入,導覽列徽章要用)
    UpbitBoard.vue            Upbit 公告(同上)
    NewsBoard.vue             市場快訊(同上,分類篩選是內部狀態)
    RobinhoodBoard.vue        Robinhood 上架(同上)
    admin/
      TabPermissions.vue      後台:標籤權限
      StrategySettings.vue    後台:策略設定
      SiteSettings.vue        後台:站台設定(logo / 社群 / QR)
      LoginNotice.vue         後台:登入公告彈窗
      PushBroadcast.vue       後台:即時推播
      UserManagement.vue      後台:使用者(待審核 / 名單 / 手動新增)
  App.vue              其餘畫面(拆分進行中)
```

## 後台

後台分成六個功能標籤(`adminTab`):使用者 / 標籤權限 / 策略設定 / 站台設定 / 登入公告 / 即時推播。

已抽成元件的兩個(標籤權限、策略設定)採同一個模式:

- **自己載自己的資料**(`onMounted` 呼叫自己的 load),不從 App.vue 拿 props
- 用 `emit('msg', ...)` 回報操作結果給外層顯示
- 用 `emit('changed')` 通知外層重抓公開資料(改了標籤權限要更新導覽列;改了策略設定要更新各策略頁的類型標籤)

其餘四個仍是 App.vue 內的 `<template v-else-if>` 區塊,之後照同一模式抽出。

## API 層(`lib/api.js`)

所有後端呼叫都走這裡,token 與 401 只在一個地方處理。

- `token` / `setToken()` —— 登入憑證,與 localStorage 同步
- `api` —— axios 實例,request interceptor 自動掛 `Authorization`
- `authFetch(url, opts)` —— **fetch 相容層**

### 為什麼還留著 authFetch
全站有 60+ 個呼叫點是照 fetch 介面寫的(`res.ok` / `res.json()` / `res.text()` / `res.blob()`)。
一次全部改寫成 axios 風格風險太高,所以用 axios 實作出相容層:呼叫點不用動,就先享受到
統一的 token 注入與錯誤處理。之後拆元件時再逐檔改成 `api.get/api.post`。

實作細節:用 `responseType: 'arraybuffer'` 收回應,`json()`/`text()`/`blob()` 都從同一份資料衍生,
不必事先知道呼叫端要哪一種;`validateStatus: () => true` 讓非 2xx 也回傳而不丟例外,保住 `res.ok` 語意。
FormData 上傳直接交給 axios(它會自己設 multipart boundary)—— 已實測可用。

## 路由(`router.js`)

刻意「只管網址、不管畫面」:內容仍由 App.vue 依 `mainTab` 渲染,router 負責把 `mainTab`
同步成真實網址。這樣 27 個 `mainTab = 'x'` 的呼叫點完全不用改,就換到了:

- 可分享的連結(`/conv`、`/gamble`…)
- 瀏覽器上一頁 / 下一頁
- 重新整理能回到同一頁(後端 `withStatic` 已有 SPA fallback)

雙向同步寫在 App.vue,用一個 `syncing` 旗標避免兩個 watch 互相觸發成迴圈。

### ⚠️ 舊版推播連結相容
後端推播的連結是寫死的 `"/?tab=" + bookTab(name)`(見 `paper.go`)。**已經發出去的通知不能失效**,
所以 `router.beforeEach` 會把 `/?tab=conv` 轉成 `/conv`。
→ 若日後要改成路徑式,**後端與前端必須同時改**,且舊通知會失效。目前刻意不動。

### 權限
分頁的最低身分由後端 `/api/tab-perms` 決定(後台可調)。App.vue 的 `canTab()` 判斷是否顯示,
身分或設定變動時,若停留在看不到的分頁會自動退回公開首頁 —— 否則內容區會落在一個都不成立的
`v-else-if` 分支上、整片空白。

**前端這層只管畫面。真正的權限在後端 `gateTab()`。**

## 拆分進度

| 狀態 | 項目 |
|---|---|
| ✅ | `lib/api.js`(axios 層) |
| ✅ | `router.js`(SPA 路由) |
| ✅ | `components/BattleField.vue` |
| ✅ | 後台拆成六個功能標籤 |
| ✅ | **後台六個元件全部抽出**(使用者 / 標籤權限 / 策略設定 / 站台設定 / 登入公告 / 即時推播) |
| ✅ | `lib/upload.js`、`lib/format.js`(共用) |
| ✅ | **八個公開看板全部抽出**(板塊 / 資金費率 / 代幣解鎖 / 清算 / 財經事件 / Upbit / 快訊 / Robinhood) |
| ✅ | `components/StrategyBook.vue` —— 冥王星 + 微策略五本共用同一份表格 |
| ⏸ | 雷達三本(星軌 / 超新星 / 銀河)→ **刻意未併入**,理由見下 |

### 策略表怎麼合併的
`StrategyBook.vue` 涵蓋 **冥王星 + 微策略五本**(逆勢/布林/乖離/布乖v2/布林EMA)——
這六頁的表格原本是逐字重複的兩份。差異用 props 表達,而不是在元件裡判斷是哪個策略:

| prop | 用途 |
|---|---|
| `statsOrder` | 統計列順序(冥王星是「策略類型」開頭,微策略是「累計損益」開頭) |
| `canExit` | 手動出場欄(冥王星限管理員,微策略一律顯示) |
| `emptyText` | 無單時的提示(各策略進場條件不同) |

各頁自己的標題/說明/清除按鈕仍留在 App.vue —— 那部分差異太大,共用只會更難讀。

**雷達三本(星軌/超新星/銀河)沒有併進來**:它多了時間範圍篩選與 CSV 匯出,統計列欄位
也不同,硬合會變成一堆 flag。那是使用者實際下單的主畫面,維持原狀比較划算。

### 拆元件時別忘了輪詢
原本很多看板是靠 App.vue 的 15 秒 `loadAll()` 帶著刷新。搬進元件後那條線就斷了,
元件要自己 `setInterval`(例如 SectorBoard 每 5 分鐘回抓一次),並在 `onUnmounted` 清掉。

### 資料歸屬原則
元件盡量「自己載自己的資料」,但**外層也要用的資料留在 App.vue 用 props 傳**:

- `UserManagement` 的 `users` —— 導覽列徽章與 15 秒輪詢也要用
- `SiteSettings` 的 `config`/`socialLinks` —— 前台頁尾與首頁 QR 也要用
- `LoginNotice` 的 `notice` —— 前台的登入公告彈窗也要用

這類元件改完資料後 `emit('reload')` / `emit('saved')`,由外層重新抓。

## ⚠️ 踩過的坑(拆檔時注意)

**權限守衛不能在登入狀態解析完成前執行。** 深連 `/admin` 時,route 會先把 `mainTab` 設成
`admin`,但那一刻 `role` 還是 `public`。若守衛此時就把分頁踢回首頁,連帶會把網址改成 `/`,
route 參數消失 —— 等 `loadMe()` 回來變成 admin 也救不回去,使用者就莫名其妙停在首頁。
解法是守衛加上 `if (!authReady.value) return`。

**用 sed 批次改寫程式碼要小心行尾。** 把 `adminMsg.value = X` 轉成 `emit('msg', X)` 時,
正則 `\(.*\)$` 會把行尾的 `}` 一起吃進去,產生 `emit('msg', ... })` 這種括號錯位。
每拆一塊就 build 一次才抓得到。

**⚠️ build 過不代表能跑。** 模板裡呼叫了元件沒有定義的函式(例如 `upbitTime`、`openDetail`),
**vite build 不會報錯**,是執行時才拋 render error、整個區塊變空白。拆完一定要真的打開那一頁看,
不能只看 build 綠燈。

抽元件前先把模板裡所有的函式呼叫列出來,逐一確認在新檔案裡解析得到:

```bash
sed -n '/<template>/,/<\/template>/p' X.vue | grep -oE '[a-zA-Z_][a-zA-Z0-9_]*\(' | sort -u
```

(這次就是靠這招才發現 `upbitTime` 漏掉;而更早我用「固定清單」去比對,結果漏掉了不在清單上的函式。)

**只用管理員帳號測會漏掉公開訪客的問題。** `loadAll()` 裡曾經有一行
`if (!authed.value) return`(全站鎖在登入牆時代的寫法)。開放公開瀏覽後,那行讓
**未登入訪客什麼都載不到** —— 排行/快訊/Upbit/財經事件/Robinhood/文章全部空白,
畫面還停在「載入排行榜中…」看起來像正在載入。用 admin token 測完全看不出來。
→ 每次改完至少要用**未登入的乾淨分頁**看一次公開頁。

**別用 `sed -i` 搭配 `-n`。** `sed -i -n '750,762p' f` 會把整個檔案改寫成只剩那幾行 —— 這次就這樣
把 App.vue 從 3467 行毀成 13 行。動大範圍前先 `cp` 一份備份。

拆檔原則:**一次一塊、每塊拆完立刻驗證**。公開頁可用瀏覽器實測;會員/VIP/後台需要本機測試庫
(見 `backend/.env`,`devadmin`)才能登入驗證。
