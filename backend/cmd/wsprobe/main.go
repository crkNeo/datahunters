// Command wsprobe verifies Binance futures WebSocket data flow while REST is banned.
//
//	go run ./cmd/wsprobe                       # default btcusdt@aggTrade
//	go run ./cmd/wsprobe -stream !markPrice@arr
package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	stream := flag.String("stream", "btcusdt@aggTrade", "stream name")
	base := flag.String("base", "wss://fstream.binance.com", "ws base")
	flag.Parse()

	url := *base + "/ws/" + *stream
	fmt.Println("connecting:", url)
	c, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		if resp != nil {
			fmt.Printf("dial failed: %v (http %d)\n", err, resp.StatusCode)
		} else {
			fmt.Println("dial failed:", err)
		}
		return
	}
	defer c.Close()
	fmt.Println("connected ✓")

	c.SetReadDeadline(time.Now().Add(15 * time.Second))
	for i := 0; i < 3; i++ {
		_, msg, err := c.ReadMessage()
		if err != nil {
			fmt.Println("read error:", err)
			return
		}
		s := string(msg)
		if len(s) > 220 {
			s = s[:220] + "…"
		}
		fmt.Printf("msg #%d (%d bytes): %s\n", i+1, len(msg), s)
	}
	fmt.Println("OK — data flowing.")
}
