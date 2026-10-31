# Kronos × 脈衝星v12 離線驗證

目的:在**不動線上系統**的前提下,先確認 [Kronos](https://github.com/shiyu-coder/Kronos)(K 線預測模型)
能不能分辨 v12 的好單 / 壞單。過關才考慮接進系統,而且接進去也先只記錄、不擋單。

## 流程

### 1. 準備環境(在你自己的電腦或伺服器,需要 Python 3.10 以上)

以下指令都在專案的 `backtest/kronos/` 資料夾裡執行:

```bash
cd backtest/kronos
git clone https://github.com/shiyu-coder/Kronos.git
cd Kronos
pip install -r requirements.txt
cd ..
```

第一次跑會自動從 HuggingFace 下載模型(Kronos-small 約 100MB)。

### 2. 匯出 v12 的資料

網站 → 脈衝星v12 頁 → 右上「⬇ 匯出 CSV」,例如存成 `pulsarv12.csv`。
(只會用到已平倉的做多單。)

### 3. 先試跑 50 筆,確認能動、估時間

```bash
python kronos_eval.py predict --csv pulsarv12.csv --kronos-dir ./Kronos --out kronos_v12.csv --max-trades 50
```

最後一行會顯示每筆花多久。2,000 多筆用一般 CPU 大概要幾個小時;
有 NVIDIA 顯卡加 `--device cuda:0`,Mac M 系列可以試 `--device mps`。

### 4. 跑全部

同一個指令拿掉 `--max-trades`。**可以隨時中斷**,再跑一次會從上次停的地方接著做
(已經做過的單會跳過,K 線也存在 `kline_cache/`,不會重抓)。

```bash
python kronos_eval.py predict --csv pulsarv12.csv --kronos-dir ./Kronos --out kronos_v12.csv
```

### 5. 看結果

```bash
python kronos_eval.py report --pred kronos_v12.csv
```

把整段輸出貼回來討論。

## 它做了什麼

每一筆單:

1. 抓**進場那根 15m 之前**的 400 根已收 K(幣安永續,沒有就用現貨)——不看任何進場後的資料
2. Kronos 往後預測 33 根(涵蓋進場後 8 小時),同一筆產生 10 條可能的走勢
3. 算出(都是相對進場價):

| 欄位 | 意思 |
|---|---|
| `k_up1` / `k_up4` / `k_up8` | 10 條走勢裡,1H / 4H / 8H 後比進場價高的比例(看漲機率) |
| `k_ret1` / `k_ret4` / `k_ret8` | 1H / 4H / 8H 後漲跌% 的中位數 |
| `k_gain8` | 8 小時內最高點漲幅% 的中位數(對照實際的「最大獲利」) |
| `k_dd8` | 8 小時內最低點跌幅% 的中位數(會不會先掃止損) |

## 報告怎麼看

1. **相關性**:預測值越高,實際損益 / 1H~8H / 最大獲利是不是越好。絕對值 > 0.05 才算有點東西。
2. **分 5 組**:Q1 = Kronos 最不看好的 20%,Q5 = 最看好的 20%。重點看:
   - Q5 的平均損益有沒有明顯比 Q1 好
   - Q1 的「進場即最高」(進場後從沒漲超過 +1.8%)比例有沒有特別高 —— 這是 v12 最想濾掉的那群單
3. **前半 / 後半**:兩段各自分組,Q5−Q1 要同方向才算數。

**過關標準**:前後兩段都同方向、差距都 ≥ 0.2%,而且不是只有零星一個指標過關。
沒過就停在這一步,不用接進系統。

## 注意

- Kronos 在 2025-08 開源,v12 的單都在那之後,模型訓練時沒看過這段行情,沒有偷看答案的問題。
- 盤中進場那根(還沒收完)沒有給模型,只用進場前已收的 K;實際上線時可以把那根的部分 K 也給它,
  理論上會更準一點,所以這裡的結果偏保守。
- 匯出的 `pnl_pct` 是頁面上的階梯口徑(會高估);報告同時列出 1H~8H 回填和最大獲利,這兩個跟出場規則無關,比較乾淨。
- 預測有隨機性(每次抽的走勢不同),固定了 `--seed`,同一台機器重跑結果一樣。
