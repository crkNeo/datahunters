#!/usr/bin/env python3
"""
Backtest harness for the anomaly score.

What it does
------------
1. Pulls historical 5m candles for a basket of coins from OKX's public API.
2. For each bar, reconstructs the inputs the live scorer sees (price change,
   a proxy OI change, a proxy CVD ratio) and computes the same score.
3. When |score| >= threshold, opens a hypothetical trade in the score's
   direction and checks whether price moved the predicted way over a forward
   horizon (e.g. next 12 bars = 1h).
4. Reports hit-rate, average forward return, and a breakdown by score bucket
   and by quality tier — so you can SEE whether higher scores actually predict
   better, and retune the weights with evidence instead of guesswork.

Important honesty note
----------------------
OKX's public candle endpoint gives price/volume but NOT historical per-bar OI
or true CVD. This harness uses *proxies* (volume-imbalance for CVD, and you can
plug a real OI history source later). So treat the absolute numbers as a
RELATIVE comparison tool for tuning weights, not as a live-accurate P&L.
For production-grade results, persist real OI snapshots over time or buy an OI
history feed, then swap the proxy functions below.

Usage
-----
    pip install requests
    python backtest.py --coins BTC,ETH,SOL --bars 600 --threshold 40 --horizon 12
"""

import argparse
import math
import time
import statistics
from dataclasses import dataclass
import urllib.request
import urllib.parse
import json

OKX = "https://www.okx.com"


# ----------------------------- data fetch -----------------------------

def fetch_candles(inst_id: str, bar: str = "5m", limit: int = 300):
    """Return candles oldest->newest as list of dicts. OKX returns newest first."""
    out = []
    after = ""  # pagination cursor (ts); OKX returns < after
    while len(out) < limit:
        params = {"instId": inst_id, "bar": bar, "limit": "100"}
        if after:
            params["after"] = after
        url = f"{OKX}/api/v5/market/candles?" + urllib.parse.urlencode(params)
        try:
            with urllib.request.urlopen(url, timeout=10) as r:
                data = json.loads(r.read())
        except Exception as e:
            print(f"  fetch error {inst_id}: {e}")
            break
        rows = data.get("data", [])
        if not rows:
            break
        for row in rows:
            out.append({
                "ts": int(row[0]),
                "o": float(row[1]), "h": float(row[2]),
                "l": float(row[3]), "c": float(row[4]),
                "vol": float(row[5]) if len(row) > 5 else 0.0,
                "volccy": float(row[7]) if len(row) > 7 else 0.0,
            })
        after = str(rows[-1][0])
        time.sleep(0.15)  # be polite
    out = sorted(out, key=lambda x: x["ts"])
    return out[-limit:]


# ----------------------------- proxy indicators -----------------------------

def pct_change(a: float, b: float) -> float:
    if a == 0:
        return 0.0
    return (b - a) / a * 100.0


def cvd_proxy(candles, i, window=12) -> float:
    """
    Proxy for CVD ratio using candle direction * volume.
    A green bar contributes +volume, red bar -volume. Net / total * 100.
    This is a stand-in for true trade-level CVD; correlation is decent for tuning.
    """
    lo = max(0, i - window + 1)
    net = 0.0
    tot = 0.0
    for k in range(lo, i + 1):
        c = candles[k]
        vol = c["volccy"] or c["vol"]
        tot += vol
        if c["c"] >= c["o"]:
            net += vol
        else:
            net -= vol
    if tot == 0:
        return 0.0
    return net / tot * 100.0


def oi_proxy(candles, i, window=12) -> float:
    """
    Placeholder OI 1h change. Real OI history isn't in the candle feed, so we
    approximate 'positioning pressure' with volume trend: rising volume into a
    move ~ rising OI. Swap this for a real OI series when you have one.
    """
    lo = max(0, i - window + 1)
    if i - lo < 2:
        return 0.0
    early = statistics.mean(c["volccy"] or c["vol"] for c in candles[lo:lo + 3])
    late = statistics.mean(c["volccy"] or c["vol"] for c in candles[i - 2:i + 1])
    return pct_change(early, late) if early else 0.0


# ----------------------------- the score (python port) -----------------------------

@dataclass
class Weights:
    oi_max: float = 40; oi_half: float = 6
    price_max: float = 15; price_half: float = 3
    cvd_max: float = 20; cvd_half: float = 10
    funding_max: float = 14; funding_extr: float = 1.0
    diverge_bonus: float = 12
    resonate_bonus: float = 14


def smooth_sat(x, mx, half):
    if half <= 0:
        return 0.0
    return mx * x / (abs(x) + half)


def sign(x):
    return (x > 0) - (x < 0)


def score(price5, price15, oi_chg, cvd_ratio, funding_pct, w: Weights):
    bd = {}
    bd["oi"] = round(smooth_sat(oi_chg, w.oi_max, w.oi_half))
    price = 0.6 * price5 + 0.4 * price15
    bd["price"] = round(smooth_sat(price, w.price_max, w.price_half))
    bd["cvd"] = round(smooth_sat(cvd_ratio, w.cvd_max, w.cvd_half))
    bd["funding"] = round(-smooth_sat(funding_pct, w.funding_max, w.funding_extr))

    pdir, odir, cdir = sign(price), sign(oi_chg), sign(cvd_ratio)
    if pdir != 0:
        if cdir != 0 and cdir != pdir:
            bd["divergence"] = round(-pdir * w.diverge_bonus)
        if odir > 0 and odir != pdir:
            bd["oi_fight"] = round(-pdir * w.diverge_bonus * 0.6)

    pos = sum(1 for k in ("oi", "price", "cvd", "funding") if bd.get(k, 0) > 0)
    neg = sum(1 for k in ("oi", "price", "cvd", "funding") if bd.get(k, 0) < 0)
    if pos >= 3:
        bd["resonance"] = int(w.resonate_bonus)
    elif neg >= 3:
        bd["resonance"] = -int(w.resonate_bonus)

    total = sum(bd.values())
    quality_factors = sum(1 for k in ("oi", "price", "cvd", "funding") if abs(bd.get(k, 0)) >= 12)
    if "divergence" in bd:
        quality_factors += 1
    return total, bd, quality_factors


# ----------------------------- backtest loop -----------------------------

def backtest(coins, bars, bar_size, threshold, horizon, w: Weights):
    trades = []  # each: (coin, ts, score, quality, fwd_ret_signed)
    for coin in coins:
        inst = f"{coin}-USDT-SWAP"
        candles = fetch_candles(inst, bar_size, bars)
        if len(candles) < horizon + 30:
            print(f"  {coin}: not enough data ({len(candles)} bars), skipping")
            continue
        print(f"  {coin}: {len(candles)} bars")

        for i in range(20, len(candles) - horizon):
            price5 = pct_change(candles[i - 1]["o"], candles[i]["c"])
            price15 = pct_change(candles[i - 3]["o"], candles[i]["c"])
            oi = oi_proxy(candles, i)
            cvd = cvd_proxy(candles, i)
            funding_pct = 0.0  # historical funding not pulled here; left neutral
            s, bd, qf = score(price5, price15, oi, cvd, funding_pct, w)

            if abs(s) < threshold:
                continue

            entry = candles[i]["c"]
            exit_ = candles[i + horizon]["c"]
            raw_ret = pct_change(entry, exit_)
            # signed by predicted direction: positive = score was "right"
            direction = 1 if s > 0 else -1
            signed = raw_ret * direction
            quality = "高品質" if qf >= 4 else ("一般" if qf >= 2 else "觀察")
            trades.append((coin, candles[i]["ts"], s, quality, signed))

    return trades


def report(trades, threshold, horizon):
    if not trades:
        print("\nNo trades triggered at this threshold. Lower --threshold.")
        return
    wins = [t for t in trades if t[4] > 0]
    hit = len(wins) / len(trades) * 100
    rets = [t[4] for t in trades]
    avg = statistics.mean(rets)
    med = statistics.median(rets)
    print("\n" + "=" * 56)
    print(f"RESULTS  (threshold={threshold}, horizon={horizon} bars)")
    print("=" * 56)
    print(f"trades triggered : {len(trades)}")
    print(f"hit-rate         : {hit:.1f}%   (>50% = predictive)")
    print(f"avg fwd return   : {avg:+.3f}%  (signed by predicted direction)")
    print(f"median fwd return: {med:+.3f}%")
    print(f"expectancy/trade : {avg:+.3f}%  before fees/slippage")

    # breakdown by score bucket -> does a higher score predict better?
    print("\nby |score| bucket:")
    buckets = [(threshold, 50), (50, 65), (65, 80), (80, 999)]
    for lo, hi in buckets:
        sub = [t for t in trades if lo <= abs(t[2]) < hi]
        if not sub:
            continue
        w_ = sum(1 for t in sub if t[4] > 0) / len(sub) * 100
        a_ = statistics.mean(t[4] for t in sub)
        print(f"  {lo:>3}-{hi:<3}: n={len(sub):<4} hit={w_:4.1f}%  avg={a_:+.3f}%")

    # breakdown by quality tier
    print("\nby quality tier:")
    for q in ("高品質", "一般", "觀察"):
        sub = [t for t in trades if t[3] == q]
        if not sub:
            continue
        w_ = sum(1 for t in sub if t[4] > 0) / len(sub) * 100
        a_ = statistics.mean(t[4] for t in sub)
        print(f"  {q}: n={len(sub):<4} hit={w_:4.1f}%  avg={a_:+.3f}%")

    print("\nNote: proxies used for OI/CVD/funding. Use this to COMPARE weight")
    print("settings relative to each other, not as live P&L. Higher buckets")
    print("SHOULD show higher hit-rate if your scoring has real signal.")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--coins", default="BTC,ETH,SOL,XRP,DOGE,BNB")
    ap.add_argument("--bars", type=int, default=500, help="candles per coin")
    ap.add_argument("--bar-size", default="5m")
    ap.add_argument("--threshold", type=int, default=40, help="min |score| to trade")
    ap.add_argument("--horizon", type=int, default=12, help="forward bars to evaluate")
    args = ap.parse_args()

    coins = [c.strip().upper() for c in args.coins.split(",") if c.strip()]
    w = Weights()
    print(f"backtesting {coins} | {args.bars} bars @ {args.bar_size} "
          f"| threshold={args.threshold} | horizon={args.horizon}")
    trades = backtest(coins, args.bars, args.bar_size, args.threshold, args.horizon, w)
    report(trades, args.threshold, args.horizon)


if __name__ == "__main__":
    main()
