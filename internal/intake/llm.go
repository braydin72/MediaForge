package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gwlsn/shrinkray/internal/config"
)

const llmTimeout = 30 * time.Second

// LLMVerificationResult holds the outcome of an LLM verification pass.
type LLMVerificationResult struct {
	// CandidateID is the 1-based index string returned by the model ("1", "2", "none").
	CandidateID string
	Confidence  float64
	Reasoning   string
	// Disabled is true when the backend is not configured or the API key is absent
	// for cloud providers. Callers should treat this as "skip LLM, use raw score".
	Disabled bool
}

// LLMClient dispatches LLM verification requests to the configured backend.
// It is not safe for concurrent use.
type LLMClient struct {
	cfg        config.LLMConfig
	httpClient *http.Client
}

// NewLLMClient creates an LLMClient. Pass nil for httpClient to use http.DefaultClient.
func NewLLMClient(cfg config.LLMConfig, httpClient *http.Client) *LLMClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &LLMClient{cfg: cfg, httpClient: httpClient}
}

// Verify asks the configured LLM to pick the best candidate for the parsed file.
//
// Returns Disabled:true (no error) when:
//   - cfg.LLM.Backend is empty
//   - Backend is "anthropic" or "openai" and cfg.LLM.APIKey is empty
//
// Returns a low-confidence result (no error) when the model's text response
// cannot be parsed as the expected JSON shape.
//
// Returns an error on HTTP failure, timeout, or non-200 API response.
func (c *LLMClient) Verify(ctx context.Context, parsed *ParsedFilename, candidates []*LookupResult) (*LLMVerificationResult, error) {
	if c.cfg.Backend == "" {
		return &LLMVerificationResult{Disabled: true}, nil
	}
	if c.cfg.Backend != "ollama" && c.cfg.APIKey == "" {
		return &LLMVerificationResult{Disabled: true}, nil
	}

	prompt := buildVerificationPrompt(parsed, candidates)

	ctx, cancel := context.WithTimeout(ctx, llmTimeout)
	defer cancel()

	var text string
	var err error
	switch c.cfg.Backend {
	case "anthropic":
		text, err = c.callAnthropic(ctx, prompt)
	case "openai":
		text, err = c.callOpenAI(ctx, prompt)
	case "ollama":
		text, err = c.callOllama(ctx, prompt)
	default:
		return nil, fmt.Errorf("llm: unknown backend %q", c.cfg.Backend)
	}
	if err != nil {
		return nil, fmt.Errorf("llm %s: %w", c.cfg.Backend, err)
	}

	return parseLLMText(text), nil
}

// --- prompt builder ---

func buildVerificationPrompt(parsed *ParsedFilename, candidates []*LookupResult) string {
	var sb strings.Builder
	sb.WriteString("Identify the best media metadata match for this video file. Return JSON only.\n\n")

	sb.WriteString("File: ")
	sb.WriteString(parsed.Raw)
	sb.WriteString("\nTitle (parsed): ")
	sb.WriteString(parsed.Title)

	if parsed.Year > 0 {
		fmt.Fprintf(&sb, "\nYear: %d", parsed.Year)
	} else {
		sb.WriteString("\nYear: unknown")
	}

	if parsed.IsTV {
		fmt.Fprintf(&sb, "\nType: TV show S%02dE%02d", parsed.Season, parsed.Episode)
	} else {
		sb.WriteString("\nType: Movie")
	}

	sb.WriteString("\n\nCandidates:\n")
	for i, r := range candidates {
		if r.Year > 0 {
			fmt.Fprintf(&sb, "%d. %s (%d) [%s] — confidence: %.0f%%", i+1, r.Title, r.Year, r.Source, r.Confidence*100)
		} else {
			fmt.Fprintf(&sb, "%d. %s [%s] — confidence: %.0f%%", i+1, r.Title, r.Source, r.Confidence*100)
		}
		if r.EpisodeTitle != "" {
			fmt.Fprintf(&sb, "\n   Episode: %q", r.EpisodeTitle)
		}
		sb.WriteString("\n")
	}

	sb.WriteString(`
JSON response (no markdown, no extra text):
{"candidate_id":"1","confidence":0.95,"reasoning":"brief reason"}

If no candidate matches: {"candidate_id":"none","confidence":0.1,"reasoning":"why"}
confidence is 0.0-1.0.`)

	return sb.String()
}

// --- backend callers ---

func (c *LLMClient) callAnthropic(ctx context.Context, prompt string) (string, error) {
	model := c.cfg.Model
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}

	body, _ := json.Marshal(map[string]interface{}{
		"model":      model,
		"max_tokens": 512,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.cfg.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic returned status %d", resp.StatusCode)
	}

	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("failed to decode anthropic response: %w", err)
	}
	for _, block := range out.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("anthropic response contained no text content")
}

func (c *LLMClient) callOpenAI(ctx context.Context, prompt string) (string, error) {
	model := c.cfg.Model
	if model == "" {
		model = "gpt-4o-mini"
	}

	body, _ := json.Marshal(map[string]interface{}{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openai returned status %d", resp.StatusCode)
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("failed to decode openai response: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("openai response contained no choices")
	}
	return out.Choices[0].Message.Content, nil
}

func (c *LLMClient) callOllama(ctx context.Context, prompt string) (string, error) {
	host := c.cfg.OllamaHost
	if host == "" {
		host = "http://localhost:11434"
	}

	body, _ := json.Marshal(map[string]interface{}{
		"model":    c.cfg.Model,
		"stream":   false,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		host+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("failed to decode ollama response: %w", err)
	}
	return out.Message.Content, nil
}

// --- response parsing ---

// parseLLMText extracts the JSON verification result from the model's text.
// When the text cannot be parsed as the expected JSON shape, a graceful
// low-confidence result is returned rather than an error.
func parseLLMText(text string) *LLMVerificationResult {
	cleaned := extractJSON(strings.TrimSpace(text))

	var r struct {
		CandidateID string  `json:"candidate_id"`
		Confidence  float64 `json:"confidence"`
		Reasoning   string  `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(cleaned), &r); err != nil || r.CandidateID == "" {
		return &LLMVerificationResult{
			CandidateID: "none",
			Confidence:  0.3,
			Reasoning:   "LLM returned unparseable response",
		}
	}

	return &LLMVerificationResult{
		CandidateID: r.CandidateID,
		Confidence:  min(max(r.Confidence, 0), 1),
		Reasoning:   r.Reasoning,
	}
}

// extractJSON strips Markdown code fences that models sometimes add around JSON.
func extractJSON(text string) string {
	if !strings.HasPrefix(text, "```") {
		return text
	}
	start := strings.Index(text, "\n")
	end := strings.LastIndex(text, "```")
	if start >= 0 && end > start {
		return strings.TrimSpace(text[start+1 : end])
	}
	return text
}
