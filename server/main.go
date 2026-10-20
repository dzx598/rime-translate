// rime-translate: 给 Rime（librime-lua）用的本地翻译服务。
//
//	GET /t?q=你好世界[&from=zh&to=en]  -> 200 text/plain "Hello world"
//	GET /auto?q=你好世界                -> 200 译文（已缓存）或 204（开始防抖倒计时，见 auto.go）
//	GET /cancel                         -> 204 取消倒计时（输入已上屏或清空）
//	GET /health                         -> 200 "ok"
//
// 设计要点：
//   - 只监听 127.0.0.1；只依赖标准库。
//   - 多后端按顺序回退（google / openai / baidu）。
//   - 内存 + 磁盘缓存；同一句话的并发请求合并为一次。
//   - 客户端（输入法里的 curl）超时退出后，后台请求继续完成并写入缓存，
//     再按一次热键即可命中缓存——输入法永远不会被慢接口卡住。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Config struct {
	Listen    string   `json:"listen"`
	From      string   `json:"from"`
	To        string   `json:"to"`
	Backends  []string `json:"backends"`
	TimeoutMs int      `json:"timeout_ms"`
	Proxy     string   `json:"proxy"`
	CacheFile string   `json:"cache_file"`
	CacheMax  int      `json:"cache_max"`
	MaxRunes  int      `json:"max_runes"`
	// 停止输入多久后自动翻译；refresh_key 是虚拟键盘按的 Linux 键码（188 = F18），0 表示不按
	AutoDelayMs int          `json:"auto_delay_ms"`
	RefreshKey  int          `json:"refresh_key"`
	OpenAI      OpenAIConfig `json:"openai"`
	Baidu       BaiduConfig  `json:"baidu"`
}

func defaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Listen:      "127.0.0.1:8765",
		From:        "zh",
		To:          "en",
		Backends:    []string{"google"},
		TimeoutMs:   8000,
		CacheFile:   filepath.Join(home, ".cache", "rime-translate", "cache.json"),
		CacheMax:    20000,
		MaxRunes:    500,
		AutoDelayMs: 2000,
		RefreshKey:  188,
		OpenAI: OpenAIConfig{
			BaseURL:     "https://api.openai.com/v1",
			Model:       "gpt-4o-mini",
			Temperature: 0,
		},
	}
}

func loadConfig(path string) (Config, error) {
	cfg := defaultConfig()
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			log.Printf("config %s not found, using defaults", path)
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	// 环境变量优先，避免把密钥写进文件
	if v := os.Getenv("RIME_TRANSLATE_OPENAI_KEY"); v != "" {
		cfg.OpenAI.APIKey = v
	}
	if v := os.Getenv("RIME_TRANSLATE_BAIDU_APPID"); v != "" {
		cfg.Baidu.AppID = v
	}
	if v := os.Getenv("RIME_TRANSLATE_BAIDU_SECRET"); v != "" {
		cfg.Baidu.Secret = v
	}
	if strings.HasPrefix(cfg.CacheFile, "~/") {
		home, _ := os.UserHomeDir()
		cfg.CacheFile = filepath.Join(home, cfg.CacheFile[2:])
	}
	return cfg, nil
}

// ---------- inflight 合并 ----------

type call struct {
	done chan struct{}
	val  string
	err  error
}

type Server struct {
	cfg      Config
	cache    *Cache
	backends []Backend
	auto     *Auto

	mu       sync.Mutex
	inflight map[string]*call
}

func (s *Server) translate(key, text, from, to string) *call {
	s.mu.Lock()
	if c, ok := s.inflight[key]; ok {
		s.mu.Unlock()
		return c
	}
	c := &call{done: make(chan struct{})}
	s.inflight[key] = c
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.inflight, key)
			s.mu.Unlock()
			close(c.done)
		}()
		var errs []string
		for _, b := range s.backends {
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.cfg.TimeoutMs)*time.Millisecond)
			start := time.Now()
			out, err := b.Translate(ctx, text, from, to)
			cancel()
			out = strings.TrimSpace(out)
			if err == nil && out != "" {
				log.Printf("[%s] %dms %q -> %q", b.Name(), time.Since(start).Milliseconds(), text, out)
				c.val = out
				s.cache.Set(key, out)
				return
			}
			if err == nil {
				err = errors.New("empty result")
			}
			log.Printf("[%s] failed: %v", b.Name(), err)
			errs = append(errs, b.Name()+": "+err.Error())
		}
		c.err = errors.New(strings.Join(errs, "; "))
	}()
	return c
}

func (s *Server) handleTranslate(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		http.Error(w, "missing q", http.StatusBadRequest)
		return
	}
	if n := len([]rune(q)); n > s.cfg.MaxRunes {
		http.Error(w, "text too long", http.StatusRequestEntityTooLarge)
		return
	}
	from := firstNonEmpty(r.URL.Query().Get("from"), s.cfg.From)
	to := firstNonEmpty(r.URL.Query().Get("to"), s.cfg.To)
	key := from + "|" + to + "|" + q

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if v, ok := s.cache.Get(key); ok {
		w.Header().Set("X-Cache", "hit")
		fmt.Fprint(w, v)
		return
	}
	c := s.translate(key, q, from, to)
	select {
	case <-c.done:
		if c.err != nil {
			http.Error(w, c.err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("X-Cache", "miss")
		fmt.Fprint(w, c.val)
	case <-r.Context().Done():
		// 客户端超时离开；后台 goroutine 继续跑完并写缓存
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func main() {
	home, _ := os.UserHomeDir()
	cfgPath := flag.String("config", filepath.Join(home, ".config", "rime-translate", "config.json"), "config file")
	flag.Parse()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	client, err := newHTTPClient(cfg.Proxy)
	if err != nil {
		log.Fatal(err)
	}
	var backends []Backend
	for _, name := range cfg.Backends {
		b, err := newBackend(name, cfg, client)
		if err != nil {
			log.Fatalf("backend %q: %v", name, err)
		}
		backends = append(backends, b)
	}
	if len(backends) == 0 {
		log.Fatal("no backends configured")
	}

	cache := NewCache(cfg.CacheFile, cfg.CacheMax)
	if err := cache.Load(); err != nil {
		log.Printf("cache load: %v", err)
	}
	stopSaver := cache.StartAutoSave(30 * time.Second)

	s := &Server{cfg: cfg, cache: cache, backends: backends, inflight: map[string]*call{}}
	s.auto = &Auto{s: s, delay: time.Duration(cfg.AutoDelayMs) * time.Millisecond}
	if cfg.RefreshKey > 0 {
		if vk, err := NewVirtualKey(uint16(cfg.RefreshKey)); err != nil {
			log.Printf("virtual key unavailable (%v): translations appear on the next key press", err)
		} else {
			s.auto.tap = vk.Tap
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/t", s.handleTranslate)
	mux.HandleFunc("/auto", s.handleAuto)
	mux.HandleFunc("/cancel", s.handleCancel)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") })
	srv := &http.Server{Addr: cfg.Listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		<-ch
		stopSaver()
		if err := cache.Save(); err != nil {
			log.Printf("cache save: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	names := make([]string, len(backends))
	for i, b := range backends {
		names[i] = b.Name()
	}
	log.Printf("rime-translate listening on %s, backends=%v, cache=%d entries", cfg.Listen, names, cache.Len())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
