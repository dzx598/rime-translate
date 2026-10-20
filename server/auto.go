package main

import (
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Auto 实现「停止输入一段时间后自动翻译」：
//
//	输入法每次刷新候选都会调用 /auto?q=...
//	  - 已缓存：直接返回译文（200），输入法把它放进候选。
//	  - 未缓存：返回 204，并（重新）开始倒计时；倒计时内再有 /auto 或 /cancel，前一次作废。
//	倒计时结束 -> 翻译 -> 若期间没有新输入，用虚拟键盘按一下刷新键，
//	输入法收到按键后重新组词，再调用 /auto 即命中缓存。
//
// Rime/fcitx5 只在收到按键时刷新候选框，所以「过一会儿自己出现」只能靠这个刷新键。
type Auto struct {
	s     *Server
	delay time.Duration
	tap   func() error // nil 表示没有虚拟键盘：译文要等下一次按键才显示

	mu      sync.Mutex
	gen     uint64
	timer   *time.Timer
	pending string // 正在倒计时的 key；同一输入重复调用不重置倒计时
}

func (a *Auto) Schedule(key, text, from, to string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.timer != nil && a.pending == key {
		return
	}
	a.gen++
	g := a.gen
	if a.timer != nil {
		a.timer.Stop()
	}
	a.pending = key
	a.timer = time.AfterFunc(a.delay, func() { a.fire(g, key, text, from, to) })
}

func (a *Auto) Cancel() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.gen++
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
	a.pending = ""
}

func (a *Auto) current(g uint64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.gen == g
}

func (a *Auto) fire(g uint64, key, text, from, to string) {
	a.mu.Lock()
	if a.gen != g {
		a.mu.Unlock()
		return
	}
	a.timer, a.pending = nil, ""
	a.mu.Unlock()

	if _, ok := a.s.cache.Get(key); !ok {
		c := a.s.translate(key, text, from, to)
		<-c.done
		if c.err != nil {
			return
		}
	}
	if !a.current(g) || a.tap == nil {
		return
	}
	if err := a.tap(); err != nil {
		log.Printf("refresh key: %v", err)
	}
}

func (s *Server) handleAuto(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len([]rune(q)) > s.cfg.MaxRunes {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	from := firstNonEmpty(r.URL.Query().Get("from"), s.cfg.From)
	to := firstNonEmpty(r.URL.Query().Get("to"), s.cfg.To)
	key := from + "|" + to + "|" + q
	if v, ok := s.cache.Get(key); ok {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(v))
		return
	}
	s.auto.Schedule(key, q, from, to)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCancel(w http.ResponseWriter, _ *http.Request) {
	s.auto.Cancel()
	w.WriteHeader(http.StatusNoContent)
}
