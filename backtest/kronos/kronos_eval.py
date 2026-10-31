#!/usr/bin/env python3
"""
kronos_eval.py — 離線驗證:Kronos 的預測能不能分辨脈衝星v12 的好單 / 壞單?

不動線上系統。讀後台「⬇ 匯出 CSV」下載的 v12 檔,對每一筆已平倉的單:
  1. 抓「進場那根 15m 之前」的 400 根已收 15m K(幣安永續,抓不到用現貨),絕不看進場後的資料
  2. 丟給 Kronos 往後預測 33 根(涵蓋進場後 8 小時),同一筆產生 N 條可能的走勢
  3. 算出幾個數字(相對進場價):
       k_up1 / k_up4 / k_up8   N 條走勢裡,1H / 4H / 8H 後比進場價高的比例(看漲機率)
       k_ret1 / k_ret4 / k_ret8 1H / 4H / 8H 後漲跌% 的中位數
       k_gain8                 8 小時內最高價漲幅% 的中位數(對照實際的 max_gain)
       k_dd8                   8 小時內最低價跌幅% 的中位數(會不會先掃止損)
  4. 寫進輸出 CSV(原本欄位 + 上面的欄位),可中斷、重跑會接著做

接著用 report 看:這些數字分高低組之後,實際損益 / 1H~8H 回填 / 最大獲利有沒有差,
而且前半期、後半期要同方向才算數。

用法(詳見同資料夾 README.md):
    python kronos_eval.py predict --csv pulsarv12.csv --kronos-dir ../../../Kronos --out kronos_v12.csv
    python kronos_eval.py report  --pred kronos_v12.csv

注意:Kronos 在 2025-08 開源,v12 的單都在那之後,模型訓練時不可能看過這段行情,沒有偷看答案的問題。
"""

import argparse
import csv
import json
import math
import os
import statistics
import sys
import time
import urllib.parse
import urllib.request

BAR = 15 * 60 * 1000  # 15m(毫秒)
HOURS = (1, 4, 8)
FEATS = ["k_up1", "k_up4", "k_up8", "k_ret1", "k_ret4", "k_ret8", "k_gain8", "k_dd8"]


# ----------------------------- K 線(含本機快取) -----------------------------

def _get(url):
    with urllib.request.urlopen(url, timeout=15) as r:
        return json.loads(r.read())


def fetch_range(coin, start_ms, end_ms):
    """幣安 15m K,[start_ms, end_ms],舊→新。永續抓不到(沒上永續)就用現貨。"""
    for base, path in (("https://fapi.binance.com", "/fapi/v1/klines"), ("https://api.binance.com", "/api/v3/klines")):
        out, cur, ok = [], start_ms, True
        while cur <= end_ms:
            q = urllib.parse.urlencode({"symbol": coin + "USDT", "interval": "15m",
                                        "startTime": cur, "endTime": end_ms, "limit": 1500})
            try:
                rows = _get(f"{base}{path}?{q}")
            except Exception as e:  # 400 = 沒有這個交易對 → 換現貨
                ok = False
                print(f"  {coin} {base} 抓 K 線失敗:{e}")
                break
            if not rows:
                break
            for k in rows:
                out.append({"ts": int(k[0]), "open": float(k[1]), "high": float(k[2]), "low": float(k[3]),
                            "close": float(k[4]), "volume": float(k[5]), "amount": float(k[7])})
            cur = int(rows[-1][0]) + BAR
            time.sleep(0.15)  # 別打太快
        if ok and out:
            return out
    return []


def load_klines(coin, start_ms, end_ms, cache_dir):
    """先看快取有沒有涵蓋這段;沒有就整段重抓一次存起來(同一個幣只抓一次)。"""
    os.makedirs(cache_dir, exist_ok=True)
    path = os.path.join(cache_dir, f"{coin}_15m.json")
    if os.path.exists(path):
        with open(path) as f:
            ks = json.load(f)
        if ks and ks[0]["ts"] <= start_ms and ks[-1]["ts"] >= end_ms - BAR:
            return ks
    ks = fetch_range(coin, start_ms, end_ms)
    if ks:
        with open(path, "w") as f:
            json.dump(ks, f)
    return ks


# ----------------------------- 讀匯出的 CSV -----------------------------

def parse_ms(s):
    from datetime import datetime
    return int(datetime.fromisoformat(s.replace("Z", "+00:00")).timestamp() * 1000)


def read_trades(path):
    with open(path, encoding="utf-8-sig", newline="") as f:
        rows = list(csv.DictReader(f))
    cols = list(rows[0].keys()) if rows else []  # 先存下來:下面會在 row 裡加內部用的 _open_ms
    out = []
    for r in rows:
        try:
            if r.get("status") != "closed" or r.get("dir") != "long" or float(r.get("entry") or 0) <= 0:
                continue
            r["_open_ms"] = parse_ms(r["open_time"])
        except Exception:
            continue
        out.append(r)
    out.sort(key=lambda r: r["_open_ms"])
    return cols, out


def even_sample(trades, n):
    """依時間平均抽 n 筆(保留整段期間的分布),n<=0 = 全部。"""
    if n <= 0 or n >= len(trades):
        return trades
    step = len(trades) / n
    return [trades[int(i * step)] for i in range(n)]


# ----------------------------- predict -----------------------------

def path_feats(paths, entry, open_ms, bar_start):
    """paths:N 個 Kronos 輸出的 DataFrame(第 0 根 = 進場那根)。回傳各指標(相對進場價 %)。"""
    def idx(h):
        return int((open_ms + h * 3600_000 - bar_start) // BAR)
    i8 = idx(8)
    rets = {h: [] for h in HOURS}
    gains, dds = [], []
    for p in paths:
        o, hi, lo = p["open"].values, p["high"].values, p["low"].values
        for h in HOURS:
            rets[h].append((o[idx(h)] - entry) / entry * 100)  # 那個時間點所在那根的開盤 ≈ 當下價
        gains.append((max(hi[: i8 + 1]) - entry) / entry * 100)
        dds.append((min(lo[: i8 + 1]) - entry) / entry * 100)
    f = {}
    for h in HOURS:
        f[f"k_up{h}"] = sum(1 for v in rets[h] if v > 0) / len(rets[h])
        f[f"k_ret{h}"] = statistics.median(rets[h])
    f["k_gain8"] = statistics.median(gains)
    f["k_dd8"] = statistics.median(dds)
    return {k: round(v, 4) for k, v in f.items()}


def cmd_predict(a):
    import pandas as pd
    sys.path.insert(0, os.path.abspath(a.kronos_dir))
    try:
        from model import Kronos, KronosTokenizer, KronosPredictor
    except ImportError as e:
        sys.exit(f"載入 Kronos 失敗:{e}\n請確認 --kronos-dir 指到 git clone 下來的 Kronos 資料夾,且已 pip install -r requirements.txt")
    import torch
    torch.manual_seed(a.seed)

    cols, trades = read_trades(a.csv)
    if not trades:
        sys.exit("CSV 裡沒有已平倉的做多單(確認是從脈衝星v12 頁「⬇ 匯出 CSV」下載的)")
    trades = even_sample(trades, a.max_trades)
    done = set()
    if os.path.exists(a.out):
        with open(a.out, encoding="utf-8-sig", newline="") as f:
            done = {r["id"] for r in csv.DictReader(f)}
    todo = [t for t in trades if t["id"] not in done]
    print(f"共 {len(trades)} 筆,已做 {len(done)} 筆,這次要做 {len(todo)} 筆")
    if not todo:
        return

    print(f"載入模型 {a.model} / {a.tokenizer} …")
    tok = KronosTokenizer.from_pretrained(a.tokenizer)
    model = Kronos.from_pretrained(a.model)
    try:
        pred = KronosPredictor(model, tok, device=a.device, max_context=512)
    except TypeError:
        pred = KronosPredictor(model, tok, max_context=512)

    # 每個幣一次抓齊需要的整段 K 線
    span = {}
    for t in todo:
        lo, hi = span.get(t["coin"], (math.inf, 0))
        span[t["coin"]] = (min(lo, t["_open_ms"]), max(hi, t["_open_ms"]))
    kl = {}
    for i, (coin, (lo, hi)) in enumerate(sorted(span.items()), 1):
        start = (lo // BAR - a.lookback - 2) * BAR
        kl[coin] = load_klines(coin, start, hi, a.cache)
        print(f"  K 線 {i}/{len(span)} {coin}:{len(kl[coin])} 根")

    out_cols = list(cols) + FEATS + ["k_note"]
    new_file = not os.path.exists(a.out)
    fout = open(a.out, "a", encoding="utf-8-sig" if new_file else "utf-8", newline="")
    w = csv.DictWriter(fout, fieldnames=out_cols, extrasaction="ignore")
    if new_file:
        w.writeheader()

    pred_len = 33  # 進場那根 + 之後 32 根 = 涵蓋進場後 8 小時
    per = max(1, a.batch // a.paths)  # 一批幾筆單(每筆 paths 條)
    t0, n_done = time.time(), 0
    for b in range(0, len(todo), per):
        group, dfs, xts, yts = [], [], [], []
        for t in todo[b: b + per]:
            bar_start = t["_open_ms"] // BAR * BAR
            hist = [k for k in kl.get(t["coin"], []) if k["ts"] < bar_start][-a.lookback:]
            if len(hist) < a.lookback:
                t["k_note"] = f"K線不足({len(hist)})"
                w.writerow(t)
                continue
            df = pd.DataFrame(hist)
            x_df = df[["open", "high", "low", "close", "volume", "amount"]].reset_index(drop=True)
            x_ts = pd.Series(pd.to_datetime(df["ts"], unit="ms")).reset_index(drop=True)
            y_ts = pd.Series(pd.to_datetime([bar_start + i * BAR for i in range(pred_len)], unit="ms"))
            group.append((t, bar_start))
            for _ in range(a.paths):  # 同一筆重複 N 次 = N 條獨立抽樣的走勢
                dfs.append(x_df)
                xts.append(x_ts)
                yts.append(y_ts)
        if group:
            outs = pred.predict_batch(df_list=dfs, x_timestamp_list=xts, y_timestamp_list=yts,
                                      pred_len=pred_len, T=a.temp, top_p=a.top_p, sample_count=1, verbose=False)
            for gi, (t, bar_start) in enumerate(group):
                paths = outs[gi * a.paths:(gi + 1) * a.paths]
                t.update(path_feats(paths, float(t["entry"]), t["_open_ms"], bar_start))
                t["k_note"] = ""
                w.writerow(t)
        fout.flush()
        n_done += len(todo[b: b + per])
        el = time.time() - t0
        eta = el / n_done * (len(todo) - n_done)
        print(f"  {n_done}/{len(todo)} 筆 · 已花 {el / 60:.1f} 分 · 預估還要 {eta / 60:.1f} 分")
    fout.close()
    print(f"完成 → {a.out}\n下一步:python kronos_eval.py report --pred {a.out}")


# ----------------------------- report -----------------------------

TARGETS = [("pnl_pct", "損益%"), ("after_1h", "1H%"), ("after_4h", "4H%"), ("after_8h", "8H%"), ("max_gain", "最大獲利%")]


def cmd_report(a):
    import pandas as pd
    df = pd.read_csv(a.pred, encoding="utf-8-sig")
    df = df[df["k_note"].isna() | (df["k_note"] == "")].copy()
    if df.empty:
        sys.exit("沒有預測完成的資料")
    for c, _ in TARGETS:
        df[c] = pd.to_numeric(df.get(c), errors="coerce")
    df["open_dt"] = pd.to_datetime(df["open_time"], utc=True)
    df = df.sort_values("open_dt").reset_index(drop=True)
    df["half"] = ["前半" if i < len(df) / 2 else "後半" for i in range(len(df))]
    df["win"] = df["pnl_pct"] > 0
    df["ceil"] = df["max_gain"] < 1.8  # 「進場即最高」:進場後從沒漲超過 +1.8%
    pd.set_option("display.width", 200)

    print(f"樣本 {len(df)} 筆 · {df['open_dt'].min():%m/%d} ~ {df['open_dt'].max():%m/%d}")
    print(f"全部:平均損益 {df['pnl_pct'].mean():+.2f}% · 勝率 {df['win'].mean() * 100:.0f}% · "
          f"8H 平均 {df['after_8h'].mean():+.2f}% · 「進場即最高」比例 {df['ceil'].mean() * 100:.0f}%\n")

    print("== 1. 相關性(Spearman,|值|>0.05 才算有點東西;正 = 預測越高、實際越好)==")
    # Spearman = 排名後的 Pearson(自己算,不用另外裝 scipy)
    corr = pd.DataFrame({lab: [df[f].rank().corr(df[c].rank()) for f in FEATS] for c, lab in TARGETS}, index=FEATS)
    print(corr.round(3).to_string(), "\n")

    print("== 2. 分 5 組(Q1 = 預測最低、Q5 = 最高)==")
    summary = []
    for f in FEATS:
        try:
            df["q"] = pd.qcut(df[f].rank(method="first"), 5, labels=["Q1", "Q2", "Q3", "Q4", "Q5"])
        except ValueError:
            continue
        g = df.groupby("q", observed=True).agg(筆數=("pnl_pct", "size"), 平均損益=("pnl_pct", "mean"),
                                              勝率=("win", "mean"), 平均8H=("after_8h", "mean"),
                                              平均最大獲利=("max_gain", "mean"), 進場即最高=("ceil", "mean"),
                                              預測值=(f, "mean"))
        g["勝率"] = (g["勝率"] * 100).round(0)
        g["進場即最高"] = (g["進場即最高"] * 100).round(0)
        print(f"--- {f} ---")
        print(g.round(3).to_string())
        # 前後半各自分 5 組(用各自的高低),看 Q5−Q1 是不是同方向
        diff = {}
        for h, d in df.groupby("half"):
            qh = pd.qcut(d[f].rank(method="first"), 5, labels=["Q1", "Q2", "Q3", "Q4", "Q5"])
            qq = d.groupby(qh, observed=True)["pnl_pct"].mean()
            diff[h] = qq.get("Q5", float("nan")) - qq.get("Q1", float("nan"))
        same = diff.get("前半", 0) * diff.get("後半", 0) > 0
        print(f"Q5−Q1 平均損益差:前半 {diff.get('前半', float('nan')):+.2f}% / 後半 {diff.get('後半', float('nan')):+.2f}%"
              f" → {'前後同方向' if same else '前後不一致(不可靠)'}\n")
        summary.append((f, diff.get("前半", float("nan")), diff.get("後半", float("nan")), same))

    print("== 3. 總結(只看前後同方向、且兩段差距都 ≥ 0.2% 的)==")
    if len(df) < 500:
        print(f"⚠️ 只有 {len(df)} 筆,每組不到 100 筆,結果很容易是運氣;正式判斷請跑全部的單。")
    print("⚠️ 一次看 8 個指標,純隨機也常有 1 個碰巧過關;只有 1 個過、或過的指標彼此矛盾,都不算數。")
    keep = [s for s in summary if s[3] and min(abs(s[1]), abs(s[2])) >= 0.2]
    if not keep:
        print("沒有任何指標過關 → Kronos 對 v12 的進場沒有可靠的分辨力,建議停在這一步。")
    for f, h1, h2, _ in keep:
        print(f"{f}:Q5−Q1 前半 {h1:+.2f}% / 後半 {h2:+.2f}% → 值得進下一步(上線只記錄、不擋單)")


def main():
    ap = argparse.ArgumentParser(description="Kronos × 脈衝星v12 離線驗證")
    sub = ap.add_subparsers(dest="cmd", required=True)
    p = sub.add_parser("predict", help="對每筆單跑 Kronos 預測")
    p.add_argument("--csv", required=True, help="後台匯出的 v12 CSV")
    p.add_argument("--kronos-dir", required=True, help="git clone 下來的 Kronos 資料夾")
    p.add_argument("--out", default="kronos_v12.csv")
    p.add_argument("--model", default="NeoQuasar/Kronos-small")
    p.add_argument("--tokenizer", default="NeoQuasar/Kronos-Tokenizer-base")
    p.add_argument("--device", default="cpu", help="有 NVIDIA 顯卡用 cuda:0;Mac M 系列可試 mps")
    p.add_argument("--lookback", type=int, default=400)
    p.add_argument("--paths", type=int, default=10, help="每筆產生幾條走勢")
    p.add_argument("--batch", type=int, default=40, help="一次丟給模型幾條(記憶體不夠就調小)")
    p.add_argument("--temp", type=float, default=1.0)
    p.add_argument("--top-p", type=float, default=0.9)
    p.add_argument("--max-trades", type=int, default=0, help="先試跑:依時間平均抽幾筆(0 = 全部)")
    p.add_argument("--cache", default="kline_cache")
    p.add_argument("--seed", type=int, default=42)
    p.set_defaults(fn=cmd_predict)
    r = sub.add_parser("report", help="看預測跟實際結果的關係")
    r.add_argument("--pred", required=True)
    r.set_defaults(fn=cmd_report)
    a = ap.parse_args()
    a.fn(a)


if __name__ == "__main__":
    main()
