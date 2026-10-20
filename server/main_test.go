package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

type fake struct {
	name  string
	delay time.Duration
	out   string
	err   error
	calls atomic.Int32
}

func (f *fake) Name() string { return f.name }
func (f *fake) Translate(ctx context.Context, text, from, to string) (string, error) {
	f.calls.Add(1)
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return f.out, f.err
}

func newTestServer(bs ...Backend) *Server {
	cfg := defaultConfig()
	cfg.TimeoutMs = 2000
	return &Server{cfg: cfg, cache: NewCache("", 100), backends: bs, inflight: map[string]*call{}}
}

func get(t *testing.T, h http.HandlerFunc, q string, timeout time.Duration) (int, string, string) {
	t.Helper()
	ts := httptest.NewServer(h)
	defer ts.Close()
	c := &http.Client{Timeout: timeout}
	resp, err := c.Get(ts.URL + "/t?q=" + url.QueryEscape(q))
	if err != nil {
		return 0, "", ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header.Get("X-Cache")
}

func TestFallbackAndCache(t *testing.T) {
	bad := &fake{name: "bad", err: errors.New("boom")}
	good := &fake{name: "good", out: " Hello world \n"}
	s := newTestServer(bad, good)

	code, body, xc := get(t, s.handleTranslate, "你好世界", time.Second)
	if code != 200 || body != "Hello world" || xc != "miss" {
		t.Fatalf("got %d %q %q", code, body, xc)
	}
	code, body, xc = get(t, s.handleTranslate, "你好世界", time.Second)
	if code != 200 || body != "Hello world" || xc != "hit" {
		t.Fatalf("second: got %d %q %q", code, body, xc)
	}
	if good.calls.Load() != 1 {
		t.Fatalf("expected 1 backend call, got %d", good.calls.Load())
	}
}

func TestClientTimeoutStillCaches(t *testing.T) {
	slow := &fake{name: "slow", delay: 400 * time.Millisecond, out: "Slow result"}
	s := newTestServer(slow)

	// 客户端 100ms 就放弃
	if code, _, _ := get(t, s.handleTranslate, "慢", 100*time.Millisecond); code != 0 {
		t.Fatalf("expected client timeout, got %d", code)
	}
	time.Sleep(500 * time.Millisecond)
	code, body, xc := get(t, s.handleTranslate, "慢", time.Second)
	if code != 200 || body != "Slow result" || xc != "hit" {
		t.Fatalf("got %d %q %q", code, body, xc)
	}
}

func TestInflightDedupe(t *testing.T) {
	slow := &fake{name: "slow", delay: 200 * time.Millisecond, out: "X"}
	s := newTestServer(slow)
	done := make(chan struct{})
	for i := 0; i < 5; i++ {
		go func() { get(t, s.handleTranslate, "同一句", time.Second); done <- struct{}{} }()
	}
	for i := 0; i < 5; i++ {
		<-done
	}
	if n := slow.calls.Load(); n != 1 {
		t.Fatalf("expected 1 backend call, got %d", n)
	}
}

func TestAllFail(t *testing.T) {
	s := newTestServer(&fake{name: "a", err: errors.New("x")}, &fake{name: "b", out: ""})
	if code, _, _ := get(t, s.handleTranslate, "失败", time.Second); code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", code)
	}
}

func TestParseGoogle(t *testing.T) {
	raw := []any{[]any{[]any{"Hello ", "你好"}, []any{"world", "世界"}}}
	s, err := parseGoogle(raw)
	if err != nil || s != "Hello world" {
		t.Fatalf("%q %v", s, err)
	}
}
