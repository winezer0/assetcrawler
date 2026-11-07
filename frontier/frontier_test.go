package frontier

import (
	"net/url"
	"testing"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return u
}

func TestNormalize(t *testing.T) {
	base := mustURL(t, "https://Example.com:443/a/b/index.html")

	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"/x/y", "https://example.com/x/y", true},
		{"../c", "https://example.com/a/c", true},
		{"./d", "https://example.com/a/b/d", true},
		{"https://Other.com/z#frag", "https://other.com/z", true},
		{"http://a.com:80/p", "http://a.com/p", true},
		{"#anchor", "", false},
		{"javascript:void(0)", "", false},
		{"mailto:a@b.com", "", false},
		{"data:image/png;base64,AAA", "", false},
		{"tel:123456", "", false},
		{"", "", false},
		// 相对引用先按 RFC 3986 与 base 合并，再消 dot-segment：
		// /a/b/ + a//b/../c => /a/b/a/b/../c => /a/b/a/c
		{"a//b/../c", "https://example.com/a/b/a/c", true},
	}
	for _, c := range cases {
		got, ok := Normalize(c.in, base)
		if ok != c.ok {
			t.Errorf("Normalize(%q) ok=%v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && got.String() != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got.String(), c.want)
		}
	}
}

func TestNormalizeEmptyPath(t *testing.T) {
	u, ok := Normalize("https://example.com", nil)
	if !ok || u.Path != "/" {
		t.Errorf("got %v, %v; want path=/", u, ok)
	}
}

func TestCleanPath(t *testing.T) {
	cases := map[string]string{
		"/a/./b":    "/a/b",
		"/a/b/../c": "/a/c",
		"/a//b":     "/a/b",
		"/a/b/":     "/a/b/",
		"/":         "/",
		"/../../x":  "/x",
	}
	for in, want := range cases {
		if got := CleanPath(in); got != want {
			t.Errorf("CleanPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRootDomain(t *testing.T) {
	cases := map[string]string{
		"www.example.com":   "example.com",
		"a.b.example.co.uk": "example.co.uk",
		"example.com:8080":  "example.com",
		"127.0.0.1":         "127.0.0.1",
		"localhost":         "localhost",
	}
	for in, want := range cases {
		if got := RootDomain(in); got != want {
			t.Errorf("RootDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPushDeduplicates(t *testing.T) {
	f := New(Options{Scope: Any})

	if _, ok := f.Push("https://example.com/a", nil); !ok {
		t.Fatal("first push should be accepted")
	}
	if _, ok := f.Push("https://Example.com:443/a#frag", nil); ok {
		t.Error("normalized duplicate should be rejected")
	}
	if _, ok := f.Push("https://example.com/a?x=1", nil); !ok {
		t.Error("different query should be a new resource")
	}
	if f.Len() != 2 {
		t.Errorf("Len() = %d, want 2", f.Len())
	}
}

func TestPushRelative(t *testing.T) {
	f := New(Options{Scope: Any})
	base := mustURL(t, "https://example.com/dir/page.html")

	u, ok := f.Push("../api/user", base)
	if !ok {
		t.Fatal("relative path should resolve")
	}
	if want := "https://example.com/api/user"; u.String() != want {
		t.Errorf("got %q, want %q", u.String(), want)
	}
}

func TestScopeSameHost(t *testing.T) {
	f := New(Options{Scope: SameHost})
	f.AddSeed(mustURL(t, "https://example.com/"))

	if f.InScope(mustURL(t, "https://sub.example.com/x")) {
		t.Error("sub.example.com should be out of scope for same-host")
	}
	if !f.InScope(mustURL(t, "https://example.com/y")) {
		t.Error("example.com should be in scope")
	}
}

func TestScopeSubdomain(t *testing.T) {
	f := New(Options{Scope: Subdomain})
	f.AddSeed(mustURL(t, "https://www.example.com/"))

	if !f.InScope(mustURL(t, "https://api.example.com/x")) {
		t.Error("api.example.com should be in scope for subdomain")
	}
	if f.InScope(mustURL(t, "https://evil.com/x")) {
		t.Error("evil.com should be out of scope")
	}
}

func TestScopeAny(t *testing.T) {
	f := New(Options{Scope: Any})
	f.AddSeed(mustURL(t, "https://example.com/"))

	if !f.InScope(mustURL(t, "https://totally-different.com/x")) {
		t.Error("Any scope should allow everything")
	}
}

func TestPushFilteredByScope(t *testing.T) {
	f := New(Options{Scope: SameHost})
	f.AddSeed(mustURL(t, "https://example.com/"))

	if _, ok := f.Push("https://other.com/x", nil); ok {
		t.Error("out-of-scope URL should be rejected")
	}
}

// TestNormalizeRelativeToModuleBase 锁定「相对模块路径」的解析契约：
// routegraph 对 Nuxt 的 "./x.js" 保留原样，由这里以当前 JS 的 URL 解析。
func TestNormalizeRelativeToModuleBase(t *testing.T) {
	base := mustURL(t, "https://nuxt.com/_nuxt/CfoxLZfe.js")
	for raw, want := range map[string]string{
		"./DPFkMy5C.js":     "https://nuxt.com/_nuxt/DPFkMy5C.js",
		"../shared/D4e5.js": "https://nuxt.com/shared/D4e5.js",
		"/assets/x.js":      "https://nuxt.com/assets/x.js",
	} {
		u, ok := Normalize(raw, base)
		if !ok || u.String() != want {
			t.Errorf("Normalize(%q) = (%v, %v)，期望 %s", raw, u, ok, want)
		}
	}
}
