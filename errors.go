package octoryn

import (
	"fmt"
	"net/http"
)

type APIError struct {
	Status     int
	Code       string
	Type       string
	Message    string
	RequestID  string
	RetryAfter string
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("octoryn: HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("octoryn: HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

func errorFromResponse(response *http.Response, payload struct {
	Error struct {
		Code    string `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}) error {
	message := payload.Error.Message
	if message == "" {
		message = response.Status
	}
	return &APIError{
		Status:     response.StatusCode,
		Code:       payload.Error.Code,
		Type:       payload.Error.Type,
		Message:    message,
		RequestID:  response.Header.Get("X-Request-Id"),
		RetryAfter: response.Header.Get("Retry-After"),
	}
}
