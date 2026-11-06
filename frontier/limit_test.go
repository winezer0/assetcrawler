package frontier

import (
	"net/url"
	"testing"
)

// TestMaxSeen 锁定内存上限：达到上限后拒绝新 URL 并计数，已入队的不受影响。
//
// 已见集合常驻内存且不会缩小，是超大站点上唯一没有上限的增长点。
// 静默截断比报错更危险——结果看起来完整，实际少了一半。
func TestMaxSeen(t *testing.T) {
	f := New(Options{Scope: Any, MaxSeen: 2})
	base, _ := url.Parse("https://example.com/")

	for _, p := range []string{"/a", "/b", "/c", "/d"} {
		f.Push(p, base)
	}
	if got := f.Len(); got != 2 {
		t.Errorf("已见集合 = %d，期望 2", got)
	}
	if got := f.Dropped(); got != 2 {
		t.Errorf("Dropped = %d，期望 2", got)
	}
}

// TestMaxSeenUnlimited 确认 0 表示不限制（默认行为不变）。
func TestMaxSeenUnlimited(t *testing.T) {
	f := New(Options{Scope: Any})
	base, _ := url.Parse("https://example.com/")
	for _, p := range []string{"/a", "/b", "/c"} {
		f.Push(p, base)
	}
	if got := f.Len(); got != 3 {
		t.Errorf("已见集合 = %d，期望 3", got)
	}
	if got := f.Dropped(); got != 0 {
		t.Errorf("Dropped = %d，期望 0", got)
	}
}
