# HANDOFF — datahunter 交接分析(給後續 AI/開發者)

> 2026-07-08 首版,**2026-07-19 大幅更新**(權限層、策略設定層、前端 SPA 化與元件拆分)。
> 本文件是給接手者的完整交接:系統現況、**已用真實數據驗證過的結論(不要重新發明)**、技術債、與建議路線圖。
>
> 另有兩份細節文件:策略規格看 [STRATEGIES.md](STRATEGIES.md),前端架構看
> [frontend/ARCHITECTURE.md](frontend/ARCHITECTURE.md)(含拆檔時踩過的坑)。

---

## 1. 系統速覽

- **後端** Go(`backend/`):單一進程,serve SPA + `/api` + `/uploads`。MySQL 持久化(users / paper_trades / articles / push_subs / site_config / score_events / liquidations)。
- **前端** Vue 3 + Vite。**已從單檔巨石拆分中**:`App.vue`(~3100 行)+ `lib/`(api/upload/format)
  + `router.js`(SPA 路由)+ `components/`(戰場、8 個公開看板、策略表)+ `components/admin/`(6 個後台元件)。
  API 統一走 `lib/api.js`(axios,token 與 401 集中處理)。PWA + Service Worker(`frontend/public/sw.js`)。
- **資料管線**:Binance WS feed(`internal/exchange/wsfeed.go`)常駐 ~36 顆幣、每顆 260 根 1h 已收盤 K 線在記憶體 → 大多數功能**零 REST**。REST 僅作 seed/fallback,曾因 per-coin 輪詢被 418 ban,**任何新功能都應優先吃 WS feed 記憶體資料**。
- **通知**:Telegram(`internal/notify`)+ Web Push VAPID(`internal/push`,金鑰存 site_config)。推播可分群:全體 `PushSend`、依角色 `PushBroadcast`、admin `adminSubs()`。

### 功能地圖(分頁 → 後端)
⚠️ **權限不再寫死在程式裡** —— 下表是「預設值」,實際由後台「標籤權限」決定(見第 1.5 節)。

| 分頁 | 預設權限 | 後端 |
|---|---|---|
| 綜合排行/幣種一覽/財經事件/清算/Upbit公告/市場快訊/資金費率/代幣解鎖/板塊強弱/Robinhood/文章專欄 | 公開 | ranking/events/liquidations/upbit/news/funding/unlock/sectors/robinhood/articles |
| OI儀表板/數據訊號/訊號紀錄/爆發雷達 | member | oi-cache/signals/scorelog/radar |
| 星軌/超新星/銀河/冥王星/支撐壓力 | VIP | paper/gamble/ema-only/conv/sr |
| 布林重回/乖離回歸/布乖v2/布林EMA | admin | admin/bollfade·meanrev·bgv2·bollema |
| 後台/推廣管理 | admin(**鎖定,不可調降**) | admin/* |

首頁另有三個常駐區塊(不分頁):**BTC 多空交戰戰場**(瀏覽器直連交易所 WS)、**整點大盤 AI 分析**、多空推薦卡。

### 策略書(共 8 本;規格詳見 STRATEGIES.md)
- **星軌 main**:門檻55、requireCross。實測 **+45%** — 最強的一本。
- **超新星 gamble**:門檻**50**(原45,依實測改)、追高型、1h冷卻、`maxSLPct=12`。
- **銀河 emaonly**:1h EMA5/20 交叉 + 15m EMA200 側;admin 可手動出場(記為「逾時」)。
- **冥王星 conv**:4H 動態ATR均線收斂,VRVP 找止盈,RR≥1.5。
- **均值回歸組**(admin):布林重回 1h / 乖離回歸 1h。
- **布乖v2**(admin):1h乖離 + 4h布林 雙腿家族,同幣互斥,只做空,單段止盈。
- **布林EMA**(admin):4H 突破蓄勢,1:3 RR,單段止盈 + 保本位「純通知」。
- ~~超新星·保本~~、~~gambleA/gambleB~~、~~30幣掃描池~~、~~逆勢超買空 rsifade~~:**均已移除**。
- Bitunix 實盤鏡像(`trader.go`):**只有開倉**,預設關閉(`BITUNIX_AUTOTRADE=1` 才啟用)。

### 1.5 兩個「後台可調」的設定層(2026-07-19 新增)
這是接手時最該先理解的部分 —— **很多以前要改程式的東西,現在改設定就好**:

1. **標籤權限**(`internal/cache/tabperm.go`,site_config `tab_perms`)
   每個分頁的最低身分可調。**後端一起擋**(路由走 `gateTab()`),不是只把前端分頁藏起來。
   護欄:後台/推廣管理鎖死;**未知分頁 fail closed 退回 admin**。
2. **策略設定**(`internal/cache/strategy_ctl.go`,site_config `strat_cfg`)
   每個策略可調:類型標籤、風控警語、最大止損%、**出場模式(分批/保本/單段三選一)**、
   分段位置與比例、保本觸發點、保本位提示、四種通知開關。附「恢復預設」。
   **預設值鏡射程式碼**,且有測試 `TestDefaultsReproducePresetPlans` 保證沒動過就行為不變。

> ⚠️ 改 `NewStore` 的 book 參數或 `multitp.go` 的 preset 時,**必須同步 `stratDefaults`**,否則測試會紅。

---

## 2. ⚠️ 已用真實數據驗證過的結論(改動前必讀)

以下都是用**真實成交 CSV + 補抓 K 線重播**驗證過的,不是猜測:

1. **gamble 門檻 45→50 的依據**:實測分數桶 45–49 淨虧 −47.9%、50–59 +42.6%、60+ −12.5%。**不是越高越好**,是「別低於50」。
2. **保本停損(移到保本)會剪掉肥尾**:對 gamble 實測,啟動點 1/3 → 總報酬 +35.6% 變 **−37.5%**;1/2 → −26.3%;2/3 → +0.7%。**全部輸給不加**。因此保本只放在 admin A/B 書觀察,**不要**推廣到公開書,除非 forward 數據翻案。
3. **SL 距離過濾**:FILTER@10% → +50.1%、**FILTER@12% → +56.2%(最佳)**、FILTER@15% → +42.9%(太鬆)。CLAMP(夾緊留單)@15% → +46.9%。目前只套在 gamblehedge(12%);**若 forward 驗證持續有效,值得推廣到 gamble 本體**。
4. **進場位置呈駝峰**(模擬代理觸發):pos(12h區間位置)≤0.5 = 接刀(−0.176R)、>0.9 = 追頂(−0.035R)、**0.5–0.9 是甜蜜點**。此 gate **尚未實作**——是現成的下一步優化候選。
5. **Coinbase Premium**:一年 1h 回測,相關性僅 0.02(1h)~0.11(24h),呈駝峰非線性;只適合當 **12–24h BTC 大盤過濾器**(深度負溢價偏空最穩),**不適合逐幣 1h 訊號**。用戶已決定不接。
6. **銀河實測 −14.3%**(勝率 56.8% 但 1:1 RR 下平均 −0.32%):**尚未深挖**,值得做一次同樣的重播分析(SL 距離分布/時段/幣種)。
7. 所有回測樣本都只有 **~10–41 天**,結論方向可信、數字別當精確值;**避免對同一段行情反覆調參(過擬合)**。

---

## 3. 高優先技術債 / 風險

1. **`App.vue` 仍偏大(~3100 行)**:已從 4760 行拆掉三分之一(戰場、8 個公開看板、6 個後台元件、
   策略表、api/format/upload lib、router)。**未拆**:雷達三本的策略表(多了時間篩選與 CSV 匯出,
   且是使用者實際下單的主畫面,刻意不合併)、排行/幣種一覽/雷達/訊號、文章編輯器、推廣管理、幣種詳情抽屜。
   拆檔的模式與踩過的坑寫在 `frontend/ARCHITECTURE.md`,**動手前先看**。
2. **⚠️ 前端零測試**:vite build 過**不代表能跑** —— 模板呼叫了元件沒定義的函式,build 不會報錯,
   執行時才整塊空白。拆完一定要真的開那一頁看,而且**要用未登入的乾淨分頁看一次公開頁**
   (曾因 `loadAll()` 有一行 `if (!authed) return`,導致公開訪客所有資料都載不到,用 admin 測完全看不出來)。
2. **記憶體狀態重啟即失**:Upbit 翻譯快取、GDELT news feed/去重、SR 關卡與突破狀態、`etfSeen`、套保 Hedged 旗標(註:保本停損價寫進 `tr.SL` 有持久化,保護還在;但 Hedged 旗標重啟後會重觸發一次推播)。影響=重啟後短暫重複推播/空白板,可接受但可優化(建議:一張 kv 快取表)。
3. **後端測試只涵蓋設定層**:`strategy_ctl_test.go` / `tabperm_test.go`(13 個測試,守著權限預設、
   出場模式互斥、分批比例正規化、切換模式不清空設定、保本觸發、鎖定分頁不可降級)。
   **策略數學仍零測試**(swingLows/cluster/entryLevels/EMA/ATR),建議優先補。
4. **外部脆弱依賴**:
   - Google 免金鑰翻譯端點(Upbit 韓→繁、GDELT 英→繁):可能被限流/停,壞了會退回原文(已容錯),但要留意。
   - **Farside ETF 爬取**(`internal/etf`):HTML 改版就失效(已容錯,壞了只是不更新)。
   - GDELT:限流 1req/5s,已用 5 分鐘輪詢,安全。
5. **iOS PWA 白屏殘餘風險**:已做 loadMe 容錯/分頁守門/visibilitychange/errorHandler 四層;唯 iOS 系統級殺進程無解,若再發生考慮更積極的 reload 或 app-shell 快取。
6. **推播量**:目前支撐壓力 + 全分類快訊(含加密)都**推全體且無節流**——用戶要求的,但要留意用戶關通知率;第一個收斂旋鈕是「加密只顯示不推」。
7. **安全**:
   - `/api/config` 曾洩漏 VAPID 私鑰,已改白名單(`publicConfigKeys`)。**建議提醒用戶到後台按一次「重置推播金鑰」**(舊私鑰可能已外流)——不確定是否已執行。
   - JWT 10 年效期(設計如此,靠 live DB gate 即時封禁);輪換 `JWT_SECRET` = 全站強制登出。
   - 密碼規則 4–16 字偏短,bcrypt 有;登入/註冊有 rate limit。

---

## 4. 資訊面補強建議(依 CP 值排序)

1. **😱 恐懼貪婪指數**(alternative.me,免 key、一個 GET):做成首頁情緒條或 risk gate。**已與用戶討論過、用戶有興趣,是下一個最該做的**。
2. **穩定幣供給變化**(USDT/USDC 市值增發,CoinGecko/DeFiLlama 免費):流動性先行指標,適合併入大盤 risk strip。
3. **聚合 OI**(Binance + Bybit + OKX 公開端點加總):升級現有 Binance-only OI,對評分系統直接有感。
4. **頂級交易員多空比**(Binance `topLongShortPositionRatio`,免費,同 futures/data 家族):散戶 vs 大戶背離是好反指。
5. **代幣解鎖日曆**:大額解鎖=山寨拋壓;無乾淨免費 API,需評估爬取成本。
6. **快訊品質**:目前 GDELT 分類只比對標題關鍵字,可補同義詞、或對「figure」類加白名單網域降噪;X/Twitter 即時發言無免費管道(API 已付費化),若要秒級可評估免費 Telegram 快訊頻道(Tree of Alpha 等)作中繼。

## 5. 策略面下一步(按證據強度)

0. **⚠️ 尚未驗證的新功能**(2026-07-19 上線,測試庫是空的,沒跑過真實交易):
   保本模式(`applyBreakeven`)、策略設定生效(最大止損%/出場模式/分批比例)、四種通知開關、
   手動出場與清空策略。**上線後先觀察一輪**,確認新單的止盈止損位置符合後台設定。
1. ~~forward 驗證 gamblehedge~~:已移除;`maxSLPct=12` 已直接進 gamble 本體。
2. **pos∈[0.5,0.9] 進場 gate**:模擬顯示期望值 ~3 倍,**尚未實作**(gambleB 觀察書已移除,
   要做的話建議做成 `StratCfg` 的一個欄位,跟其他設定一起後台可調)。
3. **銀河診斷**:重播分析找出 −14.3% 的結構性原因(SL 定義?1:1 RR?時段?)。**仍未做**。
4. **風險化倉位**:各書 SL 距離不一(3%~15%+)但 UI 建議固定槓桿——建議每單顯示「依 SL 距離反推的建議倉位/槓桿」,把停損變真的。
5. 統計欄位考慮改以 **R 為單位**(像支撐壓力頁那樣),讓績效反映策略本身而非槓桿。

## 6. 營運提醒

- 部署 = build frontend(`npm run build`)+ 重啟 Go(無 CI/CD、無 migration 工具;schema 改動靠啟動時 `CREATE TABLE IF NOT EXISTS`+手動)。
- **`frontend/dist/` 有進版控**(Go 的 `withStatic` 直接服務它),所以 build 產物要一起提交。
- **`frontend/node_modules/` 也在版控裡(862 檔,誤入)** —— 建議 `git rm -r --cached frontend/node_modules`
  清掉(`frontend/.gitignore` 已備好),但會產生上千檔的 diff,挑時機做。
- **本機開發**:`backend/.env`(已 gitignore)可設本機 DB + `ADMIN_USER/ADMIN_PASS` 自動種管理員;
  前端用 `VITE_API_TARGET` 覆蓋 proxy 目標(預設 8080)。**絕不要把正式站 DSN 寫進去。**
- 無結構化日誌/監控;出問題只能看 stdout。可考慮加 `/healthz` 擴充(feed 健康度、DB 連線、各 ticker 心跳)。
- 記得檢查 `.env`(JWT_SECRET、DB、TG、DOMAINS);`JWT_SECRET` 未設會隨機生成 → 重啟全登出。

## 7. 用戶偏好(接手時遵守)

- 回覆用**繁體中文**;直接、有主見、先給結論。
- **改策略前先用數據驗證**(用 `/api/admin/export?book=` 匯出真實成交回測),用戶明確欣賞這個流程。
- UI 文案:**不要出現「免費」「來源」字樣**(已全站清除);策略小氣泡文案是用戶親自定稿,別改。
- 新功能一律先評估 **API 壓力**(418 ban 陰影),優先用 WS feed 記憶體資料。
- 推播:目前方針是「有事就推、推全體」,admin 專屬功能除外。
