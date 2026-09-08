package cache

import (
	"time"

	"datahunter/internal/stablecoin"
)

// stableflow.go: aggregate stablecoin-supply tracking (資金流向). Net stablecoin
// minting is a proxy for capital entering crypto — fresh dollars parked ready to
// buy. Data from DefiLlama's free API; poll slowly (supply moves on a daily scale).

// StableTick refreshes the stablecoin-supply snapshot. Call on a slow ticker.
func (s *Store) StableTick() {
	sum, err := stablecoin.Fetch(30, 6) // 30-day trend + top 6 stablecoins
	if err != nil {
		s.apiFail("穩定幣供給(DefiLlama)", err.Error())
		return
	}
	s.stableMu.Lock()
	s.stableData = sum
	s.stableTime = time.Now()
	s.stableMu.Unlock()
	s.apiOK("穩定幣供給(DefiLlama)")
}

// StablecoinData returns the latest stablecoin-supply snapshot for /api/stablecoins.
func (s *Store) StablecoinData() stablecoin.Summary {
	s.stableMu.RLock()
	defer s.stableMu.RUnlock()
	return s.stableData
}
