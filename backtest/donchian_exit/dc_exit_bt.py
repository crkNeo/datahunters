#!/usr/bin/env python3
"""
唐奇安出場回測:進場用你匯出的實際單(CSV),出場換成不同規則重新用真實 1 小時 K 線重播,
比較哪一種出場比較好。只用 Python 內建模組,不用另外裝套件。

用法(在 backtest/donchian_exit 資料夾):
    py dc_exit_bt.py --csv donchian.csv donchianv2.csv
    py dc_exit_bt.py --csv donchian.csv --out result.csv     # 每筆單在每種規則下的結果另存 CSV

K 線會存在 kline_cache/,重跑不會重抓。說明見 README.md。
"""
import argparse
import csv
import json
import os
import statistics as st
import sys
import time
import urllib.error
import urllib.request
import io
import zipfile

HOUR = 3600_000
API = "https://fapi.binance.com/fapi/v1/klines"
VISION = "https://data.binance.vision/data/futures/um/daily/klines/{s}/1h/{s}-1h-{d}.zip"
DAY = 24 * HOUR
ATR_N = 24


# ---------------------------------------------------------------- 讀單

def read_trades(paths):
    """讀匯出的 CSV,合併去重(同幣同進場時間只留一筆:初版和 v2 進場一樣)。"""
    seen, out = {}, []
    for p in paths:
        with open(p, encoding="utf-8-sig") as fh:
            for r in csv.DictReader(fh):
                if r.get("outcome") in ("manual", "cancel") or r.get("status") == "pending":
                    continue
                try:
                    entry, sl = float(r["entry"]), float(r["sl"])
                except (KeyError, ValueError):
                    continue
                if entry <= 0 or sl <= 0:
                    continue
                ts = parse_time(r["open_time"])
                key = (r["coin"], ts)
                rec = (r.get("status", ""), float(r.get("pnl_pct") or 0))
                if key in seen:  # 同一筆進場:記下另一本書的實際結果(驗算用)
                    seen[key]["recs"][r.get("book", "")] = rec
                    continue
                t = {"coin": r["coin"], "dir": r["dir"], "entry": entry, "sl": sl, "open_ms": ts,
                     "recs": {r.get("book", ""): rec}}
                seen[key] = t
                out.append(t)
    out.sort(key=lambda t: t["open_ms"])
    return out


def parse_time(s):
    # 2026-11-04T10:00:00Z
    s = s.strip().replace("Z", "")
    tm = time.strptime(s[:19], "%Y-%m-%dT%H:%M:%S")
    return int(_timegm(tm)) * 1000


def _timegm(tm):
    import calendar
    return calendar.timegm(tm)


# ---------------------------------------------------------------- 抓 K 線

def fetch_klines(coin, start_ms, end_ms, cache_dir, offline=False):
    """抓 coin 在 [start_ms, end_ms) 的 1h K(舊→新),存快取。回傳 [ts, o, h, l, c, v]。"""
    os.makedirs(cache_dir, exist_ok=True)
    fn = os.path.join(cache_dir, f"{coin}_{start_ms}_{end_ms}.json")
    if os.path.exists(fn):
        with open(fn) as fh:
            return json.load(fh)
    if offline:
        return []
    bars, cur = [], start_ms
    while cur < end_ms:
        url = f"{API}?symbol={coin}USDT&interval=1h&startTime={cur}&endTime={end_ms - 1}&limit=1000"
        for attempt in range(4):
            try:
                with urllib.request.urlopen(url, timeout=20) as resp:
                    raw = json.load(resp)
                break
            except Exception as e:  # 網路 / 限流:等一下再試
                if attempt == 3:
                    print(f"  ⚠️ {coin} 抓不到 K 線:{e}", file=sys.stderr)
                    return []
                time.sleep(2 * (attempt + 1))
        if not raw:
            break
        for k in raw:
            bars.append([int(k[0]), float(k[1]), float(k[2]), float(k[3]), float(k[4]), float(k[5])])
        nxt = int(raw[-1][0]) + HOUR
        if nxt <= cur:
            break
        cur = nxt
        time.sleep(0.25)
    with open(fn, "w") as fh:
        json.dump(bars, fh)
    return bars


def fetch_vision(coin, start_ms, end_ms, cache_dir, offline=False):
    """從 Binance 公開歷史檔(data.binance.vision,每天一個檔,只到昨天)抓 1h K。
    給連不到 fapi(例如所在地區被擋)的時候用。"""
    os.makedirs(cache_dir, exist_ok=True)
    sym = coin + "USDT"
    bars = []
    d = start_ms // DAY * DAY
    while d < end_ms:
        day = time.strftime("%Y-%m-%d", time.gmtime(d / 1000))
        fn = os.path.join(cache_dir, f"v_{sym}_{day}.json")
        rows = None
        if os.path.exists(fn):
            with open(fn) as fh:
                rows = json.load(fh)
        elif not offline:
            rows = []
            for attempt in range(3):
                try:
                    with urllib.request.urlopen(VISION.format(s=sym, d=day), timeout=30) as resp:
                        z = zipfile.ZipFile(io.BytesIO(resp.read()))
                    for line in z.read(z.namelist()[0]).decode().splitlines():
                        f = line.split(",")
                        if not f or not f[0].isdigit():
                            continue  # 表頭
                        rows.append([int(f[0]), float(f[1]), float(f[2]), float(f[3]), float(f[4]), float(f[5])])
                    break
                except urllib.error.HTTPError as e:
                    if e.code == 404:  # 那天還沒有檔(今天)或幣還沒上市
                        break
                    time.sleep(2 * (attempt + 1))
                except Exception:
                    time.sleep(2 * (attempt + 1))
            if d + DAY <= int(time.time() * 1000) - DAY:  # 確定已經發布的日子才存快取
                with open(fn, "w") as fh:
                    json.dump(rows, fh)
        bars.extend(rows or [])
        d += DAY
    return [b for b in bars if start_ms <= b[0] < end_ms]


# ---------------------------------------------------------------- 出場規則

def rule(name, desc, **kw):
    """一種出場規則。參數:
    chan        出場通道根數(收盤破前 N 根低 / 高 → 下一根開盤)
    chand_k     吊燈停利倍數(0 = 沒有);進場後最高 / 最低收盤 ∓ k × ATR
    atr_cap     True = 吊燈用 min(目前 ATR, 進場 ATR),ATR 只准變小
    use_high    True = 吊燈的「最高」用盤中高點(空單低點)而不是收盤
    tight       [(到幾 R, 改成幾倍進場 ATR)]:賺到某個 R 之後吊燈改用比較近的距離
    partial     [(幾 R, 平幾成)]:盤中碰到就先平這一部分
    climax      爆量反轉 K:賺過這麼多 R 之後,量 ≥ 3 × 前 24 根均量、收在不利的 40% → 下一根開盤出
    wick        創新高的長上影:創進場後新高、上影 ≥ 60% 全距、全距 ≥ 1 ATR → 下一根開盤出
    vol_low     跌破爆量 K:進場後最大量那根(≥ 3 × 均量)的低點(空單高點)被收盤破 → 下一根開盤出
    """
    d = dict(name=name, desc=desc, chan=12, chand_k=0.0, atr_cap=False, use_high=False,
             tight=[], partial=[], climax=0.0, wick=False, vol_low=False)
    d.update(kw)
    return d


RULES = [
    rule("V1", "初版:12 根通道"),
    rule("V2", "v2 現行:12 根通道 + 吊燈 3×ATR(目前 ATR)", chand_k=3),
    rule("V2cap", "吊燈 3×ATR,ATR 只准變小(上限 = 進場 ATR)", chand_k=3, atr_cap=True),
    rule("V2cap2", "吊燈 2×ATR,ATR 只准變小", chand_k=2, atr_cap=True),
    rule("V2cap1.5", "吊燈 1.5×ATR,ATR 只准變小", chand_k=1.5, atr_cap=True),
    rule("V2hi", "吊燈 3×ATR 只准變小,最高點用盤中高點(吃得到插針)", chand_k=3, atr_cap=True, use_high=True),
    rule("Tight2/3", "V2cap,賺到 2R 後吊燈改 2×、到 3R 後改 1.5×進場 ATR", chand_k=3, atr_cap=True,
         tight=[(2, 2), (3, 1.5)]),
    rule("Part2/4", "V2cap,到 2R 平 1/3、到 4R 再平 1/3", chand_k=3, atr_cap=True, partial=[(2, 1 / 3), (4, 1 / 3)]),
    rule("Part2half", "V2 現行,到 2R 平一半", chand_k=3, partial=[(2, 0.5)]),
    rule("Climax", "V2cap + 爆量反轉 K(賺過 1.5R 後)", chand_k=3, atr_cap=True, climax=1.5),
    rule("Wick", "V2cap + 創新高的長上影", chand_k=3, atr_cap=True, wick=True),
    rule("VolLow", "V2cap + 收盤跌破爆量 K 低點", chand_k=3, atr_cap=True, vol_low=True),
    rule("Dist3", "V2cap + 三種出貨訊號全開", chand_k=3, atr_cap=True, climax=1.5, wick=True, vol_low=True),
]


def atr_at(bars, i):
    """bars[i] 收盤時的 ATR(24):第 i-23..i 根真實波幅平均(跟系統 dcATR 一樣)。"""
    if i < ATR_N:
        return 0.0
    s = 0.0
    for j in range(i - ATR_N + 1, i + 1):
        h, l, pc = bars[j][2], bars[j][3], bars[j - 1][4]
        s += max(h - l, abs(h - pc), abs(l - pc))
    return s / ATR_N


def replay(t, bars, rl):
    """用 rl 規則重播一筆單。回傳 dict(r=最後損益 R、mfe=最大有利 R、outcome、bars=持有根數、open=還沒出場)。"""
    long = t["dir"] == "long"
    sgn = 1 if long else -1
    entry, R = t["entry"], abs(t["entry"] - t["sl"])
    atr_entry = R / 3.0
    i0 = next((i for i, b in enumerate(bars) if b[0] == t["open_ms"]), None)
    if i0 is None or R <= 0:
        return None
    stop = t["sl"]
    left, realized = 1.0, 0.0  # 剩下幾成倉位 / 已實現(R,依倉位加權)
    parts = list(rl["partial"])
    ext_c = None  # 進場後最有利的收盤
    ext_h = None  # 進場後最有利的盤中價
    mfe = 0.0
    vol_bar = None  # 進場後最大量那根(≥ 3 × 均量)
    gain = lambda px: sgn * (px - entry) / R

    def done(px, outcome, i, still_open=False):
        r = realized + left * gain(px)
        return dict(r=r, mfe=mfe, outcome=outcome, bars=i - i0 + 1, open=still_open)

    for i in range(i0, len(bars)):
        ts, o, h, l, c, v = bars[i]
        fav, adv = (h, l) if long else (l, h)
        # 1) 盤中:初始停損(跳空用開盤價)
        if (long and l <= stop) or (not long and h >= stop):
            px = min(stop, o) if long else max(stop, o)
            return done(px, "sl", i)
        # 2) 盤中:分批止盈
        while parts and gain(fav) >= parts[0][0]:
            lv, frac = parts.pop(0)
            px = entry + sgn * lv * R
            if (long and o > px) or (not long and o < px):
                px = o
            frac = min(frac, left)
            realized += frac * gain(px)
            left -= frac
        mfe = max(mfe, gain(fav))
        # 3) 收盤後:更新進場後最有利收盤 / 盤中價
        newhigh = ext_h is None or (long and h > ext_h) or (not long and l < ext_h)
        if ext_c is None or (long and c > ext_c) or (not long and c < ext_c):
            ext_c = c
        if newhigh:
            ext_h = fav
        atr = atr_at(bars, i)
        avgv = st.mean(b[5] for b in bars[max(0, i - 24):i]) if i > 0 else 0
        if avgv > 0 and v >= 3 * avgv and (vol_bar is None or v > vol_bar[5]) and i > i0:
            vol_bar = bars[i]
        nxt = bars[i + 1][1] if i + 1 < len(bars) else None
        exit_reason = None
        # 通道
        if rl["chan"] and i >= rl["chan"]:
            lo = min(b[3] for b in bars[i - rl["chan"]:i])
            hi = max(b[2] for b in bars[i - rl["chan"]:i])
            if (long and c < lo) or (not long and c > hi):
                exit_reason = "channel"
        # 吊燈
        if not exit_reason and rl["chand_k"] > 0 and atr > 0:
            k = rl["chand_k"]
            a = min(atr, atr_entry) if rl["atr_cap"] else atr
            peak_r = gain(ext_c)
            for lvr, kk in rl["tight"]:
                if peak_r >= lvr:
                    k, a = kk, min(atr, atr_entry)
            ref = ext_h if rl["use_high"] else ext_c
            line = ref - sgn * k * a
            if (long and c < line) or (not long and c > line):
                exit_reason = "chandelier"
        rng = h - l
        # 爆量反轉 K
        if not exit_reason and rl["climax"] and mfe >= rl["climax"] and avgv > 0 and v >= 3 * avgv and rng > 0:
            pos = (c - l) / rng if long else (h - c) / rng  # 1 = 收在最有利那端
            if pos <= 0.4:
                exit_reason = "climax"
        # 創新高的長上影
        if not exit_reason and rl["wick"] and newhigh and i > i0 and rng > 0 and atr > 0 and rng >= atr:
            wick = (h - max(o, c)) if long else (min(o, c) - l)
            if wick / rng >= 0.6:
                exit_reason = "wick"
        # 跌破爆量 K
        if not exit_reason and rl["vol_low"] and vol_bar is not None and bars[i] is not vol_bar:
            if (long and c < vol_bar[3]) or (not long and c > vol_bar[2]):
                exit_reason = "vol_low"
        if exit_reason:
            if nxt is None:  # 最後一根:還沒有下一根開盤 → 先記收盤、算持倉中
                return done(c, exit_reason, i, still_open=True)
            return done(nxt, exit_reason, i + 1)
    last = bars[-1]
    return done(last[4], "open", len(bars) - 1, still_open=True)


# ---------------------------------------------------------------- 統計

def summarize(res):
    rs = [x["r"] for x in res]
    if not rs:
        return None
    big = [x for x in res if x["mfe"] >= 1]
    return dict(n=len(rs), avg=st.mean(rs), med=st.median(rs), tot=sum(rs),
                win=sum(r > 0 for r in rs) / len(rs) * 100,
                flip=sum(1 for x in big if x["r"] <= 0),
                give=st.mean(x["mfe"] - x["r"] for x in big) if big else 0,
                best=max(rs), still=sum(1 for x in res if x["open"]))


def fmt(s):
    if not s:
        return "(無)"
    return (f"{s['n']:>4} 筆  平均 {s['avg']:+.3f}R  中位 {s['med']:+.2f}R  總和 {s['tot']:+7.1f}R  "
            f"勝率 {s['win']:4.0f}%  賺過1R轉負 {s['flip']:>3}  回吐 {s['give']:.2f}R  最大 {s['best']:+.1f}R")


def main():
    ap = argparse.ArgumentParser(description="唐奇安出場回測(進場用實際單,出場換規則重播)")
    ap.add_argument("--csv", nargs="+", required=True, help="匯出的唐奇安 / 唐奇安v2 CSV(可多個,會去重)")
    ap.add_argument("--cache", default="kline_cache", help="K 線快取資料夾")
    ap.add_argument("--out", help="每筆單 × 每種規則的結果另存 CSV")
    ap.add_argument("--offline", action="store_true", help="只用快取,不連網(測試用)")
    ap.add_argument("--source", choices=["api", "vision"], default="api",
                    help="api = Binance 即時 API(預設);vision = 公開歷史檔(只到昨天,API 被擋時用)")
    a = ap.parse_args()

    trades = read_trades(a.csv)
    if not trades:
        sys.exit("CSV 裡沒有可用的單")
    now = int(time.time() * 1000) // HOUR * HOUR
    if a.source == "vision":
        now = now // DAY * DAY  # 歷史檔只到昨天
        trades = [t for t in trades if t["open_ms"] < now - HOUR]
    print(f"讀到 {len(trades)} 筆單(去重後),{trades[0]['coin']} … 期間 "
          f"{time.strftime('%m/%d %H:%M', time.gmtime(trades[0]['open_ms'] / 1000))} ~ "
          f"{time.strftime('%m/%d %H:%M', time.gmtime(trades[-1]['open_ms'] / 1000))} UTC")

    # 每個幣抓一次:最早進場前 30 根 → 現在(含還在走的那根就丟掉)
    by_coin = {}
    for t in trades:
        by_coin.setdefault(t["coin"], []).append(t)
    bars_of = {}
    for n, (coin, ts) in enumerate(sorted(by_coin.items()), 1):
        start = min(t["open_ms"] for t in ts) - 30 * HOUR
        if a.source == "vision":
            bars = fetch_vision(coin, start, now, a.cache, a.offline)
        else:
            bars = fetch_klines(coin, start, now, a.cache, a.offline)
        bars_of[coin] = [b for b in bars if b[0] < now]
        print(f"\r抓 K 線 {n}/{len(by_coin)} {coin:<12}", end="", file=sys.stderr)
    print(file=sys.stderr)

    results = {r["name"]: [] for r in RULES}
    rows = []
    skipped = 0
    for t in trades:
        bars = bars_of.get(t["coin"]) or []
        per = {}
        for rl in RULES:
            x = replay(t, bars, rl)
            if x is None:
                break
            per[rl["name"]] = x
        if len(per) != len(RULES):
            skipped += 1
            continue
        for k, x in per.items():
            x["t"] = t
            results[k].append(x)
        rows.append((t, per))
    print(f"重播 {len(rows)} 筆(K 線對不上而跳過 {skipped} 筆)\n")
    if not rows:
        sys.exit("沒有可以重播的單")

    # 驗算:同一套規則重播出來的結果,要跟系統實際記錄的差不多(初版 → V1、v2 → V2),否則下面的比較不可信
    for book, rn in (("donchian", "V1"), ("donchianv2", "V2")):
        chk = [(t, per[rn]) for t, per in rows if t["recs"].get(book, ("",))[0] == "closed" and not per[rn]["open"]]
        if chk:
            diff = [abs(x["r"] - t["recs"][book][1] / (abs(t["entry"] - t["sl"]) / t["entry"] * 100)) for t, x in chk]
            ok = sum(d <= 0.15 for d in diff)
            print(f"驗算 {book}:{len(chk)} 筆已平倉單用 {rn} 重播,{ok} 筆({ok / len(chk) * 100:.0f}%)跟系統紀錄相差 ≤ 0.15R")
    print()

    mid = rows[len(rows) // 2][0]["open_ms"]
    print("說明:R = 進場到初始停損的距離。「賺過1R轉負」= 最大有利到過 1R、最後 ≤ 0 的筆數;")
    print("      回吐 = 到過 1R 的單,最大有利 − 最後損益 的平均。持倉中的單用最新收盤計算。\n")
    order = sorted(RULES, key=lambda r: -summarize(results[r["name"]])["avg"])
    for rl in order:
        res = results[rl["name"]]
        a1 = summarize([x for x in res if x["t"]["open_ms"] < mid])
        a2 = summarize([x for x in res if x["t"]["open_ms"] >= mid])
        lo = summarize([x for x in res if x["t"]["dir"] == "long"])
        sh = summarize([x for x in res if x["t"]["dir"] == "short"])
        print(f"【{rl['name']}】{rl['desc']}")
        print(f"   全部 {fmt(summarize(res))}")
        print(f"   前半 平均 {a1['avg']:+.3f}R ({a1['n']} 筆) | 後半 平均 {a2['avg']:+.3f}R ({a2['n']} 筆) | "
              f"多 {lo['avg'] if lo else 0:+.3f}R | 空 {sh['avg'] if sh else 0:+.3f}R")
    base = summarize(results["V2"])
    print("\n跟 v2 現行比(平均每筆差幾 R;前後兩半都要比較好才算穩):")
    for rl in order:
        if rl["name"] == "V2":
            continue
        res = results[rl["name"]]
        d1 = summarize([x for x in res if x["t"]["open_ms"] < mid])["avg"] - summarize(
            [x for x in results["V2"] if x["t"]["open_ms"] < mid])["avg"]
        d2 = summarize([x for x in res if x["t"]["open_ms"] >= mid])["avg"] - summarize(
            [x for x in results["V2"] if x["t"]["open_ms"] >= mid])["avg"]
        tag = "✅ 兩半都比較好" if d1 > 0 and d2 > 0 else ("❌ 兩半都比較差" if d1 < 0 and d2 < 0 else "⚠️ 一好一壞")
        print(f"   {rl['name']:<10} {summarize(res)['avg'] - base['avg']:+.3f}R  (前半 {d1:+.3f} / 後半 {d2:+.3f})  {tag}")

    if a.out:
        with open(a.out, "w", newline="", encoding="utf-8-sig") as fh:
            w = csv.writer(fh)
            w.writerow(["coin", "dir", "open_time", "entry", "sl"] +
                       [f"{r['name']}_{k}" for r in RULES for k in ("r", "outcome", "bars")])
            for t, per in rows:
                w.writerow([t["coin"], t["dir"], time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(t["open_ms"] / 1000)),
                            t["entry"], t["sl"]] +
                           [v for r in RULES for v in (round(per[r["name"]]["r"], 3), per[r["name"]]["outcome"],
                                                       per[r["name"]]["bars"])])
        print(f"\n每筆明細 → {a.out}")


if __name__ == "__main__":
    main()
