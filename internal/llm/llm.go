// Package llm is the knot's reviewer model client: GLM over the zai
// OpenAI-compatible endpoint (GLM_API_KEY), with a pluggable client for
// tests. Same model family the fleet agent codes with.
package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Defaults mirror the agent's coding endpoint so reviewer and coder
// speak the same model.
const (
	DefaultBaseURL = "https://api.z.ai/api/coding/paas/v4/chat/completions"
	DefaultModel   = "glm-4.7"
)

// Client completes via GLM's OpenAI-compatible endpoint.
type Client struct {
	APIKey  string
	BaseURL string
	Model   string
	HTTP    *http.Client
}

// Complete runs one chat completion: system + user in, assistant text out.
func (c *Client) Complete(system, user string) (string, error) {
	if c.APIKey == "" {
		return "", fmt.Errorf("llm: missing GLM_API_KEY")
	}
	url := c.BaseURL
	if url == "" {
		url = DefaultBaseURL
	}
	model := c.Model
	if model == "" {
		model = DefaultModel
	}
	payload := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "system", "content": system},
			map[string]any{"role": "user", "content": user},
		},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("llm: %d %s", res.StatusCode, raw[:min(len(raw), 200)])
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("llm: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("llm: empty completion")
	}
	return out.Choices[0].Message.Content, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
