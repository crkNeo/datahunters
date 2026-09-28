package cache

import "testing"

// topicOn: explicit choice wins; otherwise the topic's default (currently all on).
func TestTopicOnDefaults(t *testing.T) {
	cases := []struct {
		prefs map[string]bool
		key   string
		want  bool
	}{
		{nil, "msg:news", true},                                   // default-on, no choice
		{nil, "msg:whales", true},                                 // default-on, no choice
		{nil, "msg:polyscout", true},                              // default-on, no choice
		{map[string]bool{"msg:whales": false}, "msg:whales", false}, // opted out
		{map[string]bool{"msg:news": false}, "msg:news", false},     // opted out
	}
	for _, c := range cases {
		if got := topicOn(c.prefs, c.key); got != c.want {
			t.Errorf("topicOn(%v,%q)=%v want %v", c.prefs, c.key, got, c.want)
		}
	}
}

// stratTopic maps book names (incl. per-timeframe orderblock) to their topic keys.
func TestStratTopic(t *testing.T) {
	for book, want := range map[string]string{
		"main":            "strat:paper",
		"conv":            "strat:conv",
		"orderblock":      "strat:orderblock",
		"orderblock_4h":   "strat:orderblock_4h",
		"orderblockv2_4h": "strat:orderblockv2_4h",
	} {
		if got := stratTopic(book); got != want {
			t.Errorf("stratTopic(%q)=%q want %q", book, got, want)
		}
	}
}

// A user only sees topics their role can reach; the two orderblock timeframes are
// distinct toggles; an unknown topic fails closed (admin).
func TestPushTopicVisibilityByRole(t *testing.T) {
	s := newTabStore() // defaults: sr=vip, polyscout=admin, news/whales=public, orderblock=admin
	has := func(list []PushTopic, key string) bool {
		for _, p := range list {
			if p.Key == key {
				return true
			}
		}
		return false
	}
	member := s.PushTopicsFor("member", "")
	if !has(member, "msg:news") {
		t.Error("member should see public topic msg:news")
	}
	if has(member, "msg:sr") {
		t.Error("member must NOT see vip topic msg:sr")
	}
	if has(member, "msg:polyscout") {
		t.Error("member must NOT see admin topic msg:polyscout")
	}
	admin := s.PushTopicsFor("admin", "")
	if !has(admin, "strat:orderblock") || !has(admin, "strat:orderblock_4h") {
		t.Error("admin should see both orderblock timeframes as separate topics")
	}
	if s.topicMinRole("msg:does-not-exist") != "admin" {
		t.Error("unknown topic must fail closed to admin")
	}
}

// booksForTF narrows a multi-timeframe strategy to the requested timeframe.
func TestBooksForTF(t *testing.T) {
	if got := booksForTF("orderblock", "4h"); len(got) != 1 || got[0] != "orderblock_4h" {
		t.Errorf("booksForTF(orderblock,4h)=%v want [orderblock_4h]", got)
	}
	if got := booksForTF("orderblock", ""); len(got) != 2 {
		t.Errorf("booksForTF(orderblock,\"\")=%v want both", got)
	}
	if got := booksForTF("conv", "1h"); len(got) != 1 || got[0] != "conv" {
		t.Errorf("booksForTF(conv,1h)=%v want [conv] (single-tf ignores filter)", got)
	}
}
