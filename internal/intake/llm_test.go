package intake

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/braydin72/mediaforge/internal/config"
)

// newMockLLMClient builds an LLMClient backed by a roundTripFunc mock.
// roundTripFunc and jsonResp are defined in tvdb_test.go (same package).
func newMockLLMClient(backend, apiKey string, fn roundTripFunc) *LLMClient {
	return NewLLMClient(config.LLMConfig{
		Backend:    backend,
		APIKey:     apiKey,
		Model:      "test-model",
		OllamaHost: "http://localhost:11434",
	}, &http.Client{Transport: fn})
}

var (
	llmTestParsed = &ParsedFilename{
		Raw:   "Breaking.Bad.S01E01.2008.mkv",
		Title: "Breaking Bad",
		Year:  2008,
		IsTV:  true, Season: 1, Episode: 1,
	}
	llmTestCandidates = []*LookupResult{
		{Source: "tvdb", MediaType: "tv", Title: "Breaking Bad", Year: 2008,
			EpisodeTitle: "Pilot", Confidence: 0.85},
	}
	llmSuccessJSON = `{"candidate_id":"1","confidence":0.95,"reasoning":"Exact title and episode match"}`
)

// anthropicResp wraps a text string in the Anthropic messages response envelope.
func anthropicResp(text string) map[string]interface{} {
	return map[string]interface{}{
		"content": []map[string]interface{}{
			{"type": "text", "text": text},
		},
	}
}

// openAIResp wraps a text string in the OpenAI chat completions response envelope.
func openAIResp(text string) map[string]interface{} {
	return map[string]interface{}{
		"choices": []map[string]interface{}{
			{"message": map[string]string{"content": text}},
		},
	}
}

// ollamaResp wraps a text string in the Ollama chat response envelope.
func ollamaResp(text string) map[string]interface{} {
	return map[string]interface{}{
		"message": map[string]string{"content": text},
	}
}

func TestLLMVerify_AnthropicSuccess(t *testing.T) {
	client := newMockLLMClient("anthropic", "sk-test", func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("x-api-key") != "sk-test" {
			t.Errorf("x-api-key header: want %q, got %q", "sk-test", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("anthropic-version header must be set")
		}
		return jsonResp(http.StatusOK, anthropicResp(llmSuccessJSON)), nil
	})

	result, err := client.Verify(context.Background(), llmTestParsed, llmTestCandidates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Disabled {
		t.Error("result should not be Disabled")
	}
	if result.CandidateID != "1" {
		t.Errorf("CandidateID: want %q, got %q", "1", result.CandidateID)
	}
	if !approxEqual(result.Confidence, 0.95) {
		t.Errorf("Confidence: want ~0.95, got %f", result.Confidence)
	}
	if result.Reasoning != "Exact title and episode match" {
		t.Errorf("Reasoning: want %q, got %q", "Exact title and episode match", result.Reasoning)
	}
}

func TestLLMVerify_OpenAISuccess(t *testing.T) {
	client := newMockLLMClient("openai", "sk-test", func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("Authorization header: want %q, got %q", "Bearer sk-test", r.Header.Get("Authorization"))
		}
		return jsonResp(http.StatusOK, openAIResp(llmSuccessJSON)), nil
	})

	result, err := client.Verify(context.Background(), llmTestParsed, llmTestCandidates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.CandidateID != "1" {
		t.Errorf("CandidateID: want %q, got %q", "1", result.CandidateID)
	}
	if !approxEqual(result.Confidence, 0.95) {
		t.Errorf("Confidence: want ~0.95, got %f", result.Confidence)
	}
}

func TestLLMVerify_OllamaSuccess(t *testing.T) {
	cfg := config.LLMConfig{
		Backend:    "ollama",
		APIKey:     "", // Ollama needs no API key
		Model:      "llama3",
		OllamaHost: "http://mock-ollama",
	}
	client := NewLLMClient(cfg, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "mock-ollama" {
			t.Errorf("unexpected host %q, want mock-ollama", r.URL.Host)
		}
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected path %q, want /api/chat", r.URL.Path)
		}
		return jsonResp(http.StatusOK, ollamaResp(llmSuccessJSON)), nil
	})})

	result, err := client.Verify(context.Background(), llmTestParsed, llmTestCandidates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Disabled {
		t.Error("Ollama should work without an API key")
	}
	if result.CandidateID != "1" {
		t.Errorf("CandidateID: want %q, got %q", "1", result.CandidateID)
	}
}

func TestLLMVerify_MalformedJSONHandled(t *testing.T) {
	client := newMockLLMClient("anthropic", "sk-test", func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, anthropicResp("I cannot determine the correct media for this file.")), nil
	})

	result, err := client.Verify(context.Background(), llmTestParsed, llmTestCandidates)
	if err != nil {
		t.Fatalf("malformed JSON should not cause an error, got: %v", err)
	}
	if result.Confidence != 0.3 {
		t.Errorf("Confidence: want 0.3 for unparseable response, got %f", result.Confidence)
	}
	if result.Reasoning != "LLM returned unparseable response" {
		t.Errorf("Reasoning: want %q, got %q", "LLM returned unparseable response", result.Reasoning)
	}
}

func TestLLMVerify_MarkdownWrappedJSONHandled(t *testing.T) {
	wrapped := "```json\n" + llmSuccessJSON + "\n```"
	client := newMockLLMClient("openai", "sk-test", func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, openAIResp(wrapped)), nil
	})

	result, err := client.Verify(context.Background(), llmTestParsed, llmTestCandidates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.CandidateID != "1" {
		t.Errorf("CandidateID: want %q, got %q — markdown stripping failed", "1", result.CandidateID)
	}
}

func TestLLMVerify_TimeoutHandled(t *testing.T) {
	client := newMockLLMClient("anthropic", "sk-test", func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done() // block until the context is cancelled
		return nil, r.Context().Err()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := client.Verify(ctx, llmTestParsed, llmTestCandidates)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestLLMVerify_DisabledBackend(t *testing.T) {
	requestMade := false
	client := NewLLMClient(config.LLMConfig{Backend: ""}, &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			requestMade = true
			return jsonResp(http.StatusOK, nil), nil
		}),
	})

	result, err := client.Verify(context.Background(), llmTestParsed, llmTestCandidates)
	if err != nil {
		t.Fatalf("disabled backend should not return an error, got: %v", err)
	}
	if !result.Disabled {
		t.Error("result.Disabled should be true when backend is empty")
	}
	if requestMade {
		t.Error("no HTTP request should be made when backend is disabled")
	}
}

func TestLLMVerify_EmptyAPIKeyDisables(t *testing.T) {
	for _, backend := range []string{"anthropic", "openai"} {
		t.Run(backend, func(t *testing.T) {
			requestMade := false
			client := newMockLLMClient(backend, "" /* empty key */, func(r *http.Request) (*http.Response, error) {
				requestMade = true
				return jsonResp(http.StatusOK, nil), nil
			})
			result, err := client.Verify(context.Background(), llmTestParsed, llmTestCandidates)
			if err != nil {
				t.Fatalf("empty API key should not return an error, got: %v", err)
			}
			if !result.Disabled {
				t.Errorf("%s: result.Disabled should be true when API key is empty", backend)
			}
			if requestMade {
				t.Errorf("%s: no request should be made with empty API key", backend)
			}
		})
	}
}

func TestLLMVerify_OllamaNoKeyNeeded(t *testing.T) {
	cfg := config.LLMConfig{
		Backend:    "ollama",
		APIKey:     "", // intentionally empty
		Model:      "llama3",
		OllamaHost: "http://localhost:11434",
	}
	client := NewLLMClient(cfg, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, ollamaResp(llmSuccessJSON)), nil
	})})

	result, err := client.Verify(context.Background(), llmTestParsed, llmTestCandidates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Disabled {
		t.Error("Ollama should not be disabled when API key is absent")
	}
}

func TestLLMVerify_UnknownBackendErrors(t *testing.T) {
	client := newMockLLMClient("ollmm", "key", func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, nil), nil
	})
	_, err := client.Verify(context.Background(), llmTestParsed, llmTestCandidates)
	if err == nil {
		t.Fatal("unknown backend should return an error")
	}
}

// --- parseLLMText unit tests ---

func TestParseLLMText_Valid(t *testing.T) {
	result := parseLLMText(`{"candidate_id":"2","confidence":0.80,"reasoning":"Good match"}`)
	if result.CandidateID != "2" {
		t.Errorf("CandidateID: want %q, got %q", "2", result.CandidateID)
	}
	if !approxEqual(result.Confidence, 0.80) {
		t.Errorf("Confidence: want ~0.80, got %f", result.Confidence)
	}
	if result.Reasoning != "Good match" {
		t.Errorf("Reasoning: want %q, got %q", "Good match", result.Reasoning)
	}
}

func TestParseLLMText_Malformed(t *testing.T) {
	cases := []string{
		"not json at all",
		`{"candidate_id":""}`, // empty ID treated as malformed
		`{}`,
		"",
	}
	for _, c := range cases {
		result := parseLLMText(c)
		if result.Confidence != 0.3 {
			t.Errorf("parseLLMText(%q): want confidence 0.3, got %f", c, result.Confidence)
		}
		if result.Reasoning != "LLM returned unparseable response" {
			t.Errorf("parseLLMText(%q): unexpected reasoning %q", c, result.Reasoning)
		}
	}
}

func TestParseLLMText_ConfidenceClamped(t *testing.T) {
	high := parseLLMText(`{"candidate_id":"1","confidence":1.5,"reasoning":"overconfident"}`)
	if high.Confidence > 1.0 {
		t.Errorf("confidence should be clamped to 1.0, got %f", high.Confidence)
	}
	low := parseLLMText(`{"candidate_id":"1","confidence":-0.5,"reasoning":"negative"}`)
	if low.Confidence < 0 {
		t.Errorf("confidence should be clamped to 0, got %f", low.Confidence)
	}
}

func TestParseLLMText_MarkdownStripped(t *testing.T) {
	wrapped := "```json\n{\"candidate_id\":\"1\",\"confidence\":0.9,\"reasoning\":\"ok\"}\n```"
	result := parseLLMText(wrapped)
	if result.CandidateID != "1" {
		t.Errorf("markdown wrapping should be stripped: got CandidateID=%q", result.CandidateID)
	}
}

func TestBuildVerificationPrompt_ContainsKeyInfo(t *testing.T) {
	parsed := &ParsedFilename{
		Raw: "Breaking.Bad.S01E01.2008.mkv", Title: "Breaking Bad",
		Year: 2008, IsTV: true, Season: 1, Episode: 1,
	}
	candidates := []*LookupResult{
		{Title: "Breaking Bad", Year: 2008, Source: "tvdb", EpisodeTitle: "Pilot", Confidence: 0.85},
	}

	prompt := buildVerificationPrompt(parsed, candidates)

	for _, want := range []string{"Breaking Bad", "2008", "S01E01", "tvdb", "Pilot"} {
		if !containsSubstring(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
