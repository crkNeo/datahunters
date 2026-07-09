package cache

import (
	"fmt"
	"time"
)

// apihealth.go: lightweight up/down tracking for external free APIs (GDELT,
// Farside, …). When a source fails several times in a row it fires ONE Telegram
// alert; when it works again it fires a recovery alert. This avoids per-tick spam
// while still telling the admin when an API gets rate-limited / blocked.

const apiFailThreshold = 3 // consecutive failures before alerting

// apiFail records a failure for source. On the Nth consecutive failure it sends a
// single "down" Telegram alert (deduped until recovery). detail is a short reason.
func (s *Store) apiFail(source, detail string) {
	s.rlMu.Lock()
	s.rlFails[source]++
	n := s.rlFails[source]
	shouldAlert := n >= apiFailThreshold && !s.rlDown[source]
	if shouldAlert {
		s.rlDown[source] = true
	}
	s.rlMu.Unlock()
	if shouldAlert && s.notifier.Enabled() {
		go s.notifier.Send(fmt.Sprintf("⚠️ <b>API 異常</b> · %s\n連續 %d 次失敗:%s\n(可能被限流/擋線;恢復後會再通知)", source, n, detail))
	}
}

// apiOK records a success for source, clearing the failure count and firing a
// recovery alert if the source had previously been reported down.
func (s *Store) apiOK(source string) {
	s.rlMu.Lock()
	s.rlFails[source] = 0
	wasDown := s.rlDown[source]
	s.rlDown[source] = false
	s.rlMu.Unlock()
	if wasDown && s.notifier.Enabled() {
		go s.notifier.Send(fmt.Sprintf("✅ <b>API 恢復</b> · %s", source))
	}
}

// BinanceHealthTick alerts (via apiFail/apiOK) when ALL Binance lanes are banned
// / weight-paused (418/429). Call on a ~1-minute ticker; the 3-strike threshold
// means it only fires after Binance is fully unusable for ~3 minutes.
func (s *Store) BinanceHealthTick() {
	if banned, until := s.ex.AllBanned(); banned {
		s.apiFail("Binance API", fmt.Sprintf("全部連線被限流/ban,約 %s 後恢復", until.Round(time.Second)))
	} else {
		s.apiOK("Binance API")
	}
}
