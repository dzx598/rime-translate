package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Cache 是一个带磁盘持久化的简单 KV 缓存。满了以后随机淘汰（Go map 迭代顺序随机），
// 对输入法这种量级足够，不引入 LRU 依赖。
type Cache struct {
	mu    sync.RWMutex
	m     map[string]string
	max   int
	file  string
	dirty bool
}

func NewCache(file string, max int) *Cache {
	if max <= 0 {
		max = 20000
	}
	return &Cache{m: map[string]string{}, max: max, file: file}
}

func (c *Cache) Get(k string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.m[k]
	return v, ok
}

func (c *Cache) Set(k, v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.m[k]; !exists && len(c.m) >= c.max {
		n := len(c.m) / 10 // 一次淘汰 10%
		for key := range c.m {
			delete(c.m, key)
			n--
			if n <= 0 {
				break
			}
		}
	}
	c.m[k] = v
	c.dirty = true
}

func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.m)
}

func (c *Cache) Load() error {
	if c.file == "" {
		return nil
	}
	b, err := os.ReadFile(c.file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return json.Unmarshal(b, &c.m)
}

func (c *Cache) Save() error {
	if c.file == "" {
		return nil
	}
	c.mu.Lock()
	if !c.dirty {
		c.mu.Unlock()
		return nil
	}
	b, err := json.Marshal(c.m)
	c.dirty = false
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.file), 0o755); err != nil {
		return err
	}
	tmp := c.file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.file)
}

func (c *Cache) StartAutoSave(every time.Duration) (stop func()) {
	t := time.NewTicker(every)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-t.C:
				_ = c.Save()
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { t.Stop(); close(done) }) }
}
