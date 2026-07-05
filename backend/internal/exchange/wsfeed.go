package exchange

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WSFeed maintains live Binance USDⓈ-M futures state over a single WebSocket
// connection instead of REST polling — the fix for recurring rate-limit (418)
// bans. It carries mark price + funding (from !markPrice@arr) and rolling 1h
// kline buffers per coin (from <symbol>@kline_1h, seeded once via REST).
//
// NOTE: Open Interest has no WebSocket stream on Binance; it stays on REST and
// must be polled at a low frequency by the caller. An ACTIVE futures ban also
// starves the futures WS (handshake succeeds, no data), so WS prevents FUTURE
// bans rather than bypassing a current one.
type WSFeed struct {
	client *Client
	coins  []string
	klimit int // rolling kline buffer length per coin

	mu        sync.RWMutex
	prices    map[string]float64  // coin -> mark price
	funding   map[string]float64  // coin -> funding rate
	klines    map[string][]Candle // coin -> rolling CLOSED 1h candles (oldest→newest)
	forming   map[string]Candle   // coin -> current (not-yet-closed) 1h bar
	seeded    map[string]bool     // coin -> initial REST history loaded
	connected bool
	lastMsg   time.Time
}

// NewWSFeed builds a feed for the given coins (bare symbols, e.g. "BTC").
func NewWSFeed(client *Client, coins []string, klimit int) *WSFeed {
	if klimit < 60 {
		klimit = 260
	}
	return &WSFeed{
		client:  client,
		coins:   coins,
		klimit:  klimit,
		prices:  map[string]float64{},
		funding: map[string]float64{},
		klines:  map[string][]Candle{},
		forming: map[string]Candle{},
		seeded:  map[string]bool{},
	}
}

// Start launches the seed loop and the WS connection loop (both reconnect on
// failure). Non-blocking.
func (f *WSFeed) Start() {
	go f.seedLoop()
	go f.connLoop()
}

// ---- accessors ----

func (f *WSFeed) Price(coin string) (float64, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	p, ok := f.prices[coin]
	return p, ok && p > 0
}

func (f *WSFeed) Funding(coin string) (float64, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	r, ok := f.funding[coin]
	return r, ok
}

// FundingMap returns a copy of the all-coins funding map.
func (f *WSFeed) FundingMap() map[string]float64 {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make(map[string]float64, len(f.funding))
	for k, v := range f.funding {
		out[k] = v
	}
	return out
}

// Klines returns a copy of the coin's rolling 1h candle buffer (may be empty
// until seeded). Oldest→newest, CLOSED bars only.
func (f *WSFeed) Klines(coin string) []Candle {
	f.mu.RLock()
	defer f.mu.RUnlock()
	src := f.klines[coin]
	out := make([]Candle, len(src))
	copy(out, src)
	return out
}

// KlinesLive returns the closed buffer with the current forming bar appended as
// the last element — matching REST BinanceKlines shape (last bar = in-progress),
// so callers that expect a live last bar (detail/radar) work unchanged.
func (f *WSFeed) KlinesLive(coin string) []Candle {
	f.mu.RLock()
	defer f.mu.RUnlock()
	src := f.klines[coin]
	out := make([]Candle, len(src), len(src)+1)
	copy(out, src)
	if fb, ok := f.forming[coin]; ok && fb.Ts > 0 {
		if len(out) == 0 || fb.Ts > out[len(out)-1].Ts {
			out = append(out, fb)
		}
	}
	return out
}

// Healthy reports whether the feed is connected and received data recently.
func (f *WSFeed) Healthy() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.connected && time.Since(f.lastMsg) < 90*time.Second
}

// ---- seeding (REST, once per coin, retried until it succeeds) ----

// seedLoop fills each coin's kline buffer via REST once; retries every 30s for
// coins not yet seeded (e.g. while a ban is in effect). Low, one-off weight.
func (f *WSFeed) seedLoop() {
	for {
		// only seed once the WS is actually delivering data — on a network where
		// futures WS never streams, this never fetches (no wasted REST / ban risk).
		if !f.Healthy() {
			time.Sleep(5 * time.Second)
			continue
		}
		allSeeded := true
		for _, coin := range f.coins {
			f.mu.RLock()
			done := f.seeded[coin]
			f.mu.RUnlock()
			if done {
				continue
			}
			kl, err := f.client.BinanceKlines(coin+"USDT", "1h", f.klimit)
			if err != nil || len(kl) < 60 {
				allSeeded = false
				continue
			}
			// drop the still-forming last bar so the buffer holds CLOSED bars only
			if len(kl) > 0 {
				kl = kl[:len(kl)-1]
			}
			f.mu.Lock()
			f.klines[coin] = kl
			f.seeded[coin] = true
			f.mu.Unlock()
			time.Sleep(150 * time.Millisecond) // gentle: avoid a seed burst
		}
		if allSeeded {
			return
		}
		time.Sleep(30 * time.Second)
	}
}

// ---- websocket connection ----

func (f *WSFeed) streamURL() string {
	parts := make([]string, 0, len(f.coins)+1)
	parts = append(parts, "!markPrice@arr")
	for _, c := range f.coins {
		parts = append(parts, strings.ToLower(c)+"usdt@kline_1h")
	}
	return "wss://fstream.binance.com/stream?streams=" + strings.Join(parts, "/")
}

func (f *WSFeed) connLoop() {
	backoff := time.Second
	for {
		if err := f.connectOnce(); err != nil {
			f.setConnected(false)
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second // reset after a clean run
	}
}

func (f *WSFeed) connectOnce() error {
	c, _, err := websocket.DefaultDialer.Dial(f.streamURL(), nil)
	if err != nil {
		return err
	}
	defer c.Close()
	f.setConnected(true)
	// Binance closes idle sockets after 24h and pings periodically; gorilla's
	// default handler answers pings. Guard against a dead peer with a read deadline
	// that each message resets.
	c.SetReadDeadline(time.Now().Add(90 * time.Second))
	for {
		_, msg, err := c.ReadMessage()
		if err != nil {
			return err
		}
		c.SetReadDeadline(time.Now().Add(90 * time.Second))
		f.handle(msg)
	}
}

func (f *WSFeed) setConnected(v bool) {
	f.mu.Lock()
	f.connected = v
	f.mu.Unlock()
}

// combined-stream envelope: {"stream":"...","data":<payload>}
type wsEnvelope struct {
	Stream string          `json:"stream"`
	Data   json.RawMessage `json:"data"`
}

func (f *WSFeed) handle(msg []byte) {
	var env wsEnvelope
	if err := json.Unmarshal(msg, &env); err != nil || len(env.Data) == 0 {
		return
	}
	f.mu.Lock()
	f.lastMsg = time.Now()
	f.mu.Unlock()

	switch {
	case env.Stream == "!markPrice@arr":
		f.handleMarkPrice(env.Data)
	case strings.HasSuffix(env.Stream, "@kline_1h"):
		f.handleKline(env.Data)
	}
}

func (f *WSFeed) handleMarkPrice(data json.RawMessage) {
	var arr []struct {
		Symbol  string `json:"s"`
		Mark    string `json:"p"`
		Funding string `json:"r"`
	}
	if json.Unmarshal(data, &arr) != nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range arr {
		coin := coinFromSymbol(e.Symbol)
		if coin == "" {
			continue
		}
		if p, err := strconv.ParseFloat(e.Mark, 64); err == nil && p > 0 {
			f.prices[coin] = p
		}
		if r, err := strconv.ParseFloat(e.Funding, 64); err == nil {
			f.funding[coin] = r
		}
	}
}

func (f *WSFeed) handleKline(data json.RawMessage) {
	var k struct {
		Symbol string `json:"s"`
		K      struct {
			Start    int64  `json:"t"`
			Open     string `json:"o"`
			High     string `json:"h"`
			Low      string `json:"l"`
			Close    string `json:"c"`
			Volume   string `json:"v"`
			Closed   bool   `json:"x"`
			QuoteVol string `json:"q"`
			Trades   int64  `json:"n"`
			TakerBuy string `json:"V"`
		} `json:"k"`
	}
	if json.Unmarshal(data, &k) != nil {
		return
	}
	coin := coinFromSymbol(k.Symbol)
	if coin == "" {
		return
	}
	bar := Candle{
		Ts:       k.K.Start,
		Open:     atofSafe(k.K.Open),
		High:     atofSafe(k.K.High),
		Low:      atofSafe(k.K.Low),
		Close:    atofSafe(k.K.Close),
		Volume:   atofSafe(k.K.Volume),
		TakerBuy: atofSafe(k.K.TakerBuy),
		QuoteVol: atofSafe(k.K.QuoteVol),
		Trades:   float64(k.K.Trades),
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !k.K.Closed {
		f.forming[coin] = bar // track the in-progress bar for KlinesLive
		return
	}
	// bar closed: append to the buffer and clear the forming slot
	buf := f.klines[coin]
	if n := len(buf); n > 0 && buf[n-1].Ts == bar.Ts {
		buf[n-1] = bar // replace if the same bar arrives twice
	} else {
		buf = append(buf, bar)
	}
	if len(buf) > f.klimit {
		buf = buf[len(buf)-f.klimit:]
	}
	f.klines[coin] = buf
	delete(f.forming, coin)
}

func coinFromSymbol(sym string) string {
	if strings.HasSuffix(sym, "USDT") {
		return strings.TrimSuffix(sym, "USDT")
	}
	return ""
}

func atofSafe(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// describe is a tiny helper for logging feed status.
func (f *WSFeed) String() string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return fmt.Sprintf("WSFeed{connected=%v prices=%d klines=%d seeded=%d}",
		f.connected, len(f.prices), len(f.klines), len(f.seeded))
}
