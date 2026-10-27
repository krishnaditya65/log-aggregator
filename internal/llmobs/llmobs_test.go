package llmobs

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWrapOpenAIStyle(t *testing.T) {
	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Hello there"}]}`
	respBody := `{"id":"cc-1","choices":[{"message":{"role":"assistant","content":"Hi! How can I help?"}}],"usage":{"prompt_tokens":10,"completion_tokens":8,"total_tokens":18}}`

	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(respBody)),
			Header:     make(http.Header),
		}, nil
	})

	pricing := PricingTable{
		"openai:gpt-4o": {PromptPer1K: 5.0, CompletionPer1K: 15.0},
	}

	var got LLMCall
	transport := WrapWithPricing(inner, "openai", func(c LLMCall) { got = c }, pricing)
	client := &http.Client{Transport: transport}

	req, _ := http.NewRequest("POST", "http://example.invalid/v1/chat/completions", strings.NewReader(reqBody))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()

	if got.Provider != "openai" {
		t.Fatalf("expected provider openai, got %q", got.Provider)
	}
	if got.Model != "gpt-4o" {
		t.Fatalf("expected model gpt-4o, got %q", got.Model)
	}
	if got.Prompt != "Hello there" {
		t.Fatalf("expected prompt captured, got %q", got.Prompt)
	}
	if got.Completion != "Hi! How can I help?" {
		t.Fatalf("expected completion captured, got %q", got.Completion)
	}
	if got.PromptTokens != 10 || got.CompletionTokens != 8 || got.TotalTokens != 18 {
		t.Fatalf("unexpected token counts: %+v", got)
	}
	wantCost := (10.0/1000.0)*5.0 + (8.0/1000.0)*15.0
	if got.CostUSD != wantCost {
		t.Fatalf("expected cost %.6f, got %.6f", wantCost, got.CostUSD)
	}
	if got.StatusCode != 200 {
		t.Fatalf("expected status 200, got %d", got.StatusCode)
	}

	// Body must still be readable downstream after wrapping.
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Hi! How can I help?") {
		t.Fatalf("expected response body preserved for downstream consumers")
	}
}

func TestWrapAnthropicStyle(t *testing.T) {
	reqBody := `{"model":"claude-opus-4-6","messages":[{"role":"user","content":"Summarize this."}]}`
	respBody := `{"id":"msg_1","type":"message","content":[{"type":"text","text":"Here is a summary."}],"usage":{"input_tokens":20,"output_tokens":12}}`

	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(respBody)),
			Header:     make(http.Header),
		}, nil
	})

	var got LLMCall
	transport := Wrap(inner, "anthropic", func(c LLMCall) { got = c })
	client := &http.Client{Transport: transport}

	req, _ := http.NewRequest("POST", "http://example.invalid/v1/messages", strings.NewReader(reqBody))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()

	if got.Model != "claude-opus-4-6" {
		t.Fatalf("expected model claude-opus-4-6, got %q", got.Model)
	}
	if got.Prompt != "Summarize this." {
		t.Fatalf("expected prompt captured, got %q", got.Prompt)
	}
	if got.Completion != "Here is a summary." {
		t.Fatalf("expected completion captured, got %q", got.Completion)
	}
	if got.PromptTokens != 20 || got.CompletionTokens != 12 || got.TotalTokens != 32 {
		t.Fatalf("unexpected token counts: %+v", got)
	}
}

func TestWrapAnthropicErrorResponse(t *testing.T) {
	reqBody := `{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hi"}]}`
	respBody := `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`

	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 529,
			Status:     "529 Overloaded",
			Body:       io.NopCloser(strings.NewReader(respBody)),
			Header:     make(http.Header),
		}, nil
	})

	var got LLMCall
	transport := Wrap(inner, "anthropic", func(c LLMCall) { got = c })
	client := &http.Client{Transport: transport}
	req, _ := http.NewRequest("POST", "http://example.invalid/v1/messages", strings.NewReader(reqBody))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()

	if got.Error != "Overloaded" {
		t.Fatalf("expected error 'Overloaded', got %q", got.Error)
	}
}

func TestWrapGenericFallback(t *testing.T) {
	reqBody := `not even json`
	respBody := `also not json {{{`

	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(respBody)),
			Header:     make(http.Header),
		}, nil
	})

	var got LLMCall
	transport := Wrap(inner, "custom-provider", func(c LLMCall) { got = c })
	client := &http.Client{Transport: transport}
	req, _ := http.NewRequest("POST", "http://example.invalid/v1/whatever", strings.NewReader(reqBody))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()

	if got.Provider != "custom-provider" {
		t.Fatalf("expected provider set even in fallback, got %q", got.Provider)
	}
	if got.StatusCode != 200 {
		t.Fatalf("expected status captured in fallback, got %d", got.StatusCode)
	}
	if got.Model != "" || got.Prompt != "" || got.TotalTokens != 0 {
		t.Fatalf("expected no structured fields extracted in fallback, got %+v", got)
	}
	if got.LatencyMS < 0 {
		t.Fatalf("expected non-negative latency")
	}
}

func TestPricingTableEstimateCost(t *testing.T) {
	pt := PricingTable{
		"openai:gpt-4o":    {PromptPer1K: 5.0, CompletionPer1K: 15.0},
		"generic-fallback": {PromptPer1K: 1.0, CompletionPer1K: 2.0},
	}

	cost := pt.EstimateCost("openai", "gpt-4o", 1000, 1000)
	if cost != 20.0 {
		t.Fatalf("expected cost 20.0, got %.4f", cost)
	}

	// Falls back to plain model key when provider:model not present.
	cost2 := pt.EstimateCost("someprovider", "generic-fallback", 1000, 500)
	want2 := 1.0 + 0.5*2.0
	if cost2 != want2 {
		t.Fatalf("expected cost %.4f, got %.4f", want2, cost2)
	}

	// Unknown model -> 0.
	if pt.EstimateCost("openai", "unknown-model", 100, 100) != 0 {
		t.Fatalf("expected 0 cost for unknown model")
	}
}

func TestTruncatePromptCompletion(t *testing.T) {
	long := strings.Repeat("x", MaxPromptChars+500)
	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"` + long + `"}]}`

	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(`{"choices":[],"usage":{}}`)),
			Header:     make(http.Header),
		}, nil
	})

	var got LLMCall
	transport := Wrap(inner, "openai", func(c LLMCall) { got = c })
	client := &http.Client{Transport: transport}
	req, _ := http.NewRequest("POST", "http://example.invalid/v1/chat/completions", strings.NewReader(reqBody))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()

	if len(got.Prompt) > MaxPromptChars+len("...[truncated]") {
		t.Fatalf("expected prompt truncated, got length %d", len(got.Prompt))
	}
	if !strings.HasSuffix(got.Prompt, "...[truncated]") {
		t.Fatalf("expected truncation marker suffix")
	}
}

func TestRecordLLMCall(t *testing.T) {
	var got LLMCall
	sink := func(c LLMCall) { got = c }

	call := LLMCall{Provider: "bedrock", Model: "anthropic.claude-3", PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}
	RecordLLMCall(sink, call)

	if got.Provider != "bedrock" || got.Model != "anthropic.claude-3" {
		t.Fatalf("expected manual call recorded as-is, got %+v", got)
	}

	// nil sink must not panic.
	RecordLLMCall(nil, call)
}
