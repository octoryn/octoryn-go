package octoryn

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

type TextStream struct {
	mu       sync.Mutex
	events   []StreamEvent
	notify   chan struct{}
	done     bool
	result   TextResult
	err      error
	metadata GovernanceMetadata
}

type StreamIterator struct {
	stream *TextStream
	index  int
	event  StreamEvent
	err    error
}

func (client *Client) StreamText(
	ctx context.Context,
	params GenerateTextParams,
) (*TextStream, error) {
	payload, err := buildChatRequest(params, true)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("octoryn: encode request: %w", err)
	}
	request, err := client.newRequest(
		ctx,
		http.MethodPost,
		"/chat/completions",
		bytes.NewReader(encoded),
	)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("octoryn: stream request failed: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		var errorPayload struct {
			Error struct {
				Code    string `json:"code"`
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&errorPayload)
		return nil, errorFromResponse(response, errorPayload)
	}
	stream := &TextStream{
		notify:   make(chan struct{}),
		metadata: governanceFromHeaders(response.Header),
	}
	stream.push(StreamEvent{Type: "start", Octoryn: stream.metadata})
	go stream.consume(response.Body)
	return stream, nil
}

func (stream *TextStream) Iterator() *StreamIterator {
	return &StreamIterator{stream: stream}
}

func (iterator *StreamIterator) Next(ctx context.Context) bool {
	for {
		iterator.stream.mu.Lock()
		if iterator.index < len(iterator.stream.events) {
			iterator.event = iterator.stream.events[iterator.index]
			iterator.index++
			iterator.stream.mu.Unlock()
			return true
		}
		if iterator.stream.done {
			iterator.err = iterator.stream.err
			iterator.stream.mu.Unlock()
			return false
		}
		notify := iterator.stream.notify
		iterator.stream.mu.Unlock()
		select {
		case <-ctx.Done():
			iterator.err = ctx.Err()
			return false
		case <-notify:
		}
	}
}

func (iterator *StreamIterator) Event() StreamEvent {
	return iterator.event
}

func (iterator *StreamIterator) Err() error {
	return iterator.err
}

func (stream *TextStream) Result(ctx context.Context) (TextResult, error) {
	for {
		stream.mu.Lock()
		if stream.done {
			result, err := stream.result, stream.err
			stream.mu.Unlock()
			return result, err
		}
		notify := stream.notify
		stream.mu.Unlock()
		select {
		case <-ctx.Done():
			return TextResult{}, ctx.Err()
		case <-notify:
		}
	}
}

type pendingTool struct {
	id        string
	name      string
	arguments strings.Builder
}

func (stream *TextStream) consume(body io.ReadCloser) {
	defer body.Close()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	var data []string
	var text strings.Builder
	tools := make(map[int]*pendingTool)
	var usage *Usage
	var finishReason string
	sawDone := false
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		payload := strings.TrimSpace(strings.Join(data, "\n"))
		data = data[:0]
		if payload == "[DONE]" {
			sawDone = true
			return nil
		}
		var chunk chatChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return fmt.Errorf("octoryn: decode stream event: %w", err)
		}
		recognized := false
		if chunk.Usage != nil {
			usage = chunk.Usage
			stream.push(StreamEvent{Type: "usage", Usage: usage})
			recognized = true
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				text.WriteString(choice.Delta.Content)
				stream.push(StreamEvent{Type: "text-delta", Text: choice.Delta.Content})
				recognized = true
			}
			for _, delta := range choice.Delta.ToolCalls {
				current := tools[delta.Index]
				if current == nil {
					current = &pendingTool{id: fmt.Sprintf("tool_%d", delta.Index)}
					tools[delta.Index] = current
				}
				if delta.ID != "" {
					current.id = delta.ID
				}
				current.name += delta.Function.Name
				current.arguments.WriteString(delta.Function.Arguments)
				recognized = true
			}
			if choice.FinishReason != nil {
				finishReason = *choice.FinishReason
			}
		}
		if !recognized {
			stream.push(StreamEvent{Type: "provider-event", Provider: json.RawMessage(payload)})
		}
		return nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := flush(); err != nil {
				stream.finish(TextResult{}, err)
				return
			}
			if sawDone {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		stream.finish(TextResult{}, fmt.Errorf("octoryn: read stream: %w", err))
		return
	}
	if !sawDone {
		if err := flush(); err != nil {
			stream.finish(TextResult{}, err)
			return
		}
	}
	if !sawDone {
		stream.finish(TextResult{}, errors.New("octoryn: stream ended before [DONE]"))
		return
	}
	toolCalls := make([]ToolCall, 0, len(tools))
	for index := 0; index < len(tools); index++ {
		pending := tools[index]
		if pending == nil {
			continue
		}
		call := ToolCall{
			ID:   pending.id,
			Type: "function",
			Function: FunctionCall{
				Name:      pending.name,
				Arguments: pending.arguments.String(),
			},
		}
		toolCalls = append(toolCalls, call)
		stream.push(StreamEvent{Type: "tool-call", ToolCall: &call})
	}
	stream.push(StreamEvent{
		Type:         "finish",
		FinishReason: finishReason,
		Octoryn:      stream.metadata,
	})
	stream.finish(TextResult{
		Text:         text.String(),
		ToolCalls:    toolCalls,
		FinishReason: finishReason,
		Usage:        usage,
		Octoryn:      stream.metadata,
	}, nil)
}

func (stream *TextStream) push(event StreamEvent) {
	stream.mu.Lock()
	stream.events = append(stream.events, event)
	close(stream.notify)
	stream.notify = make(chan struct{})
	stream.mu.Unlock()
}

func (stream *TextStream) finish(result TextResult, err error) {
	stream.mu.Lock()
	stream.result = result
	stream.err = err
	stream.done = true
	close(stream.notify)
	stream.notify = make(chan struct{})
	stream.mu.Unlock()
}
