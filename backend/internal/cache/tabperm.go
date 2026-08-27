package cache

import (
	"encoding/json"
	"log"
	"sort"
)

// tabperm.go: admin-editable "which role can see which tab" table.
//
// The frontend hides tabs the caller's role can't reach, but that is cosmetic —
// anyone can call the API directly. So this table is the SINGLE source of truth
// and the API gates on it too (see Server.gateTab). Defaults mirror the roles the
// routes were previously hard-coded with, so an untouched install behaves exactly
// as before.

// TabDef describes one configurable tab for the admin UI.
type TabDef struct {
	Tab    string `json:"tab"`    // frontend mainTab key
	Label  string `json:"label"`  // 中文名
	Role   string `json:"role"`   // 目前生效的最低角色
	Kind   string `json:"kind"`   // 目前生效的類型:info(資訊)/ signal(訊號)
	Locked bool   `json:"locked"` // true: 不允許調整(管理功能)
}

// tabMeta is the canonical tab list: order shown in the admin UI, display label,
// and the default minimum role (= what the code used before this table existed).
var tabMeta = []struct {
	tab, label, def string
	locked          bool
}{
	// 公開
	// tab 名稱必須與前端的 mainTab 值一致(例如「幣種一覽」是 list 不是 coins,
	// 「清算」是 flow 不是 liq),否則前端查不到、會退回備援值。
	{"ranking", "綜合排行", "public", false},
	{"list", "幣種一覽", "public", false},
	{"events", "財經事件", "public", false},
	{"flow", "清算", "public", false},
	{"upbit", "Upbit 公告", "public", false},
	{"news", "市場快訊", "public", false},
	{"funding", "資金費率", "public", false},
	{"unlock", "代幣解鎖", "public", false},
	{"sectors", "板塊強弱", "public", false},
	{"robinhood", "Robinhood", "public", false},
	{"articles", "文章專欄", "public", false},
	// 會員
	{"oi", "OI 儀表板", "member", false},
	{"signals", "量化訊號", "member", false},
	{"radar", "爆發雷達", "member", false},
	{"scorelog", "訊號紀錄", "member", false},
	// VIP
	{"paper", "星軌", "vip", false},
	{"gamble", "超新星", "vip", false},
	{"emaonly", "銀河", "vip", false},
	{"conv", "冥王星", "vip", false},
	{"sr", "支撐壓力", "vip", false},
	// 策略觀察書(預設管理員,可視需要開放給 VIP)
	{"bollfade", "布林重回", "admin", false},
	{"meanrev", "火星", "admin", false},
	{"bgv2", "布乖v2", "admin", false},
	{"bollema", "海王星", "admin", false},
	{"srmtf", "錘頭/射擊星", "admin", false},
	{"ema2155", "2155多", "admin", false},
	{"surge", "爆量脈搏", "admin", false},
	{"pulsar", "脈衝星", "admin", false},
	{"pulsarv2", "脈衝星v2", "admin", false},
	// 管理功能:永遠鎖在 admin,不開放調整
	{"admin", "管理後台", "admin", true},
	{"referral", "推廣管理", "admin", true},
}

// validRoles is the allowed set for a tab's minimum role.
var validRoles = map[string]bool{"public": true, "member": true, "vip": true, "admin": true}

// tabDefaultKind seeds each tab's type. Anything not listed defaults to "info"
// (資訊); listed tabs are "signal" (訊號). Admin can override per tab, same as role.
var tabDefaultKind = map[string]string{
	"signals": "signal", "radar": "signal", "scorelog": "signal",
	"paper": "signal", "gamble": "signal", "emaonly": "signal", "conv": "signal",
	"bollfade": "signal", "meanrev": "signal", "bgv2": "signal", "bollema": "signal",
	"srmtf": "signal", "ema2155": "signal",
	"surge": "signal", "pulsar": "signal", "pulsarv2": "signal",
}

// validKinds is the allowed set for a tab's type.
var validKinds = map[string]bool{"info": true, "signal": true}

// loadTabPerms restores the admin overrides at startup.
func (s *Store) loadTabPerms() {
	m := map[string]string{}
	if s.db != nil {
		if raw := s.db.getConfig("tab_perms"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &m); err != nil {
				log.Printf("tab_perms: bad json, using defaults: %v", err)
				m = map[string]string{}
			}
		}
	}
	s.tabMu.Lock()
	s.tabPerms = m
	s.tabMu.Unlock()
}

// loadTabKinds restores the admin type overrides at startup.
func (s *Store) loadTabKinds() {
	m := map[string]string{}
	if s.db != nil {
		if raw := s.db.getConfig("tab_kinds"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &m); err != nil {
				log.Printf("tab_kinds: bad json, using defaults: %v", err)
				m = map[string]string{}
			}
		}
	}
	s.tabMu.Lock()
	s.tabKinds = m
	s.tabMu.Unlock()
}

// TabKind returns a tab's effective type (info/signal): admin override, else the
// seeded default, else "info".
func (s *Store) TabKind(tab string) string {
	s.tabMu.RLock()
	k, ok := s.tabKinds[tab]
	s.tabMu.RUnlock()
	if ok && validKinds[k] {
		return k
	}
	if d, ok := tabDefaultKind[tab]; ok {
		return d
	}
	return "info"
}

// SetTabKind applies an admin type change. Locked tabs and unknown kinds rejected.
func (s *Store) SetTabKind(tab, kind string) bool {
	if !validKinds[kind] {
		return false
	}
	found := false
	for _, t := range tabMeta {
		if t.tab == tab {
			if t.locked {
				return false // 管理功能不參與資訊/訊號分類
			}
			found = true
			break
		}
	}
	if !found {
		return false
	}
	s.tabMu.Lock()
	s.tabKinds[tab] = kind
	blob, err := json.Marshal(s.tabKinds)
	s.tabMu.Unlock()
	if err == nil && s.db != nil {
		s.db.setConfig("tab_kinds", string(blob))
	}
	return true
}

// VisibleTabKinds returns the tab→type map the frontend nav needs.
func (s *Store) VisibleTabKinds() map[string]string {
	out := make(map[string]string, len(tabMeta))
	for _, t := range tabMeta {
		out[t.tab] = s.TabKind(t.tab)
	}
	return out
}

// TabRole returns a tab's effective minimum role. Unknown tabs are treated as
// admin-only: a new tab is invisible until it's added to tabMeta, which fails
// closed rather than leaking it to everyone.
func (s *Store) TabRole(tab string) string {
	for _, t := range tabMeta {
		if t.tab != tab {
			continue
		}
		if t.locked {
			return t.def
		}
		s.tabMu.RLock()
		r, ok := s.tabPerms[tab]
		s.tabMu.RUnlock()
		if ok && validRoles[r] {
			return r
		}
		return t.def
	}
	return "admin"
}

// SetTabRole applies an admin change. Locked tabs and unknown roles are rejected.
func (s *Store) SetTabRole(tab, role string) bool {
	if !validRoles[role] {
		return false
	}
	found := false
	for _, t := range tabMeta {
		if t.tab == tab {
			if t.locked {
				return false // 管理功能不可降級
			}
			found = true
			break
		}
	}
	if !found {
		return false
	}
	s.tabMu.Lock()
	s.tabPerms[tab] = role
	blob, err := json.Marshal(s.tabPerms)
	s.tabMu.Unlock()
	if err == nil && s.db != nil {
		s.db.setConfig("tab_perms", string(blob))
	}
	return true
}

// TabPerms returns the full table for the admin UI (canonical order).
func (s *Store) TabPerms() []TabDef {
	out := make([]TabDef, 0, len(tabMeta))
	for _, t := range tabMeta {
		out = append(out, TabDef{Tab: t.tab, Label: t.label, Role: s.TabRole(t.tab), Kind: s.TabKind(t.tab), Locked: t.locked})
	}
	return out
}

// VisibleTabs returns the tab→minRole map the frontend needs to decide what to
// render. Public data: it reveals which tabs exist and what they require, not
// their contents.
func (s *Store) VisibleTabs() map[string]string {
	out := make(map[string]string, len(tabMeta))
	for _, t := range tabMeta {
		out[t.tab] = s.TabRole(t.tab)
	}
	return out
}

// tabOfRoute maps an API path to the tab that governs it, so one table drives
// both the nav and the API gate. Keep in sync with Routes().
var tabOfRoute = map[string]string{
	"/api/oi-cache": "oi", "/api/signals": "signals", "/api/radar": "radar",
	"/api/scorelog": "scorelog", "/api/klines": "oi",
	"/api/paper": "paper", "/api/gamble": "gamble", "/api/ema-only": "emaonly",
	"/api/conv": "conv", "/api/sr": "sr",
	"/api/admin/bollfade": "bollfade",
	"/api/admin/meanrev": "meanrev", "/api/admin/bgv2": "bgv2", "/api/admin/bollema": "bollema",
	"/api/srmtf": "srmtf", "/api/ema2155": "ema2155",
	"/api/admin/surge": "surge", "/api/pulsar": "pulsar", "/api/pulsarv2": "pulsarv2",
}

// RouteTabs lists the (route → tab) pairs, sorted, for diagnostics.
func RouteTabs() []string {
	out := make([]string, 0, len(tabOfRoute))
	for k := range tabOfRoute {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
