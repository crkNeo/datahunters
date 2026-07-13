# 策略名詞對照表(datahunter / JMCH)

> 最後整理:2026-07-13。這份是**名詞對照參考**,不是實作規格;參數以程式碼為準(`backend/internal/cache/`)。
> 每個策略的權威代號是 **ID**(= DB `book` 欄 + trade-id 前綴 + 多數 `?tab=` 值)。中文名只是顯示用。

## 公開策略(一般使用者可見)

| ID | 中文名 | Tab | 週期 | 進場邏輯 | 分批止盈 | 濾網 | 程式 |
|---|---|---|---|---|---|---|---|
| `main` | **星軌** | `paper` ⚠️ | 即時(雷達) | 分數 ≥55、需新鮮金叉、冷卻 4h(紀律型) | tpMomentum 40/30/30 | — | `paper.go` |
| `gamble` | **超新星** | `gamble` | 即時(雷達) | 分數 ≥50、不要求金叉、冷卻 1h(激進型) | tpMomentum | maxSL 12% | `paper.go` |
| `emaonly` | **銀河** | `emaonly` | 1h + 15m | EMA5/20 交叉 + 15m EMA200 同側(多空) | tpMomentum | — | `paper_ema.go` |

## 管理員策略(admin,觀察 / display-only)

| ID | 中文名 | 週期 | 進場邏輯 | 止盈 | 濾網 | 程式 |
|---|---|---|---|---|---|---|
| `conv` | **均線收斂** | 4H | EMA200 同側橫盤 4 根、VRVP 找止盈、盈虧比 ≥1.5 | tpMomentum | RR ≥1.5 | `convergence.go` |
| `pool` | **掃描池**(30幣) | 1H | EMA50 金叉 EMA200 + 收盤>EMA800(**僅多**)、8×ATR 吊燈停損 | ❌ 無(吊燈移動停損) | top-30 量能 | `scanpool.go` |
| `rsifade` | **逆勢超買空** | 30m | RSI(3)>90 且收盤<EMA200(**僅空**) | tpMeanRev 50/30/20 | maxSL 10% | `microrev.go` |
| `bollfade` | **布林重回** | 1h | 前根收在布林(20,2σ)外、本根收回、與 EMA200 同側 | tpMeanRevFront 60/25/15 | maxSL 10% | `microrev.go` |
| `meanrev` | **乖離回歸** | 1h | 收盤偏離 EMA20 > 2×ATR、與 EMA200 同側 | tpMeanRevFront 60/25/15 | maxSL 10% | `microrev.go` |

## A/B 對照書(admin,複製超新星,各隔離一個變數)

| ID | 中文名 | 相對 `gamble` 的差異 |
|---|---|---|
| `gambleA` | **超新星·A 緊止損** | maxSL 12% → **8%** |
| `gambleB` | **超新星·B 位置閘** | 加 **posGate 0.9**(多單位置>0.9 / 空單<0.1 不追) |

## 分批止盈(multi-TP)兩套預設

引擎:`multitp.go`(`tpPlan` / `setupTP` / `stepTP`,以 `Filled`/`Realized` 記帳,`closeTrade` 混合結算)。
TP1 觸及→止損移保本;TP2 觸及→止損鎖 TP1;TP3=最終止盈。

| 預設 | 用在 | 位置(a/b) | 分批比例(w1/w2/w3) |
|---|---|---|---|
| `tpMomentum`(順勢組) | 星軌 / 超新星 / 銀河 / 均線收斂 | 0.40 / 0.70 | 40 / 30 / 30 |
| `tpMeanRev`(回歸組) | 逆勢超買空 | 0.30 / 0.60 | 50 / 30 / 20 |
| `tpMeanRevFront`(回歸·前置) | 布林重回 / 乖離回歸 | **0.45** / 0.60 | **60 / 25 / 15** |

## 出場結果代碼(`outcomeCN`)

| code | 中文 | 意義 |
|---|---|---|
| `tp3` | TP3 完整 | 三段全達標 |
| `tp2sl` | TP2後出場 | 摸到 TP2,剩餘鎖 TP1 出場 |
| `tp1sl` | TP1後保本 | 摸到 TP1,剩餘保本出場(風控成功,非虧損) |
| `sl` | 止損 SL | 未摸 TP1 直接觸損 |
| `expired` | 逾時平倉 | 超過持倉上限市價結算 |
| `momdead` | 動能衰弱 | admin 手動出場(銀河除外) |
| `reversed` | 反向出場 | 反向訊號平倉 |
| `signal` / `chandelier` / `lock` | (掃描池專用) | 死亡交叉 / 8ATR吊燈 / 早鎖利 |

---

## ⚠️ 已知名詞地雷(2026-07-13 決定暫不改,僅記錄)

1. **「銀河」一詞兼兩義** — `emaonly` 策略顯示名是「銀河」;**但** conv/pool/micro 共用的選幣池(`emaCoins`)註解裡也叫「銀河 coins」。看文件時請自行區分:**大寫「銀河策略」= emaonly;「銀河幣池」= 共用選幣池**。日後若清理,建議把幣池改稱「母池」。
2. **`main` 三代號** — ID=`main`、顯示=`星軌`、Tab=`paper`(歷史遺留,與其他 tab=ID 不一致)。
3. **死名詞殘留** — `bookLabel` 的 `trail`→「移動止損」、`outcomeCN` 的 `hedge`→「套保出場」是已移除的 gamblehedge 遺跡,現已無策略使用。

> 相關:回測結論見記憶 `backtest-conclusions.md`;系統全貌見 [HANDOFF.md](HANDOFF.md)。
