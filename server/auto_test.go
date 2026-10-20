package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func newAutoServer(b Backend, delay time.Duration) (*Server, *atomic.Int32, *httptest.Server) {
	s := newTestServer(b)
	var taps atomic.Int32
	s.auto = &Auto{s: s, delay: delay, tap: func() error { taps.Add(1); return nil }}
	mux := http.NewServeMux()
	mux.HandleFunc("/auto", s.handleAuto)
	mux.HandleFunc("/cancel", s.handleCancel)
	return s, &taps, httptest.NewServer(mux)
}

func hit(t *testing.T, base, path string) (int, string) {
	t.Helper()
	resp, err := http.Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func auto(q string) string { return "/auto?q=" + url.QueryEscape(q) }

func TestAutoDebounce(t *testing.T) {
	f := &fake{name: "f", out: "EN"}
	_, taps, ts := newAutoServer(f, 60*time.Millisecond)
	defer ts.Close()

	for _, q := range []string{"你", "你好", "你好世"} {
		if code, _ := hit(t, ts.URL, auto(q)); code != http.StatusNoContent {
			t.Fatalf("%s: want 204, got %d", q, code)
		}
		time.Sleep(20 * time.Millisecond) // 小于 delay：前一次被作废
	}
	hit(t, ts.URL, auto("你好世界"))
	time.Sleep(150 * time.Millisecond)

	if n := f.calls.Load(); n != 1 {
		t.Fatalf("want 1 backend call after debounce, got %d", n)
	}
	if n := taps.Load(); n != 1 {
		t.Fatalf("want 1 refresh tap, got %d", n)
	}
	if code, body := hit(t, ts.URL, auto("你好世界")); code != 200 || body != "EN" {
		t.Fatalf("want cached 200 EN, got %d %q", code, body)
	}
	if n := taps.Load(); n != 1 {
		t.Fatalf("cache hit must not tap again, got %d", n)
	}
}

func TestAutoCancel(t *testing.T) {
	f := &fake{name: "f", out: "EN"}
	_, taps, ts := newAutoServer(f, 40*time.Millisecond)
	defer ts.Close()

	hit(t, ts.URL, auto("你好"))
	hit(t, ts.URL, "/cancel")
	time.Sleep(100 * time.Millisecond)
	if f.calls.Load() != 0 || taps.Load() != 0 {
		t.Fatalf("cancelled: want no call/tap, got %d/%d", f.calls.Load(), taps.Load())
	}
}

func TestAutoNoTapIfInputChangedDuringTranslate(t *testing.T) {
	f := &fake{name: "f", out: "EN", delay: 80 * time.Millisecond}
	_, taps, ts := newAutoServer(f, 20*time.Millisecond)
	defer ts.Close()

	hit(t, ts.URL, auto("你好"))
	time.Sleep(50 * time.Millisecond) // 已开始翻译
	hit(t, ts.URL, "/cancel")         // 用户上屏了
	time.Sleep(100 * time.Millisecond)
	if taps.Load() != 0 {
		t.Fatalf("want no tap after cancel during translate, got %d", taps.Load())
	}
	if code, _ := hit(t, ts.URL, auto("你好")); code != 200 {
		t.Fatalf("result should still be cached, got %d", code)
	}
}

func TestAutoSameInputDoesNotResetTimer(t *testing.T) {
	f := &fake{name: "f", out: "EN"}
	_, taps, ts := newAutoServer(f, 60*time.Millisecond)
	defer ts.Close()

	for i := 0; i < 5; i++ { // 共 100ms，超过 delay
		hit(t, ts.URL, auto("你好"))
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if f.calls.Load() != 1 || taps.Load() != 1 {
		t.Fatalf("want 1 call/tap, got %d/%d", f.calls.Load(), taps.Load())
	}
}
