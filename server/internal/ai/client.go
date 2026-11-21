// Package ai talks to a language model for classification, summaries,
// drafts, extraction and embeddings. Supported are OpenAI-compatible
// endpoints (Ollama, LM Studio, vLLM, OpenAI, OpenRouter, Mistral, ...), the
// Anthropic Messages API (plus compatible services) and a ChatGPT
// subscription via "Sign in with ChatGPT" (chatgpt.go). Embeddings always use
// an OpenAI-compatible endpoint.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"postwerk/internal/store"
)

var ErrDisabled = errors.New("ai disabled")

const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"

	anthropicVersion    = "2023-06-01"
	defaultMaxTokens    = 16000
	jsonOnlyInstruction = "\n\nAntworte ausschließlich mit einem einzigen gültigen JSON-Objekt – ohne Markdown-Codeblock und ohne Text davor oder danach."
)

// Provider normalizes the configured provider ("" means OpenAI-compatible).
func Provider(cfg store.AISettings) string {
	switch p := strings.TrimSpace(cfg.Provider); {
	case strings.EqualFold(p, ProviderAnthropic):
		return ProviderAnthropic
	case strings.EqualFold(p, ProviderChatGPT):
		return ProviderChatGPT
	}
	return ProviderOpenAI
}

type Client struct {
	Settings *store.SettingsCache
	HTTP     *http.Client
	Log      *slog.Logger // optional
	// ChatGPTIssuer and ChatGPTBackend override the ChatGPT sign-in service
	// and API (tests); empty means the official endpoints.
	ChatGPTIssuer  string
	ChatGPTBackend string
	// OnChatGPTChange is called after a ChatGPT sign-in, a logout and when
	// the sign-in was lost because the tokens could not be refreshed.
	OnChatGPTChange func(connected bool)

	cg chatGPTState
}

func NewClient(s *store.SettingsCache) *Client {
	return &Client{Settings: s, HTTP: &http.Client{}}
}

func (c *Client) cfg() (store.AISettings, error) {
	cfg := c.Settings.Get().AI
	if !cfg.Enabled || (cfg.BaseURL == "" && Provider(cfg) != ProviderChatGPT) {
		return cfg, ErrDisabled
	}
	return cfg, nil
}

// apiError keeps the HTTP status so callers can react to specific failures.
type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string { return e.Msg }

// Is makes HTTP 429 match ErrRateLimited for every provider.
func (e *apiError) Is(target error) bool {
	return target == ErrRateLimited && e.Status == http.StatusTooManyRequests
}

func (c *Client) do(ctx context.Context, cfg store.AISettings, method, url string, headers map[string]string, body any, out any) error {
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
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
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
		return &apiError{Status: resp.StatusCode, Msg: fmt.Sprintf("ai endpoint HTTP %d: %s", resp.StatusCode, truncate(errorText(data), 160))}
	}
	return json.Unmarshal(data, out)
}

// errorText extracts the message from OpenAI/Anthropic error bodies.
func errorText(data []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
		return printable(e.Error.Message)
	}
	// Anything else is not echoed: the endpoint is configurable and the
	// error text must not reveal arbitrary response bodies.
	return "(keine Fehlermeldung des Anbieters)"
}

// printable keeps a single line of printable characters.
func printable(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// ---- endpoints and headers ----

func openAIURL(base, path string) string { return strings.TrimRight(base, "/") + path }

func openAIHeaders(key string) map[string]string {
	if key == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + key}
}

// anthropicURL accepts the base with or without /v1
// ("https://api.anthropic.com" and "https://api.anthropic.com/v1" both work).
func anthropicURL(base, path string) string {
	b := strings.TrimRight(base, "/")
	if !strings.HasSuffix(b, "/v1") {
		b += "/v1"
	}
	return b + path
}

func anthropicHeaders(key string) map[string]string {
	h := map[string]string{"anthropic-version": anthropicVersion}
	if key != "" {
		h["x-api-key"] = key
	}
	return h
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Chat sends one system + user turn and returns the assistant text.
func (c *Client) Chat(ctx context.Context, system, user string, jsonMode bool) (string, error) {
	cfg, err := c.cfg()
	if err != nil {
		return "", err
	}
	text, err := c.chatProvider(ctx, cfg, system, user, jsonMode)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(stripThinking(text)), nil
}

func (c *Client) chatOpenAI(ctx context.Context, cfg store.AISettings, system, user string, jsonMode bool) (string, error) {
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
	url, headers := openAIURL(cfg.BaseURL, "/chat/completions"), openAIHeaders(cfg.APIKey)
	err := c.do(ctx, cfg, http.MethodPost, url, headers, req, &resp)
	// Some hosted reasoning models only accept their default sampling
	// settings or no response_format: retry once without them.
	var ae *apiError
	if errors.As(err, &ae) && ae.Status == http.StatusBadRequest {
		msg := strings.ToLower(ae.Msg)
		if strings.Contains(msg, "temperature") || strings.Contains(msg, "response_format") {
			delete(req, "temperature")
			if strings.Contains(msg, "response_format") {
				delete(req, "response_format")
			}
			err = c.do(ctx, cfg, http.MethodPost, url, headers, req, &resp)
		}
	}
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("ai endpoint returned no choices")
	}
	return resp.Choices[0].Message.Content, nil
}

func (c *Client) chatAnthropic(ctx context.Context, cfg store.AISettings, system, user string, jsonMode bool) (string, error) {
	if jsonMode {
		system += jsonOnlyInstruction
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	// No sampling parameters and no assistant prefill: current models reject both.
	req := map[string]any{
		"model":      cfg.ChatModel,
		"max_tokens": maxTokens,
		"system":     system,
		"messages":   []chatMsg{{"user", user}},
	}
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := c.do(ctx, cfg, http.MethodPost, anthropicURL(cfg.BaseURL, "/messages"), anthropicHeaders(cfg.APIKey), req, &resp); err != nil {
		return "", err
	}
	if resp.StopReason == "refusal" {
		return "", errors.New("das Modell hat die Anfrage abgelehnt")
	}
	var b strings.Builder
	for _, blk := range resp.Content {
		if blk.Type == "text" {
			b.WriteString(blk.Text)
		}
	}
	if b.Len() == 0 {
		if resp.StopReason == "max_tokens" {
			return "", fmt.Errorf("antwort abgeschnitten: max_tokens (%d) reicht nicht", maxTokens)
		}
		return "", errors.New("ai endpoint returned no text")
	}
	return b.String(), nil
}

// ChatJSON runs Chat in JSON mode and decodes the (possibly fenced) result.
func (c *Client) ChatJSON(ctx context.Context, system, user string, out any) error {
	text, err := c.Chat(ctx, system, user, true)
	if err != nil {
		return err
	}
	return decodeLooseJSON(text, out)
}

// embeddingEndpoint returns the OpenAI-compatible endpoint used for
// embeddings: the dedicated one if set, otherwise the chat endpoint when it
// speaks the OpenAI protocol.
func embeddingEndpoint(cfg store.AISettings) (base, key string, ok bool) {
	if cfg.EmbeddingModel == "" {
		return "", "", false
	}
	if strings.TrimSpace(cfg.EmbeddingBaseURL) != "" {
		return cfg.EmbeddingBaseURL, cfg.EmbeddingAPIKey, true
	}
	if Provider(cfg) == ProviderOpenAI && cfg.BaseURL != "" {
		return cfg.BaseURL, cfg.APIKey, true
	}
	return "", "", false
}

// CanEmbed reports whether embeddings (semantic search) are configured.
func (c *Client) CanEmbed() bool {
	cfg, err := c.cfg()
	if err != nil {
		return false
	}
	_, _, ok := embeddingEndpoint(cfg)
	return ok
}

func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	cfg, err := c.cfg()
	if err != nil {
		return nil, err
	}
	base, key, ok := embeddingEndpoint(cfg)
	if !ok {
		return nil, ErrDisabled
	}
	var resp struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := c.do(ctx, cfg, http.MethodPost, openAIURL(base, "/embeddings"), openAIHeaders(key), map[string]any{"model": cfg.EmbeddingModel, "input": truncate(text, 6000)}, &resp); err != nil {
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

// TestResult describes a connection test of the stored configuration.
type TestResult struct {
	Models         []string // available models (may be empty if the service has no model list)
	ChatOK         bool     // a short test request with the chat model succeeded
	ChatError      string
	EmbeddingOK    bool
	EmbeddingError string // empty when embeddings are not configured or worked
}

// TestConnection checks the stored configuration even while AI features are
// switched off: it lists models and, if a chat model is set, sends a tiny
// test request. Services without a model list still pass via the chat test.
func (c *Client) TestConnection(ctx context.Context) (*TestResult, error) {
	cfg := c.Settings.Get().AI
	if Provider(cfg) == ProviderChatGPT {
		if _, err := c.chatGPTModels(ctx); err != nil {
			return nil, err
		}
		if cfg.ChatModel == "" {
			cfg.ChatModel = ChatGPTDefaultModel
		}
	} else if cfg.BaseURL == "" {
		return nil, errors.New("keine Basis-URL konfiguriert")
	}
	res := &TestResult{Models: []string{}}
	models, listErr := c.models(ctx, cfg)
	if listErr == nil {
		res.Models = models
	}
	if cfg.ChatModel != "" {
		if _, err := c.chatWith(ctx, cfg, "Antworte nur mit OK.", "Test"); err != nil {
			res.ChatError = err.Error()
		} else {
			res.ChatOK = true
		}
	}
	if base, key, ok := embeddingEndpoint(cfg); ok {
		var resp struct {
			Data []struct {
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}
		err := c.do(ctx, cfg, http.MethodPost, openAIURL(base, "/embeddings"), openAIHeaders(key), map[string]any{"model": cfg.EmbeddingModel, "input": "Test"}, &resp)
		if err == nil && (len(resp.Data) == 0 || len(resp.Data[0].Embedding) == 0) {
			err = errors.New("empty embedding")
		}
		if err != nil {
			res.EmbeddingError = err.Error()
		} else {
			res.EmbeddingOK = true
		}
	}
	switch {
	case listErr != nil && !res.ChatOK && res.ChatError != "":
		return nil, errors.New(res.ChatError)
	case listErr != nil && cfg.ChatModel == "":
		return nil, listErr
	}
	return res, nil
}

// chatWith sends a plain (non-JSON) chat with an explicit configuration.
func (c *Client) chatWith(ctx context.Context, cfg store.AISettings, system, user string) (string, error) {
	return c.chatProvider(ctx, cfg, system, user, false)
}

func (c *Client) chatProvider(ctx context.Context, cfg store.AISettings, system, user string, jsonMode bool) (string, error) {
	switch Provider(cfg) {
	case ProviderAnthropic:
		return c.chatAnthropic(ctx, cfg, system, user, jsonMode)
	case ProviderChatGPT:
		return c.chatChatGPT(ctx, cfg, system, user, jsonMode)
	}
	return c.chatOpenAI(ctx, cfg, system, user, jsonMode)
}

func (c *Client) models(ctx context.Context, cfg store.AISettings) ([]string, error) {
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	var err error
	switch Provider(cfg) {
	case ProviderChatGPT:
		return c.chatGPTModels(ctx)
	case ProviderAnthropic:
		err = c.do(ctx, cfg, http.MethodGet, anthropicURL(cfg.BaseURL, "/models?limit=1000"), anthropicHeaders(cfg.APIKey), nil, &resp)
	default:
		err = c.do(ctx, cfg, http.MethodGet, openAIURL(cfg.BaseURL, "/models"), openAIHeaders(cfg.APIKey), nil, &resp)
	}
	if err != nil {
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
