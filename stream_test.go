package octoryn

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestStreamTextIsReplayableAndAssemblesTools(t *testing.T) {
	client := testClient(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("X-Octoryn-Run-Id", "run_go_stream")
		writer.Header().Set("X-Octoryn-Upstream", "anthropic")
		_, _ = writer.Write(conformanceFixture(t, "chat-stream.sse"))
	})
	stream, err := client.StreamText(context.Background(), GenerateTextParams{
		Model:  "policy/frontier",
		Prompt: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	first := collectEvents(t, stream.Iterator())
	second := collectEvents(t, stream.Iterator())
	if len(first) != len(second) || len(first) < 5 {
		t.Fatalf("stream was not replayable: %d %d", len(first), len(second))
	}
	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "Octoryn" || result.FinishReason != "tool_calls" {
		t.Fatalf("unexpected result %#v", result)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Function.Name != "getWeather" {
		t.Fatalf("unexpected tools %#v", result.ToolCalls)
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
	if result.Octoryn.RunID != "run_go_stream" || result.Octoryn.Upstream != "anthropic" {
		t.Fatalf("missing governance %#v", result.Octoryn)
	}
}

func TestStreamTextCancellation(t *testing.T) {
	client := testClient(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := writer.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not support flushing")
		}
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"started\"}}]}\n\n"))
		flusher.Flush()
		<-request.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.StreamText(ctx, GenerateTextParams{
		Model:  "policy/frontier",
		Prompt: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	iterator := stream.Iterator()
	if !iterator.Next(context.Background()) || iterator.Event().Type != "start" {
		t.Fatal("missing start event")
	}
	if !iterator.Next(context.Background()) || iterator.Event().Text != "started" {
		t.Fatal("missing first text delta")
	}
	cancel()
	resultContext, resultCancel := context.WithTimeout(context.Background(), time.Second)
	defer resultCancel()
	if _, err := stream.Result(resultContext); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func collectEvents(t *testing.T, iterator *StreamIterator) []StreamEvent {
	t.Helper()
	var events []StreamEvent
	for iterator.Next(context.Background()) {
		events = append(events, iterator.Event())
	}
	if err := iterator.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}
