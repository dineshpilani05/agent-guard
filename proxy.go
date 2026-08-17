package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

var onBreakerTrip func(reason string)

var toolCallWatcher *repeatWatcher

// Pricing per million tokens, in USD. Update these if Anthropic changes
// pricing — keeping them in one place makes that a one-line fix.
const (
	pricePerMillionInputTokens  = 3.00  // Sonnet input rate
	pricePerMillionOutputTokens = 15.00 // Sonnet output rate
)

// runningTotalCost accumulates cost across the whole session — package
// level so it persists across every request the proxy handles.
var runningTotalCost float64

func estimateCost(usage anthropicUsage) float64 {
	inputCost := (float64(usage.InputTokens) / 1_000_000) * pricePerMillionInputTokens
	outputCost := (float64(usage.OutputTokens) / 1_000_000) * pricePerMillionOutputTokens
	return inputCost + outputCost
}

func startProxy(port string, targetURL string) error {
	target, err := url.Parse(targetURL)
	if err != nil {
		return err
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
		req.Header.Set("Accept-Encoding", "identity")
		inspectRequest(req)
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		return inspectResponse(resp)
	}

	fmt.Printf("Proxy listening on :%s, forwarding to %s\n", port, targetURL)

	return http.ListenAndServe(":"+port, proxy)
}

func inspectRequest(req *http.Request) {
	fmt.Printf("\n--> %s %s\n", req.Method, req.URL.Path)

	fmt.Println("  headers:")
	for name, values := range req.Header {
		for _, value := range values {
			if name == "X-Api-Key" || name == "Authorization" {
				fmt.Printf("    %s: [REDACTED, length=%d]\n", name, len(value))
			} else {
				fmt.Printf("    %s: %s\n", name, value)
			}
		}
	}

	if req.Body == nil {
		return
	}

	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		fmt.Println("  (failed to read request body:", err, ")")
		return
	}

	if len(bodyBytes) > 0 {
		fmt.Printf("  body (%d bytes): %s\n", len(bodyBytes), string(bodyBytes))
	}

	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ToolName  string          `json:"name,omitempty"`
	ToolInput json.RawMessage `json:"input,omitempty"`
}

type anthropicResponse struct {
	Usage   anthropicUsage `json:"usage"`
	Content []contentBlock `json:"content"`
}

// streamEvent describes the shape of one SSE "data:" line. Different event
// types populate different fields — a lot of these are pointers, so we can
// tell "this field wasn't in this event" (nil) apart from "it was zero".
type streamEvent struct {
	Type string `json:"type"`

	// Present on "message_start"
	Message *struct {
		Usage anthropicUsage `json:"usage"`
	} `json:"message,omitempty"`

	// Present on "content_block_start", "content_block_delta", "content_block_stop"
	Index int `json:"index"`

	// Present on "content_block_start"
	ContentBlock *contentBlock `json:"content_block,omitempty"`

	// Present on "content_block_delta"
	Delta *struct {
		Type        string `json:"type,omitempty"`
		Text        string `json:"text,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`
	} `json:"delta,omitempty"`

	// Present on "message_delta" (final output token count lives here)
	Usage *anthropicUsage `json:"usage,omitempty"`
}

// toolCallInProgress accumulates the pieces of one tool call as its
// arguments arrive split across multiple delta events.
type toolCallInProgress struct {
	name      string
	inputJSON strings.Builder
}

func inspectResponse(resp *http.Response) error {
	fmt.Printf("<-- status %d\n", resp.StatusCode)

	if resp.Body == nil {
		return nil
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != 200 {
		fmt.Printf("  ⚠️  error response body: %s\n", string(bodyBytes))
	} else if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		parseStreamingResponse(bodyBytes)
	} else {
		parseNonStreamingResponse(bodyBytes)
	}

	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	return nil
}

// parseNonStreamingResponse handles the simple case we built earlier —
// one complete JSON object as the whole response.
func parseNonStreamingResponse(bodyBytes []byte) {
	var parsed anthropicResponse
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		fmt.Println("  (could not parse response as Anthropic JSON:", err, ")")
		return
	}

	reportUsage(parsed.Usage)

	for _, block := range parsed.Content {
		if block.Type == "tool_use" {
			reportToolCall(block.ToolName, string(block.ToolInput))
		}
	}
}

// parseStreamingResponse walks a Server-Sent Events body line by line,
// accumulating tool calls and usage numbers across multiple events.
func parseStreamingResponse(bodyBytes []byte) {
	inProgress := make(map[int]*toolCallInProgress) // one entry per content block index
	var totalUsage anthropicUsage

	scanner := bufio.NewScanner(bytes.NewReader(bodyBytes))
	// SSE lines can be long (a big text delta) — raise the scanner's max
	// line size above its default, so it doesn't error out on a long line.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		// We only care about lines carrying actual event data.
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		jsonPart := strings.TrimPrefix(line, "data: ")

		var event streamEvent
		if err := json.Unmarshal([]byte(jsonPart), &event); err != nil {
			continue // skip anything that doesn't parse — not fatal
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				totalUsage.InputTokens = event.Message.Usage.InputTokens
			}

		case "content_block_start":
			if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
				inProgress[event.Index] = &toolCallInProgress{name: event.ContentBlock.ToolName}
			}

		case "content_block_delta":
			if event.Delta != nil && event.Delta.Type == "input_json_delta" {
				if tc, exists := inProgress[event.Index]; exists {
					tc.inputJSON.WriteString(event.Delta.PartialJSON)
				}
			}

		case "content_block_stop":
			if tc, exists := inProgress[event.Index]; exists {
				reportToolCall(tc.name, tc.inputJSON.String())
				delete(inProgress, event.Index)
			}

		case "message_delta":
			if event.Usage != nil {
				totalUsage.OutputTokens = event.Usage.OutputTokens
			}
		}
	}

	reportUsage(totalUsage)
}

// reportUsage and reportToolCall are shared by both the streaming and
// non-streaming paths, so the actual logging/detection logic lives in
// exactly one place.
func reportUsage(usage anthropicUsage) {
	if usage.InputTokens > 0 || usage.OutputTokens > 0 {
		total := usage.InputTokens + usage.OutputTokens
		cost := estimateCost(usage)
		runningTotalCost += cost

		fmt.Printf("  📊 tokens used: %d in / %d out (%d total) — $%.4f this call, $%.4f session total\n",
			usage.InputTokens, usage.OutputTokens, total, cost, runningTotalCost)
	}
}

func reportToolCall(name string, inputJSON string) {
	signature := fmt.Sprintf("%s(%s)", name, inputJSON)
	fmt.Printf("  🔧 tool call: %s\n", signature)

	if toolCallWatcher.observe(signature) {
		reason := fmt.Sprintf("tool call repeated %d times: %s (session cost so far: $%.4f)",
			toolCallWatcher.countFor(signature), signature, runningTotalCost)
		fmt.Println("\n🚨 CIRCUIT BREAKER TRIPPED (repeated tool call detected) 🚨")
		fmt.Println("  " + reason)

		if onBreakerTrip != nil {
			onBreakerTrip(reason)
		}
	}
}
