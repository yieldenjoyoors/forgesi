// Package llm is the knot's reviewer model client: OpenAI-compatible
// chat completions over the Workers AI REST endpoint (CF_ACCOUNT_ID +
// CLOUDFLARE_API_TOKEN), with a pluggable client for tests.
package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultModel is small, fast, tuned for chat — reviewer verdicts and
// short structured output.
const DefaultModel = "@cf/meta/llama-3.3-70b-instruct-fp8-fast"

// Client completes via Workers AI's OpenAI-compatible endpoint.
type Client struct {
	AccountID string
	Token     string
	Model     string
	HTTP      *http.Client
}

// Complete runs one chat completion: system + user in, assistant text out.
func (c *Client) Complete(system, user string) (string, error) {
	if c.AccountID == "" || c.Token == "" {
		return "", fmt.Errorf("llm: missing CF_ACCOUNT_ID or CLOUDFLARE_API_TOKEN")
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
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/ai/v1/chat/completions", c.AccountID)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
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
