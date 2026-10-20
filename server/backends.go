package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Backend interface {
	Name() string
	Translate(ctx context.Context, text, from, to string) (string, error)
}

type OpenAIConfig struct {
	BaseURL     string  `json:"base_url"`
	APIKey      string  `json:"api_key"`
	Model       string  `json:"model"`
	Prompt      string  `json:"prompt"`
	Temperature float64 `json:"temperature"`
}

type BaiduConfig struct {
	AppID  string `json:"appid"`
	Secret string `json:"secret"`
}

func newHTTPClient(proxy string) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("bad proxy %q: %w", proxy, err)
		}
		tr.Proxy = http.ProxyURL(u)
	} else {
		tr.Proxy = http.ProxyFromEnvironment
	}
	// 只用 HTTP/1.1：Google gtx 接口会对 Go 的 HTTP/2 客户端指纹稳定返回 429
	tr.ForceAttemptHTTP2 = false
	tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	tr.TLSClientConfig = &tls.Config{NextProtos: []string{"http/1.1"}}
	tr.MaxIdleConnsPerHost = 4
	tr.IdleConnTimeout = 90 * time.Second
	return &http.Client{Transport: tr}, nil
}

func newBackend(name string, cfg Config, c *http.Client) (Backend, error) {
	switch name {
	case "google":
		return &Google{c: c}, nil
	case "openai":
		if cfg.OpenAI.APIKey == "" {
			return nil, errors.New("openai.api_key (or env RIME_TRANSLATE_OPENAI_KEY) is empty")
		}
		return &OpenAI{c: c, cfg: cfg.OpenAI}, nil
	case "baidu":
		if cfg.Baidu.AppID == "" || cfg.Baidu.Secret == "" {
			return nil, errors.New("baidu.appid / baidu.secret is empty")
		}
		return &Baidu{c: c, cfg: cfg.Baidu}, nil
	}
	return nil, fmt.Errorf("unknown backend (want google|openai|baidu)")
}

func doJSON(ctx context.Context, c *http.Client, req *http.Request, out any) error {
	req = req.WithContext(ctx)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode: %w (%s)", err, truncate(string(body), 200))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---------- Google（免 key 的 gtx 接口，国内需走代理） ----------

type Google struct{ c *http.Client }

func (g *Google) Name() string { return "google" }

func googleLang(l string) string {
	switch l {
	case "zh", "zh-cn", "zh-CN", "zh-Hans":
		return "zh-CN"
	case "zh-tw", "zh-TW", "zh-Hant":
		return "zh-TW"
	}
	return l
}

func (g *Google) Translate(ctx context.Context, text, from, to string) (string, error) {
	q := url.Values{}
	q.Set("client", "gtx")
	q.Set("sl", googleLang(from))
	q.Set("tl", googleLang(to))
	q.Set("dt", "t")
	q.Set("q", text)
	req, _ := http.NewRequest(http.MethodGet, "https://translate.googleapis.com/translate_a/single?"+q.Encode(), nil)
	var raw []any
	if err := doJSON(ctx, g.c, req, &raw); err != nil {
		return "", err
	}
	return parseGoogle(raw)
}

func parseGoogle(raw []any) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("empty response")
	}
	segs, ok := raw[0].([]any)
	if !ok {
		return "", errors.New("unexpected response shape")
	}
	var sb strings.Builder
	for _, s := range segs {
		if arr, ok := s.([]any); ok && len(arr) > 0 {
			if t, ok := arr[0].(string); ok {
				sb.WriteString(t)
			}
		}
	}
	return sb.String(), nil
}

// ---------- OpenAI 兼容接口（OpenAI / DeepSeek / 通义 / Ollama 等） ----------

type OpenAI struct {
	c   *http.Client
	cfg OpenAIConfig
}

func (o *OpenAI) Name() string { return "openai" }

var langNames = map[string]string{
	"zh": "Simplified Chinese", "en": "English", "ja": "Japanese", "ko": "Korean",
	"fr": "French", "de": "German", "es": "Spanish", "ru": "Russian",
}

func langName(l string) string {
	if n, ok := langNames[l]; ok {
		return n
	}
	return l
}

func (o *OpenAI) Translate(ctx context.Context, text, from, to string) (string, error) {
	prompt := o.cfg.Prompt
	if prompt == "" {
		prompt = "You are a translation engine embedded in an input method. " +
			"Translate the user's text from {from} to {to}. Use natural, idiomatic phrasing " +
			"suitable for chat and work messages. Output ONLY the translation: no quotes, no notes."
	}
	prompt = strings.NewReplacer("{from}", langName(from), "{to}", langName(to)).Replace(prompt)

	payload := map[string]any{
		"model":       o.cfg.Model,
		"temperature": o.cfg.Temperature,
		"messages": []map[string]string{
			{"role": "system", "content": prompt},
			{"role": "user", "content": text},
		},
	}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, strings.TrimRight(o.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)

	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := doJSON(ctx, o.c, req, &resp); err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("no choices")
	}
	out := strings.TrimSpace(resp.Choices[0].Message.Content)
	out = strings.Trim(out, "\"“”")
	return out, nil
}

// ---------- 百度翻译开放平台 ----------

type Baidu struct {
	c   *http.Client
	cfg BaiduConfig
}

func (b *Baidu) Name() string { return "baidu" }

func (b *Baidu) Translate(ctx context.Context, text, from, to string) (string, error) {
	salt := strconv.Itoa(rand.Intn(1_000_000_000))
	sum := md5.Sum([]byte(b.cfg.AppID + text + salt + b.cfg.Secret))
	form := url.Values{}
	form.Set("q", text)
	form.Set("from", from)
	form.Set("to", to)
	form.Set("appid", b.cfg.AppID)
	form.Set("salt", salt)
	form.Set("sign", hex.EncodeToString(sum[:]))
	req, _ := http.NewRequest(http.MethodPost, "https://fanyi-api.baidu.com/api/trans/vip/translate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var resp struct {
		ErrorCode string `json:"error_code"`
		ErrorMsg  string `json:"error_msg"`
		Result    []struct {
			Dst string `json:"dst"`
		} `json:"trans_result"`
	}
	if err := doJSON(ctx, b.c, req, &resp); err != nil {
		return "", err
	}
	if resp.ErrorCode != "" && resp.ErrorCode != "52000" {
		return "", fmt.Errorf("baidu error %s: %s", resp.ErrorCode, resp.ErrorMsg)
	}
	parts := make([]string, 0, len(resp.Result))
	for _, r := range resp.Result {
		parts = append(parts, r.Dst)
	}
	return strings.Join(parts, "\n"), nil
}
