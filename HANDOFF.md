# HANDOFF — datahunter 交接分析(給後續 AI/開發者)

> 2026-07-08 整理。本文件是給接手者的完整交接:系統現況、**已用真實數據驗證過的結論(不要重新發明)**、技術債、與建議路線圖。

---

## 1. 系統速覽

- **後端** Go(`backend/`):單一進程,serve SPA + `/api` + `/uploads`。MySQL 持久化(users / paper_trades / articles / push_subs / site_config / score_events / liquidations)。
- **前端** Vue 3(`frontend/src/App.vue`,**單檔 ~2600 行**)。PWA + Service Worker(`frontend/public/sw.js`)。
- **資料管線**:Binance WS feed(`internal/exchange/wsfeed.go`)常駐 ~36 顆幣、每顆 260 根 1h 已收盤 K 線在記憶體 → 大多數功能**零 REST**。REST 僅作 seed/fallback,曾因 per-coin 輪詢被 418 ban,**任何新功能都應優先吃 WS feed 記憶體資料**。
- **通知**:Telegram(`internal/notify`)+ Web Push VAPID(`internal/push`,金鑰存 site_config)。推播可分群:全體 `PushSend`、依角色 `PushBroadcast`、admin `adminSubs()`。

### 功能地圖(分頁 → 後端)
| 分頁 | 權限 | 後端 |
|---|---|---|
| 綜合排行/幣種一覽/財經事件/清算/Upbit公告/市場快訊/文章專欄 | 公開 | ranking/home/events/liquidations/upbit/news/articles |
| OI儀表板/數據訊號/訊號紀錄/爆發雷達 | member | oi-cache/signals/scorelog/radar |
| 星軌(main)/超新星(gamble)/銀河(emaonly)/支撐壓力(sr) | VIP | paper/gamble/ema-only/sr |
| 後台/超新星·保本(gamblehedge) | admin | admin/*、gamble-hedge |

### 策略書(paper books,`internal/cache/paper.go`)
- **星軌 main**:門檻55、requireCross(等回檔突破)。實測 **+45%** — 最強的一本。
- **超新星 gamble**:門檻**50**(原45,依實測改)、追高型、1h冷卻。
- **銀河 emaonly**:1h EMA5/20 金叉/死叉 + EMA50 側,SL=20根極值、TP=1:1;每小時收盤評估一次(`paper_ema.go` refreshEMA);admin 可手動出場(記為「逾時」)。
- **超新星·保本 gamblehedge**:admin 專屬 A/B — gamble 進場 + `maxSLPct=12`(SL>12% 不進)+ 保本停損(獲利達 TP 1/3 → SL 上移進場±0.05%,TP 不變,觸發推播 admin)。開/平倉靜音(`adminOnly`),避免與公開超新星重複推播。
- Bitunix 實盤鏡像(`trader.go`):**只有開倉**,靠交易所端 TP/SL 出場;Phase 2(平倉/查持倉/減倉)未做。

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

1. **`App.vue` 巨石**(~2600行):所有頁面、狀態、樣式在一個檔。加功能成本越來越高,建議拆 components + composables(純重構、不改行為)。
2. **記憶體狀態重啟即失**:Upbit 翻譯快取、GDELT news feed/去重、SR 關卡與突破狀態、`etfSeen`、套保 Hedged 旗標(註:保本停損價寫進 `tr.SL` 有持久化,保護還在;但 Hedged 旗標重啟後會重觸發一次推播)。影響=重啟後短暫重複推播/空白板,可接受但可優化(建議:一張 kv 快取表)。
3. **無自動化測試**:策略數學(swingLows/cluster/entryLevels/EMA)完全靠手測。建議至少為 `internal/cache/support.go`、`paper_ema.go`、`radar.go entryLevels` 補單元測試。
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

1. **forward 驗證 gamblehedge**(保本+FILTER@12%)vs gamble:兩本並行就是為了這個,累積 2–4 週後匯出 CSV 對比,決定 12% 濾網要不要進公開書。
2. **pos∈[0.5,0.9] 進場 gate**:模擬顯示期望值 ~3 倍,尚未實作;建議做成 per-book 可開關參數,先掛 gamblehedge 觀察。
3. **銀河診斷**:重播分析找出 −14.3% 的結構性原因(SL 定義?1:1 RR?時段?)。
4. **風險化倉位**:各書 SL 距離不一(3%~15%+)但 UI 建議固定槓桿——建議每單顯示「依 SL 距離反推的建議倉位/槓桿」,把停損變真的。
5. 統計欄位考慮改以 **R 為單位**(像支撐壓力頁那樣),讓績效反映策略本身而非槓桿。

## 6. 營運提醒

- 部署 = build frontend(`npm run build`)+ 重啟 Go(無 CI/CD、無 migration 工具;schema 改動靠啟動時 `CREATE TABLE IF NOT EXISTS`+手動)。
- 無結構化日誌/監控;出問題只能看 stdout。可考慮加 `/healthz` 擴充(feed 健康度、DB 連線、各 ticker 心跳)。
- 記得檢查 `.env`(JWT_SECRET、DB、TG、DOMAINS);`JWT_SECRET` 未設會隨機生成 → 重啟全登出。

## 7. 用戶偏好(接手時遵守)

- 回覆用**繁體中文**;直接、有主見、先給結論。
- **改策略前先用數據驗證**(用 `/api/admin/export?book=` 匯出真實成交回測),用戶明確欣賞這個流程。
- UI 文案:**不要出現「免費」「來源」字樣**(已全站清除);策略小氣泡文案是用戶親自定稿,別改。
- 新功能一律先評估 **API 壓力**(418 ban 陰影),優先用 WS feed 記憶體資料。
- 推播:目前方針是「有事就推、推全體」,admin 專屬功能除外。
