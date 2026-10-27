// Package llmobs provides GenAI/LLM observability: instrumentation
// wrappers that trace calls to LLM providers (OpenAI, Anthropic, Gemini,
// AWS Bedrock, ...) and capture provider, model, prompt/completion
// (truncated), token counts, latency, cost estimate and status.
package llmobs

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// LLMCall captures one observed call to an LLM provider.
type LLMCall struct {
	Provider         string
	Model            string
	Prompt           string // truncated
	Completion       string // truncated
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	LatencyMS        int64
	CostUSD          float64
	Error            string
	StatusCode       int
}

// ModelPricing describes per-1K-token pricing for a model.
type ModelPricing struct {
	PromptPer1K     float64
	CompletionPer1K float64
}

// PricingTable maps "provider:model" or plain "model" keys to pricing.
// Lookup tries "provider:model" first, then falls back to "model" alone.
type PricingTable map[string]ModelPricing

// EstimateCost computes a cost estimate in USD for the given provider,
// model and token counts using the pricing table. Returns 0 if no
// matching entry is found.
func (pt PricingTable) EstimateCost(provider, model string, promptTokens, completionTokens int) float64 {
	if pt == nil {
		return 0
	}
	price, ok := pt[provider+":"+model]
	if !ok {
		price, ok = pt[model]
		if !ok {
			return 0
		}
	}
	return (float64(promptTokens)/1000.0)*price.PromptPer1K +
		(float64(completionTokens)/1000.0)*price.CompletionPer1K
}

// MaxPromptChars / MaxCompletionChars bound how much prompt/completion
// text is retained in captured calls, to avoid unbounded memory use and
// leaking excessive content into observability sinks.
const (
	MaxPromptChars     = 2000
	MaxCompletionChars = 2000
)

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}

// ---------------------------------------------------------------------
// Wire shapes
// ---------------------------------------------------------------------

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// openAIRequest matches OpenAI/compatible chat-completions request bodies.
type openAIRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

// openAIResponse matches OpenAI-style responses, including usage and the
// first choice's message content (for completion capture) or an error.
type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// anthropicRequest matches Anthropic Messages API request bodies.
type anthropicRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

// anthropicResponse matches Anthropic Messages API responses.
type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Type string `json:"type"` // "error" for top-level error responses
}

func flattenContent(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if txt, ok := m["text"].(string); ok {
					parts = append(parts, txt)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func extractOpenAIPrompt(messages []chatMessage) string {
	var parts []string
	for _, m := range messages {
		parts = append(parts, flattenContent(m.Content))
	}
	return strings.Join(parts, "\n")
}

// ---------------------------------------------------------------------
// Body parsing (shared by Wrap and any manual instrumentation)
// ---------------------------------------------------------------------

// parseRequestBody attempts OpenAI-style then Anthropic-style parsing of
// a request body, filling in Model and Prompt on call if recognized.
// Returns true if a known shape was recognized.
func parseRequestBody(body []byte, call *LLMCall) bool {
	var oai openAIRequest
	if err := json.Unmarshal(body, &oai); err == nil && oai.Model != "" && len(oai.Messages) > 0 {
		call.Model = oai.Model
		call.Prompt = truncate(extractOpenAIPrompt(oai.Messages), MaxPromptChars)
		return true
	}
	var anth anthropicRequest
	if err := json.Unmarshal(body, &anth); err == nil && anth.Model != "" && len(anth.Messages) > 0 {
		call.Model = anth.Model
		call.Prompt = truncate(extractOpenAIPrompt(anth.Messages), MaxPromptChars)
		return true
	}
	return false
}

// parseResponseBody attempts OpenAI-style then Anthropic-style parsing of
// a response body, filling in completion/token fields on call. Returns
// true if a known shape was recognized.
func parseResponseBody(body []byte, call *LLMCall) bool {
	var oai openAIResponse
	if err := json.Unmarshal(body, &oai); err == nil {
		if oai.Error != nil {
			call.Error = oai.Error.Message
			return true
		}
		if len(oai.Choices) > 0 || oai.Usage.TotalTokens > 0 {
			if len(oai.Choices) > 0 {
				call.Completion = truncate(oai.Choices[0].Message.Content, MaxCompletionChars)
			}
			call.PromptTokens = oai.Usage.PromptTokens
			call.CompletionTokens = oai.Usage.CompletionTokens
			call.TotalTokens = oai.Usage.TotalTokens
			return true
		}
	}
	var anth anthropicResponse
	if err := json.Unmarshal(body, &anth); err == nil {
		if anth.Type == "error" && anth.Error != nil {
			call.Error = anth.Error.Message
			return true
		}
		if len(anth.Content) > 0 || anth.Usage.InputTokens > 0 || anth.Usage.OutputTokens > 0 {
			var texts []string
			for _, c := range anth.Content {
				if c.Type == "text" {
					texts = append(texts, c.Text)
				}
			}
			call.Completion = truncate(strings.Join(texts, "\n"), MaxCompletionChars)
			call.PromptTokens = anth.Usage.InputTokens
			call.CompletionTokens = anth.Usage.OutputTokens
			call.TotalTokens = anth.Usage.InputTokens + anth.Usage.OutputTokens
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------
// http.RoundTripper wrapper
// ---------------------------------------------------------------------

type wrappedTransport struct {
	next     http.RoundTripper
	provider string
	sink     func(LLMCall)
	pricing  PricingTable
}

// WrapOptions configures optional behavior of Wrap.
type WrapOptions struct {
	Pricing PricingTable
}

// Wrap returns an http.RoundTripper that instruments every call made
// through it: it inspects the request/response JSON bodies for
// OpenAI-style or Anthropic-style shapes, extracting model, prompt,
// completion and token usage. If the body shape isn't recognized, it
// still records provider, latency and status (generic fallback). The
// resulting LLMCall is passed to sink after the round trip completes.
func Wrap(next http.RoundTripper, provider string, sink func(LLMCall)) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &wrappedTransport{next: next, provider: provider, sink: sink}
}

// WrapWithPricing is like Wrap but also attaches a cost estimate using
// the given pricing table.
func WrapWithPricing(next http.RoundTripper, provider string, sink func(LLMCall), pricing PricingTable) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &wrappedTransport{next: next, provider: provider, sink: sink, pricing: pricing}
}

func (w *wrappedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	call := LLMCall{Provider: w.provider}

	var reqBody []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err == nil {
			reqBody = b
			req.Body = io.NopCloser(bytes.NewReader(b))
		}
	}
	if len(reqBody) > 0 {
		parseRequestBody(reqBody, &call)
	}

	start := time.Now()
	resp, err := w.next.RoundTrip(req)
	call.LatencyMS = time.Since(start).Milliseconds()

	if err != nil {
		call.Error = err.Error()
		if w.sink != nil {
			w.sink(call)
		}
		return resp, err
	}
	call.StatusCode = resp.StatusCode

	if resp.Body != nil {
		b, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(b))
		if rerr == nil && len(b) > 0 {
			parseResponseBody(b, &call)
		}
	}

	if resp.StatusCode >= 400 && call.Error == "" {
		call.Error = "http status " + resp.Status
	}

	if w.pricing != nil {
		call.CostUSD = w.pricing.EstimateCost(call.Provider, call.Model, call.PromptTokens, call.CompletionTokens)
	}

	if w.sink != nil {
		w.sink(call)
	}
	return resp, nil
}

// RecordLLMCall reports a manually constructed LLMCall to sink. Useful
// for providers/SDKs that aren't invoked over a traceable http.Client
// (e.g. a native AWS Bedrock SDK call), where the caller already has the
// relevant fields and just needs to emit them through the same pipeline.
func RecordLLMCall(sink func(LLMCall), call LLMCall) {
	if sink == nil {
		return
	}
	sink(call)
}
