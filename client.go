package octoryn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.octoryn.dev/v1"

type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	headers    http.Header
}

type Option func(*Client)

func WithBaseURL(baseURL string) Option {
	return func(client *Client) {
		client.baseURL = strings.TrimRight(baseURL, "/")
	}
}

func WithHTTPClient(httpClient *http.Client) Option {
	return func(client *Client) {
		client.httpClient = httpClient
	}
}

func WithHeader(name, value string) Option {
	return func(client *Client) {
		client.headers.Set(name, value)
	}
}

func NewClient(apiKey string, options ...Option) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("octoryn: api key is required")
	}
	client := &Client{
		apiKey:  apiKey,
		baseURL: defaultBaseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Minute,
		},
		headers: make(http.Header),
	}
	for _, option := range options {
		option(client)
	}
	if client.httpClient == nil {
		return nil, errors.New("octoryn: HTTP client cannot be nil")
	}
	return client, nil
}

func (client *Client) GenerateText(
	ctx context.Context,
	params GenerateTextParams,
) (TextResult, error) {
	request, err := buildChatRequest(params, false)
	if err != nil {
		return TextResult{}, err
	}
	var completion ChatCompletion
	headers, err := client.postJSON(ctx, "/chat/completions", request, &completion)
	if err != nil {
		return TextResult{}, err
	}
	return normalizeCompletion(completion, governanceFromHeaders(headers))
}

func (client *Client) GenerateObject(
	ctx context.Context,
	params GenerateObjectParams,
	target any,
) (TextResult, error) {
	if target == nil {
		return TextResult{}, errors.New("octoryn: structured output target is required")
	}
	request, err := buildChatRequest(params.GenerateTextParams, false)
	if err != nil {
		return TextResult{}, err
	}
	name := params.SchemaName
	if name == "" {
		name = "response"
	}
	request.ResponseFormat = map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":        name,
			"description": params.SchemaDescription,
			"strict":      true,
			"schema":      params.Schema,
		},
	}
	var completion ChatCompletion
	headers, err := client.postJSON(ctx, "/chat/completions", request, &completion)
	if err != nil {
		return TextResult{}, err
	}
	result, err := normalizeCompletion(completion, governanceFromHeaders(headers))
	if err != nil {
		return TextResult{}, err
	}
	if err := json.Unmarshal([]byte(result.Text), target); err != nil {
		return TextResult{}, fmt.Errorf(
			"octoryn: structured output validation failed: %w; raw=%q",
			err,
			result.Text,
		)
	}
	return result, nil
}

func GenerateObject[T any](
	ctx context.Context,
	client *Client,
	params GenerateObjectParams,
) (ObjectResult[T], error) {
	var object T
	result, err := client.GenerateObject(ctx, params, &object)
	if err != nil {
		return ObjectResult[T]{}, err
	}
	return ObjectResult[T]{Object: object, TextResult: result}, nil
}

func buildChatRequest(params GenerateTextParams, stream bool) (chatRequest, error) {
	hasPrompt := params.Prompt != ""
	hasMessages := params.Messages != nil
	if hasPrompt == hasMessages {
		return chatRequest{}, errors.New("octoryn: pass exactly one of Prompt or Messages")
	}
	if strings.TrimSpace(params.Model) == "" {
		return chatRequest{}, errors.New("octoryn: model is required")
	}
	messages := make([]Message, 0, len(params.Messages)+2)
	if params.System != "" {
		messages = append(messages, Message{Role: "system", Content: params.System})
	}
	if hasPrompt {
		messages = append(messages, Message{Role: "user", Content: params.Prompt})
	} else {
		messages = append(messages, params.Messages...)
	}
	return chatRequest{
		Model:       params.Model,
		Messages:    messages,
		Stream:      stream,
		Temperature: params.Temperature,
		TopP:        params.TopP,
		MaxTokens:   params.MaxOutputTokens,
		Tools:       params.Tools,
		ToolChoice:  params.ToolChoice,
		Metadata:    params.Metadata,
	}, nil
}

func normalizeCompletion(
	completion ChatCompletion,
	metadata GovernanceMetadata,
) (TextResult, error) {
	if len(completion.Choices) == 0 {
		return TextResult{}, errors.New("octoryn: response contained no choices")
	}
	choice := completion.Choices[0]
	text, err := textContent(choice.Message.Content)
	if err != nil {
		return TextResult{}, err
	}
	return TextResult{
		Text:         text,
		ToolCalls:    choice.Message.ToolCalls,
		FinishReason: choice.FinishReason,
		Usage:        completion.Usage,
		Octoryn:      metadata,
		Response:     completion,
	}, nil
}

func textContent(content any) (string, error) {
	switch value := content.(type) {
	case string:
		return value, nil
	case nil:
		return "", nil
	case []any:
		var builder strings.Builder
		for _, item := range value {
			part, ok := item.(map[string]any)
			if ok && part["type"] == "text" {
				if text, ok := part["text"].(string); ok {
					builder.WriteString(text)
				}
			}
		}
		return builder.String(), nil
	default:
		return "", fmt.Errorf("octoryn: unsupported message content %T", content)
	}
}

func (client *Client) postJSON(
	ctx context.Context,
	path string,
	body any,
	target any,
) (http.Header, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("octoryn: encode request: %w", err)
	}
	request, err := client.newRequest(ctx, http.MethodPost, path, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("octoryn: request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload struct {
			Error struct {
				Code    string `json:"code"`
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload)
		return nil, errorFromResponse(response, payload)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return nil, fmt.Errorf("octoryn: decode response: %w", err)
	}
	return response.Header.Clone(), nil
}

func (client *Client) newRequest(
	ctx context.Context,
	method, path string,
	body io.Reader,
) (*http.Request, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		method,
		client.baseURL+"/"+strings.TrimLeft(path, "/"),
		body,
	)
	if err != nil {
		return nil, fmt.Errorf("octoryn: create request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+client.apiKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "octoryn-go/0.1.0")
	request.Header.Set("X-Octoryn-Sdk", "go/0.1.0")
	for name, values := range client.headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	return request, nil
}

func governanceFromHeaders(headers http.Header) GovernanceMetadata {
	metadata := GovernanceMetadata{
		RunID:          headers.Get("X-Octoryn-Run-Id"),
		Upstream:       headers.Get("X-Octoryn-Upstream"),
		BYOK:           headers.Get("X-Octoryn-Byok"),
		Region:         headers.Get("X-Octoryn-Region"),
		Route:          headers.Get("X-Octoryn-Route"),
		PolicyDecision: headers.Get("X-Octoryn-Policy-Decision"),
		EvidenceHash:   headers.Get("X-Octoryn-Evidence-Hash"),
	}
	if value := headers.Get("X-Octoryn-Estimated-Cost"); value != "" {
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			metadata.EstimatedCost = &parsed
		}
	}
	return metadata
}
