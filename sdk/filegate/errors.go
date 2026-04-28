package filegate

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxErrorBodyBytes = 1 << 20

// APIError is returned when Filegate responds with a non-2xx status code.
type APIError struct {
	// StatusCode is the HTTP status returned by Filegate.
	StatusCode int
	// Message is the parsed `error` field from the response envelope, or
	// the HTTP status text when the body was not the expected shape.
	Message string
	// Body is the raw response body (truncated at 1 MiB). Always present
	// (may be empty).
	Body string
	// ExistingID is populated on 409 Conflict responses when the daemon
	// could resolve the colliding node. Empty otherwise. Use this for
	// rendering "what should we do?" UI prompts without an extra resolve
	// round-trip.
	ExistingID string
	// ExistingPath is populated on 409 Conflict responses when the daemon
	// could resolve the colliding node. Empty otherwise.
	ExistingPath string
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Sprintf("filegate api error (%d)", e.StatusCode)
	}
	return fmt.Sprintf("filegate api error (%d): %s", e.StatusCode, e.Message)
}

// IsConflict reports whether this error is a 409 Conflict from Filegate.
// On true, ExistingID and ExistingPath may be populated.
func (e *APIError) IsConflict() bool {
	return e != nil && e.StatusCode == http.StatusConflict
}

func ensureSuccess(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	msg := strings.TrimSpace(http.StatusText(resp.StatusCode))
	apiErr := &APIError{
		StatusCode: resp.StatusCode,
		Body:       string(bodyBytes),
	}
	if len(bodyBytes) > 0 {
		var payload ErrorResponse
		if err := json.Unmarshal(bodyBytes, &payload); err == nil {
			if trimmed := strings.TrimSpace(payload.Error); trimmed != "" {
				msg = trimmed
			}
			apiErr.ExistingID = payload.ExistingID
			apiErr.ExistingPath = payload.ExistingPath
		}
	}
	if msg == "" {
		msg = "request failed"
	}
	apiErr.Message = msg
	return apiErr
}
