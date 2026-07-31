package octoryn

import (
	"encoding/json"
	"time"
)

type GovernanceMetadata struct {
	RunID          string   `json:"run_id,omitempty"`
	Upstream       string   `json:"upstream,omitempty"`
	BYOK           string   `json:"byok,omitempty"`
	Region         string   `json:"region,omitempty"`
	Route          string   `json:"route,omitempty"`
	PolicyDecision string   `json:"policy_decision,omitempty"`
	EvidenceHash   string   `json:"evidence_hash,omitempty"`
	EstimatedCost  *float64 `json:"estimated_cost,omitempty"`
}

type Message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type FunctionDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type ToolDefinition struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

func Tool(name, description string, schema map[string]any) ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDefinition{
			Name:        name,
			Description: description,
			Parameters:  schema,
		},
	}
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (call ToolCall) DecodeInput(target any) error {
	return json.Unmarshal([]byte(call.Function.Arguments), target)
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type GenerateTextParams struct {
	Model           string
	Prompt          string
	Messages        []Message
	System          string
	Tools           []ToolDefinition
	ToolChoice      any
	Temperature     *float64
	TopP            *float64
	MaxOutputTokens *int
	Metadata        map[string]string
}

type TextResult struct {
	Text         string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        *Usage
	Octoryn      GovernanceMetadata
	Response     ChatCompletion
}

type GenerateObjectParams struct {
	GenerateTextParams
	Schema            map[string]any
	SchemaName        string
	SchemaDescription string
}

type ObjectResult[T any] struct {
	Object T
	TextResult
}

type ChatCompletion struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type chatRequest struct {
	Model          string            `json:"model"`
	Messages       []Message         `json:"messages"`
	Stream         bool              `json:"stream,omitempty"`
	Temperature    *float64          `json:"temperature,omitempty"`
	TopP           *float64          `json:"top_p,omitempty"`
	MaxTokens      *int              `json:"max_tokens,omitempty"`
	Tools          []ToolDefinition  `json:"tools,omitempty"`
	ToolChoice     any               `json:"tool_choice,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	ResponseFormat any               `json:"response_format,omitempty"`
}

type StreamEvent struct {
	Type         string
	Text         string
	ToolCall     *ToolCall
	Usage        *Usage
	FinishReason string
	Octoryn      GovernanceMetadata
	Provider     json.RawMessage
	Err          error
}

type chatChunk struct {
	ID      string        `json:"id"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *Usage        `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type chunkDelta struct {
	Role      string          `json:"role,omitempty"`
	Content   string          `json:"content,omitempty"`
	ToolCalls []toolCallDelta `json:"tool_calls,omitempty"`
}

type toolCallDelta struct {
	Index    int               `json:"index"`
	ID       string            `json:"id,omitempty"`
	Type     string            `json:"type,omitempty"`
	Function functionCallDelta `json:"function,omitempty"`
}

type functionCallDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type AuditTimestamp = time.Time
