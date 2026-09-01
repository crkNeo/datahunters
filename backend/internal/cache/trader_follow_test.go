package cache

import (
	"math"
	"os"
	"testing"
)

// 跟隨追蹤表的記憶體操作(db=nil,不碰 DB / 網路)。
func TestFollowMapLifecycle(t *testing.T) {
	tr := &bitunixTrader{follows: map[string]*followPos{}}
	fp := &followPos{TradeID: "orderblock|SOL|long|1", Acct: "帳1", Symbol: "SOLUSDT", OrigQty: 10}

	tr.putFollow(fp)
	if got := tr.getFollow(fp.TradeID, "帳1"); got == nil || got.Symbol != "SOLUSDT" {
		t.Fatalf("putFollow/getFollow 失敗:%+v", got)
	}
	if tr.getFollow(fp.TradeID, "帳2") != nil {
		t.Fatalf("不同帳號不該共用同一列")
	}
	tr.addFilled(fp.TradeID, "帳1", 0.25)
	tr.addFilled(fp.TradeID, "帳1", 0.25)
	if got := tr.getFollow(fp.TradeID, "帳1"); math.Abs(got.Filled-0.5) > 1e-9 {
		t.Fatalf("addFilled 累加錯:%.4f", got.Filled)
	}
	tr.removeFollow(fp.TradeID, "帳1")
	if tr.getFollow(fp.TradeID, "帳1") != nil {
		t.Fatalf("removeFollow 後仍在")
	}
}

// hasFollow:任一帳號 follow 就為真。
func TestHasFollow(t *testing.T) {
	tr := &bitunixTrader{accts: []*bitunixAccount{{exitMode: "single"}, {exitMode: "single"}}}
	if tr.hasFollow() {
		t.Fatalf("全 single 不該 hasFollow")
	}
	tr.accts[1].exitMode = "follow"
	if !tr.hasFollow() {
		t.Fatalf("有 follow 帳號應 hasFollow")
	}
}

// exitMode 由 env 解析:預設 single,BITUNIX_EXIT_MODE=follow → follow。
func TestBuildAccountExitMode(t *testing.T) {
	a := buildAccount("帳1", "BITUNIX_", "k", "s")
	if a.exitMode != "single" {
		t.Fatalf("預設應 single,got %s", a.exitMode)
	}
	os.Setenv("BITUNIX_2_EXIT_MODE", "follow")
	defer os.Unsetenv("BITUNIX_2_EXIT_MODE")
	b := buildAccount("帳2", "BITUNIX_2_", "k", "s")
	if b.exitMode != "follow" {
		t.Fatalf("BITUNIX_2_EXIT_MODE=follow 應 follow,got %s", b.exitMode)
	}
}

// mirrorLeg/mirrorClose 在未注入掛鉤時是安全空操作(無 follow 帳號 / 未啟用實盤)。
func TestMirrorHooksNilSafe(t *testing.T) {
	exitMirrorLeg, exitMirrorClose = nil, nil
	mirrorLeg(&PaperTrade{}, 0.25) // 不應 panic
	mirrorClose(&PaperTrade{})
}

// floorTo 向下取位(不會超額下單)。
func TestFloorTo(t *testing.T) {
	if got := floorTo(1.23987, 3); math.Abs(got-1.239) > 1e-9 {
		t.Fatalf("floorTo 應向下,got %.5f", got)
	}
}
