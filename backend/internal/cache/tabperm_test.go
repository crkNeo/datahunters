package cache

import "testing"

func newTabStore() *Store { return &Store{tabPerms: map[string]string{}} }

// Defaults must match the roles the routes were hard-coded with before this
// table existed, or adding the admin panel silently changes who sees what.
func TestTabDefaultsMatchOldHardcodedRoles(t *testing.T) {
	s := newTabStore()
	for tab, want := range map[string]string{
		"ranking": "public", "funding": "public", "sectors": "public",
		"oi": "member", "signals": "member", "radar": "member", "scorelog": "member",
		"paper": "vip", "gamble": "vip", "emaonly": "vip", "conv": "vip", "sr": "vip",
		"bollfade": "admin", "bollema": "admin", "bgv2": "admin",
		"admin": "admin", "referral": "admin",
	} {
		if got := s.TabRole(tab); got != want {
			t.Errorf("%s: default role = %q, want %q", tab, got, want)
		}
	}
}

// An unknown tab must fail CLOSED (admin), never default to public.
func TestUnknownTabFailsClosed(t *testing.T) {
	s := newTabStore()
	if got := s.TabRole("something-new"); got != "admin" {
		t.Errorf("unknown tab = %q, want admin (fail closed)", got)
	}
}

func TestSetTabRole(t *testing.T) {
	s := newTabStore()
	if !s.SetTabRole("conv", "member") {
		t.Fatal("SetTabRole(conv, member) rejected")
	}
	if got := s.TabRole("conv"); got != "member" {
		t.Errorf("after override: %q, want member", got)
	}
	// 管理功能不可降級
	if s.SetTabRole("admin", "public") {
		t.Error("locked tab 'admin' accepted a downgrade")
	}
	if got := s.TabRole("admin"); got != "admin" {
		t.Errorf("locked tab role changed to %q", got)
	}
	if s.SetTabRole("referral", "vip") {
		t.Error("locked tab 'referral' accepted a downgrade")
	}
	// 未知角色 / 未知分頁
	if s.SetTabRole("conv", "superuser") {
		t.Error("invalid role accepted")
	}
	if s.SetTabRole("nope", "vip") {
		t.Error("unknown tab accepted")
	}
	// 前一次的有效設定不該被無效請求覆蓋
	if got := s.TabRole("conv"); got != "member" {
		t.Errorf("conv role clobbered by rejected writes: %q", got)
	}
}

// Every route in tabOfRoute must point at a tab that actually exists, otherwise
// gateTab would resolve it to "admin" and silently lock users out.
func TestRouteTabsAllResolve(t *testing.T) {
	known := map[string]bool{}
	for _, m := range tabMeta {
		known[m.tab] = true
	}
	for route, tab := range tabOfRoute {
		if !known[tab] {
			t.Errorf("route %s maps to unknown tab %q", route, tab)
		}
	}
}

// TabPerms must list every tab, with locked ones flagged for the UI.
func TestTabPermsListing(t *testing.T) {
	s := newTabStore()
	rows := s.TabPerms()
	if len(rows) != len(tabMeta) {
		t.Fatalf("TabPerms returned %d rows, want %d", len(rows), len(tabMeta))
	}
	locked := 0
	for _, r := range rows {
		if r.Label == "" {
			t.Errorf("tab %s has no label", r.Tab)
		}
		if r.Locked {
			locked++
		}
	}
	if locked != 2 { // admin + referral
		t.Errorf("locked tabs = %d, want 2", locked)
	}
}
