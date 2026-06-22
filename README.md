# DataHunter (self-hosted)

A self-hosted market-data dashboard that aggregates **public** OKX + Binance
derivatives data, computes OI / CVD / funding-rate indicators, scores each coin,
and serves it to a Vue3 frontend. Built as a clean-room equivalent of the kind of
"market anomaly" dashboard you analysed — using only free, keyless, documented
exchange endpoints. It does **not** call or depend on any third-party paid service.

## Architecture

```
OKX / Binance public APIs        (free, keyless)
        │
        ▼
 internal/exchange   ──► raw fetch: funding-rate, open-interest, candles, aggTrades
        │
        ▼
 internal/indicator  ──► CVD, CVD ratio, % change
        │
        ▼
 internal/cache      ──► per-coin Snapshot {okx_chg, oi_chg_1h, cvd_ratio, funding}
        │                 refreshed on a 60s ticker  (mirrors the oi-cache table)
        ▼
 internal/scorer     ──► weighted score → bias (long/short/neutral) + quality
        │
        ▼
 internal/api        ──► /api/oi-cache , /api/signals   (CORS-enabled JSON)
        │
        ▼
 frontend (Vue3+Vite) ──► dashboard table + signals view, polls every 15s
```

## Data sources (all public)

| Metric        | Endpoint |
|---------------|----------|
| Funding rate  | `GET okx.com/api/v5/public/funding-rate?instId=BTC-USDT-SWAP` |
| Open interest | `GET okx.com/api/v5/public/open-interest?instId=BTC-USDT-SWAP` |
| Candles       | `GET okx.com/api/v5/market/candles?instId=...&bar=1H` |
| CVD source    | `GET fapi.binance.com/fapi/v1/aggTrades?symbol=BTCUSDT` |
| Binance OI    | `GET fapi.binance.com/fapi/v1/openInterest?symbol=BTCUSDT` |

## Run the backend

```bash
cd backend
go mod tidy
go run ./cmd/server
# listens on :8080
# optional: COINS="BTC,ETH,SOL" PORT=8080 go run ./cmd/server
```

Endpoints:
- `GET /api/oi-cache` — full per-coin snapshot table
- `GET /api/signals`  — only coins whose |score| ≥ 20, sorted by strength
- `GET /healthz`

## Run the frontend

```bash
cd frontend
npm install
npm run dev
# opens http://localhost:5173 , proxies /api to :8080
```

## The scoring function

`internal/scorer/scorer.go` reproduces the *spirit* of the weighting you saw
(OI structure ±40, price structure ±15, CVD ±20, funding ±12, crowd ±4).
These weights are **reverse-engineered approximations, not the original values.**
The whole point of self-hosting is that you can backtest and retune them honestly
instead of trusting a black box. Edit the weights, add factors (CHoCH / FVG /
multi-timeframe), and measure hit-rate on your own.

## What's intentionally left as an exercise

- **WebSocket live feed** — current build polls REST on a ticker. For true
  real-time, subscribe to OKX `trades`/`open-interest` and Binance `aggTrade`
  streams and keep a rolling CVD window in memory.
- **Historical OI window** — `oi_chg_1h` here is computed from successive
  snapshots after the process has been running ≥1h. For instant accuracy, pull
  an OI history endpoint or persist snapshots to a DB.
- **The "AI 解讀" text** — feed the structured numbers to an LLM with a fixed
  prompt template if you want the narrative paragraphs. Pure cosmetics over the
  same numbers.
- **Persistence / auth / alerts** — add Postgres, a Telegram bot, etc. as needed.

## Disclaimer

All data is from public exchange APIs and is for research only. The scoring is a
heuristic with self-chosen weights. Nothing here is investment advice.
