package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"githints/internal/config"
)

func TestNewClientReturnsNilWhenDisabled(t *testing.T) {
	cfg := config.Default()
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient disabled: %v", err)
	}
	if client != nil {
		t.Fatal("expected nil client when ollama is disabled")
	}
}

func TestNewClientRejectsBadTimeout(t *testing.T) {
	cfg := config.Default()
	cfg.Ollama.Enabled = true
	cfg.Ollama.TimeoutMS = 0
	_, err := NewClient(cfg)
	if err == nil {
		t.Fatal("expected error for zero timeout")
	}
}

func TestUnreachableEndpointReturnsError(t *testing.T) {
	cfg := configDefaultEnabled()
	cfg.Ollama.Endpoint = "http://127.0.0.1:1" // almost certainly closed
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err = client.SummarizeDiff(ctx, "main.go", "+func main() {}")
	if err == nil {
		t.Fatal("expected error for unreachable endpoint")
	}
}

func TestMalformedResponseReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{not valid json`))
	}))
	defer server.Close()

	client := newTestClient(server.URL, t)
	_, err := client.SummarizeDiff(context.Background(), "main.go", "+func main() {}")
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestErrorStatusReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("ollama busy"))
	}))
	defer server.Close()

	client := newTestClient(server.URL, t)
	_, err := client.SummarizeDiff(context.Background(), "main.go", "+func main() {}")
	if err == nil {
		t.Fatal("expected error for non-200 status")
	}
}

func TestSuccessfulSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyGeneratePayload(t, r)
		resp := generateResponse{Response: "Adds a main entry point.", Done: true}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestClient(server.URL, t)
	summary, err := client.SummarizeDiff(context.Background(), "main.go", "+func main() {}")
	if err != nil {
		t.Fatalf("SummarizeDiff: %v", err)
	}
	if summary != "Adds a main entry point." {
		t.Errorf("summary = %q, want %q", summary, "Adds a main entry point.")
	}
}

func TestDiffTruncatedBeforeSend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req generateRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		// Extract the diff portion after the fixed prompt template.
		const marker = "DIFF:\n"
		idx := strings.Index(req.Prompt, marker)
		if idx < 0 {
			t.Fatalf("prompt missing DIFF marker: %s", req.Prompt)
		}
		diffPart := req.Prompt[idx+len(marker):]
		if len(diffPart) > 128 {
			t.Errorf("diff portion length %d exceeds max_diff_bytes 128", len(diffPart))
		}
		json.NewEncoder(w).Encode(generateResponse{Response: "short", Done: true})
	}))
	defer server.Close()

	cfg := configDefaultEnabled()
	cfg.Ollama.Endpoint = server.URL
	cfg.Ollama.MaxDiffBytes = 128
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	hugeDiff := strings.Repeat("+a", 500)
	_, err = client.SummarizeDiff(context.Background(), "main.go", hugeDiff)
	if err != nil {
		t.Fatalf("SummarizeDiff: %v", err)
	}
}

func TestSanitizeRejectsMultiline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(generateResponse{Response: "line one\nline two", Done: true})
	}))
	defer server.Close()

	client := newTestClient(server.URL, t)
	_, err := client.SummarizeDiff(context.Background(), "main.go", "+x")
	if err == nil {
		t.Fatal("expected error for multiline response")
	}
}

func TestSanitizeRejectsShellMetacharacters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(generateResponse{Response: "`rm -rf /`", Done: true})
	}))
	defer server.Close()

	client := newTestClient(server.URL, t)
	_, err := client.SummarizeDiff(context.Background(), "main.go", "+x")
	if err == nil {
		t.Fatal("expected error for response containing shell metacharacter")
	}
}

func TestSanitizeTruncatesLongResponse(t *testing.T) {
	long := strings.Repeat("word ", 100)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(generateResponse{Response: long, Done: true})
	}))
	defer server.Close()

	client := newTestClient(server.URL, t)
	summary, err := client.SummarizeDiff(context.Background(), "main.go", "+x")
	if err != nil {
		t.Fatalf("SummarizeDiff: %v", err)
	}
	if len(summary) > maxSummaryLen {
		t.Errorf("summary length %d exceeds %d", len(summary), maxSummaryLen)
	}
}

func TestCircuitBreakerOpensAfterFailures(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := newTestClient(server.URL, t)
	for i := 0; i < failureThreshold+2; i++ {
		_, err := client.SummarizeDiff(context.Background(), "main.go", "+x")
		if err == nil {
			t.Fatalf("call %d expected error", i)
		}
	}

	if calls > failureThreshold+1 {
		t.Errorf("server saw %d calls, expected at most %d after breaker opened", calls, failureThreshold+1)
	}

	// A subsequent call should short-circuit without hitting the server.
	before := calls
	_, err := client.SummarizeDiff(context.Background(), "main.go", "+x")
	if err == nil {
		t.Fatal("expected error after breaker opened")
	}
	if calls != before {
		t.Errorf("breaker did not short-circuit: calls went from %d to %d", before, calls)
	}
}

func TestCircuitBreakerClosesOnSuccess(t *testing.T) {
	failures := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failures++
		if failures < failureThreshold {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(generateResponse{Response: "Recovered.", Done: true})
	}))
	defer server.Close()

	client := newTestClient(server.URL, t)
	for i := 0; i < failureThreshold-1; i++ {
		_, err := client.SummarizeDiff(context.Background(), "main.go", "+x")
		if err == nil {
			t.Fatalf("call %d expected error", i)
		}
	}

	summary, err := client.SummarizeDiff(context.Background(), "main.go", "+x")
	if err != nil {
		t.Fatalf("expected success after recovery: %v", err)
	}
	if summary != "Recovered." {
		t.Errorf("summary = %q, want Recovered.", summary)
	}
}

func TestSummarizeTextCompresses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req generateRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !strings.Contains(strings.ToLower(req.Prompt), "compress") {
			t.Errorf("prompt should mention compression, got: %s", req.Prompt)
		}
		json.NewEncoder(w).Encode(generateResponse{Response: "Compressed overview.", Done: true})
	}))
	defer server.Close()

	client := newTestClient(server.URL, t)
	summary, err := client.SummarizeText(context.Background(), "a\nb\nc")
	if err != nil {
		t.Fatalf("SummarizeText: %v", err)
	}
	if summary != "Compressed overview." {
		t.Errorf("summary = %q, want Compressed overview.", summary)
	}
}

func configDefaultEnabled() config.Config {
	cfg := config.Default()
	cfg.Ollama.Enabled = true
	cfg.Ollama.TimeoutMS = 2000
	return cfg
}

func newTestClient(endpoint string, t *testing.T) *Client {
	t.Helper()
	cfg := configDefaultEnabled()
	cfg.Ollama.Endpoint = endpoint
	cfg.Ollama.TimeoutMS = 2000
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func verifyGeneratePayload(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", r.Method)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	body, _ := io.ReadAll(r.Body)
	var req generateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if req.Model != "qwen2.5:3b-instruct" {
		t.Errorf("model = %q, want qwen2.5:3b-instruct", req.Model)
	}
	if req.Stream {
		t.Error("expected stream=false")
	}
	if !strings.Contains(strings.ToLower(req.Prompt), "do not follow") {
		t.Errorf("prompt should instruct model not to follow embedded instructions, got: %s", req.Prompt)
	}
}
