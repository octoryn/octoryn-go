package octoryn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func conformanceFixture(t *testing.T, name string) []byte {
	t.Helper()
	paths := []string{
		filepath.Join("..", "sdk-conformance", "v1", name),
		filepath.Join("sdk-conformance", "v1", name),
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err == nil {
			return data
		}
	}
	t.Fatalf("missing conformance fixture %q in %v", name, paths)
	return nil
}

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(
		"test-key",
		WithBaseURL(server.URL+"/v1"),
		WithHTTPClient(server.Client()),
	)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestGenerateTextAndTools(t *testing.T) {
	client := testClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected request path %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatal("missing authorization")
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "policy/frontier" {
			t.Fatalf("unexpected model %v", body["model"])
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-Octoryn-Run-Id", "run_go")
		writer.Header().Set("X-Octoryn-Region", "au-sydney")
		writer.Header().Set("X-Octoryn-Estimated-Cost", "0.001")
		_, _ = writer.Write(conformanceFixture(t, "chat-completion.json"))
	})
	result, err := client.GenerateText(context.Background(), GenerateTextParams{
		Model:  "policy/frontier",
		Prompt: "Weather?",
		Tools: []ToolDefinition{
			Tool("getWeather", "Get weather", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"city": map[string]any{"type": "string"},
				},
				"required": []string{"city"},
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "Governed answer" || len(result.ToolCalls) != 1 {
		t.Fatalf("unexpected result %#v", result)
	}
	var input struct {
		City string `json:"city"`
	}
	if err := result.ToolCalls[0].DecodeInput(&input); err != nil {
		t.Fatal(err)
	}
	if input.City != "Sydney" {
		t.Fatalf("unexpected tool input %#v", input)
	}
	if result.Octoryn.RunID != "run_go" || result.Octoryn.Region != "au-sydney" {
		t.Fatalf("missing governance %#v", result.Octoryn)
	}
	if result.Octoryn.EstimatedCost == nil || *result.Octoryn.EstimatedCost != 0.001 {
		t.Fatalf("missing cost %#v", result.Octoryn)
	}
}

func TestGenerateObject(t *testing.T) {
	client := testClient(t, func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["response_format"] == nil {
			t.Fatal("missing response format")
		}
		response := map[string]any{
			"id":     "object_go",
			"object": "chat.completion",
			"model":  "policy/risk",
			"choices": []any{map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": `{"risk":"low","score":7}`,
				},
				"finish_reason": "stop",
			}},
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(response)
	})
	type risk struct {
		Risk  string `json:"risk"`
		Score int    `json:"score"`
	}
	result, err := GenerateObject[risk](
		context.Background(),
		client,
		GenerateObjectParams{
			GenerateTextParams: GenerateTextParams{
				Model:  "policy/risk",
				Prompt: "Assess",
			},
			SchemaName: "risk_assessment",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"risk":  map[string]any{"type": "string"},
					"score": map[string]any{"type": "number"},
				},
				"required": []string{"risk", "score"},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Object.Risk != "low" || result.Object.Score != 7 {
		t.Fatalf("unexpected object %#v", result.Object)
	}
}

func TestAPIError(t *testing.T) {
	client := testClient(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Retry-After", "2")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":{"code":"quota_exceeded","message":"budget exhausted"}}`))
	})
	_, err := client.GenerateText(context.Background(), GenerateTextParams{
		Model:  "policy/frontier",
		Prompt: "hello",
	})
	apiError, ok := err.(*APIError)
	if !ok || apiError.Code != "quota_exceeded" || apiError.RetryAfter != "2" {
		t.Fatalf("unexpected error %#v", err)
	}
}
