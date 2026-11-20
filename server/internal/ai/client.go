// Package ai talks to any OpenAI-compatible endpoint (Ollama, vLLM,
// LocalAI, OpenAI, ...) for classification, summaries, drafts, extraction
// and embeddings.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"postwerk/internal/store"
)

var ErrDisabled = errors.New("ai disabled")

type Client struct {
	Settings *store.SettingsCache
	HTTP     *http.Client
}

func NewClient(s *store.SettingsCache) *Client {
	return &Client{Settings: s, HTTP: &http.Client{}}
}

func (c *Client) cfg() (store.AISettings, error) {
	cfg := c.Settings.Get().AI
	if !cfg.Enabled || cfg.BaseURL == "" {
		return cfg, ErrDisabled
	}
	return cfg, nil
}

func (c *Client) do(ctx context.Context, cfg store.AISettings, method, path string, body any, out any) error {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(cfg.BaseURL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ai endpoint %s: %s", resp.Status, truncate(string(data), 300))
	}
	return json.Unmarshal(data, out)
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Chat sends a chat completion and returns the assistant text.
func (c *Client) Chat(ctx context.Context, system, user string, jsonMode bool) (string, error) {
	cfg, err := c.cfg()
	if err != nil {
		return "", err
	}
	req := map[string]any{
		"model":       cfg.ChatModel,
		"messages":    []chatMsg{{"system", system}, {"user", user}},
		"temperature": 0.2,
		"stream":      false,
	}
	if jsonMode {
		req["response_format"] = map[string]string{"type": "json_object"}
	}
	var resp struct {
		Choices []struct {
			Message chatMsg `json:"message"`
		} `json:"choices"`
	}
	if err := c.do(ctx, cfg, http.MethodPost, "/chat/completions", req, &resp); err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("ai endpoint returned no choices")
	}
	return strings.TrimSpace(stripThinking(resp.Choices[0].Message.Content)), nil
}

// ChatJSON runs Chat in JSON mode and decodes the (possibly fenced) result.
func (c *Client) ChatJSON(ctx context.Context, system, user string, out any) error {
	text, err := c.Chat(ctx, system, user, true)
	if err != nil {
		return err
	}
	return decodeLooseJSON(text, out)
}

func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	cfg, err := c.cfg()
	if err != nil {
		return nil, err
	}
	if cfg.EmbeddingModel == "" {
		return nil, ErrDisabled
	}
	var resp struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := c.do(ctx, cfg, http.MethodPost, "/embeddings", map[string]any{"model": cfg.EmbeddingModel, "input": truncate(text, 6000)}, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 || len(resp.Data[0].Embedding) == 0 {
		return nil, errors.New("empty embedding")
	}
	return resp.Data[0].Embedding, nil
}

func (c *Client) Models(ctx context.Context) ([]string, error) {
	cfg, err := c.cfg()
	if err != nil {
		return nil, err
	}
	return c.models(ctx, cfg)
}

// TestConnection lists models even while AI features are switched off.
func (c *Client) TestConnection(ctx context.Context) ([]string, error) {
	cfg := c.Settings.Get().AI
	if cfg.BaseURL == "" {
		return nil, errors.New("keine Basis-URL konfiguriert")
	}
	return c.models(ctx, cfg)
}

func (c *Client) models(ctx context.Context, cfg store.AISettings) ([]string, error) {
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.do(ctx, cfg, http.MethodGet, "/models", nil, &resp); err != nil {
		return nil, err
	}
	out := []string{}
	for _, m := range resp.Data {
		out = append(out, m.ID)
	}
	return out, nil
}

func stripThinking(s string) string {
	// Reasoning models (e.g. qwen3, deepseek-r1) may emit <think>…</think>.
	for {
		i := strings.Index(s, "<think>")
		j := strings.Index(s, "</think>")
		if i < 0 || j < i {
			return s
		}
		s = s[:i] + s[j+len("</think>"):]
	}
}

func decodeLooseJSON(text string, out any) error {
	t := strings.TrimSpace(text)
	t = strings.TrimPrefix(t, "```json")
	t = strings.TrimPrefix(t, "```")
	t = strings.TrimSuffix(t, "```")
	if i := strings.IndexByte(t, '{'); i > 0 {
		t = t[i:]
	}
	if j := strings.LastIndexByte(t, '}'); j >= 0 && j < len(t)-1 {
		t = t[:j+1]
	}
	if err := json.Unmarshal([]byte(t), out); err != nil {
		return fmt.Errorf("ai returned invalid JSON: %w", err)
	}
	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
