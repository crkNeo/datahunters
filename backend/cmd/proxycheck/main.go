// Command proxycheck verifies a proxy is usable for Binance futures BEFORE you
// put it in BINANCE_PROXIES: it prints the exit IP (which IP Binance will see)
// and whether Binance futures REST is reachable / region-blocked / banned
// through it. Also accepts "direct" to check your own IP.
//
//	go run ./cmd/proxycheck -proxy socks5://127.0.0.1:1080
//	go run ./cmd/proxycheck -proxy http://user:pass@1.2.3.4:8080
//	go run ./cmd/proxycheck -proxy direct
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func client(proxy string) (*http.Client, error) {
	c := &http.Client{Timeout: 12 * time.Second}
	if proxy == "" || proxy == "direct" {
		return c, nil
	}
	pu, err := url.Parse(proxy)
	if err != nil {
		return nil, err
	}
	c.Transport = &http.Transport{Proxy: http.ProxyURL(pu)}
	return c, nil
}

func get(c *http.Client, url string) (int, string, http.Header) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := c.Do(req)
	if err != nil {
		return -1, "err: " + err.Error(), nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
	return resp.StatusCode, strings.TrimSpace(string(b)), resp.Header
}

func main() {
	proxy := flag.String("proxy", "direct", "proxy URL (socks5://.. / http://..) or 'direct'")
	flag.Parse()

	c, err := client(*proxy)
	if err != nil {
		fmt.Println("bad proxy url:", err)
		return
	}
	fmt.Printf("=== 檢查 %s ===\n", *proxy)

	// 1) exit IP — what Binance will see
	if code, body, _ := get(c, "https://api.ipify.org?format=text"); code == 200 {
		fmt.Printf("出口 IP        : %s\n", body)
	} else {
		fmt.Printf("出口 IP        : 取得失敗 (%d %s) — proxy 可能不通\n", code, body)
	}

	// 2) Binance futures reachability + region / ban check
	code, body, hdr := get(c, "https://fapi.binance.com/fapi/v1/ping")
	switch {
	case code == 200:
		w := ""
		if hdr != nil {
			w = hdr.Get("X-Mbx-Used-Weight-1m")
		}
		fmt.Printf("Binance 期貨   : ✅ 可用 (200)%s\n", func() string {
			if w != "" {
				return "  已用權重 " + w + "/2400"
			}
			return ""
		}())
	case code == 451:
		fmt.Printf("Binance 期貨   : ❌ 地區封鎖 (451) — 這個 IP 在 Binance 禁區(如美國),不能用\n")
	case code == 418 || code == 429:
		fmt.Printf("Binance 期貨   : ⚠️ 此 IP 已被限流 (%d) — %.60s\n", code, body)
	default:
		fmt.Printf("Binance 期貨   : ❌ 異常 (%d) %.60s\n", code, body)
	}

	fmt.Println("\n判讀:出口 IP 正常 + Binance 200 = 這條可放進 BINANCE_PROXIES。")
	fmt.Println("      451 = 換非美國/非禁區的 IP;proxy 不通 = 檢查 proxy 位址/帳密。")
}
