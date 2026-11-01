package ai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// GeminiAPIError represents a structured error returned by or mapped from the Gemini API.
type GeminiAPIError struct {
	StatusCode    int    `json:"status_code"`
	ErrorCode     string `json:"error_code"`
	Message       string `json:"message"`
	RawResponse   string `json:"raw_response,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
}

func (e *GeminiAPIError) Error() string {
	if e.ErrorCode != "" {
		return fmt.Sprintf("Gemini API error (%d - %s): %s", e.StatusCode, e.ErrorCode, e.Message)
	}
	return fmt.Sprintf("Gemini API error (%d): %s", e.StatusCode, e.Message)
}

// MapHTTPStatusToErrorCode maps HTTP status codes from the Gemini API to standard API error codes.
func MapHTTPStatusToErrorCode(statusCode int, rawBody string) string {
	// If the rawBody contains explicit error details from Google API
	if strings.Contains(rawBody, `"status": "INVALID_ARGUMENT"`) || strings.Contains(rawBody, "invalid_argument") {
		return "invalid_request"
	}
	if strings.Contains(rawBody, `"status": "FAILED_PRECONDITION"`) {
		return "failed_precondition"
	}
	if strings.Contains(rawBody, `"status": "OUT_OF_RANGE"`) {
		return "out_of_range"
	}
	if strings.Contains(rawBody, `"status": "UNAUTHENTICATED"`) {
		return "authentication"
	}
	if strings.Contains(rawBody, `"status": "PERMISSION_DENIED"`) {
		return "permission_denied"
	}
	if strings.Contains(rawBody, `"status": "NOT_FOUND"`) {
		if strings.Contains(strings.ToLower(rawBody), "model") {
			return "model_not_found"
		}
		return "not_found"
	}
	if strings.Contains(rawBody, `"status": "ALREADY_EXISTS"`) {
		return "already_exists"
	}
	if strings.Contains(rawBody, `"status": "ABORTED"`) {
		return "aborted"
	}
	if strings.Contains(rawBody, `"status": "RESOURCE_EXHAUSTED"`) {
		if strings.Contains(strings.ToLower(rawBody), "quota") {
			return "quota_exceeded"
		}
		return "rate_limit_exceeded"
	}
	if strings.Contains(rawBody, `"status": "CANCELLED"`) {
		return "cancelled"
	}
	if strings.Contains(rawBody, `"status": "UNIMPLEMENTED"`) {
		return "unimplemented"
	}
	if strings.Contains(rawBody, `"status": "UNAVAILABLE"`) {
		return "service_unavailable"
	}
	if strings.Contains(rawBody, `"status": "DEADLINE_EXCEEDED"`) {
		return "deadline_exceeded"
	}

	// Fallback map by HTTP status code
	switch statusCode {
	case 400:
		return "invalid_request"
	case 401:
		return "authentication"
	case 402:
		return "payment_required"
	case 403:
		return "permission_denied"
	case 404:
		return "not_found"
	case 409:
		return "already_exists"
	case 416:
		return "out_of_range"
	case 429:
		return "rate_limit_exceeded"
	case 499:
		return "cancelled"
	case 500:
		return "api_error"
	case 501:
		return "unimplemented"
	case 503:
		return "service_unavailable"
	case 504:
		return "deadline_exceeded"
	default:
		return "api_error"
	}
}

// MapFinishReasonToErrorCode maps Gemini Generation blocked & error codes.
func MapFinishReasonToErrorCode(finishReason string, blockReason string) string {
	normFinish := strings.ToLower(strings.TrimSpace(finishReason))
	normBlock := strings.ToLower(strings.TrimSpace(blockReason))

	switch normBlock {
	case "safety":
		return "safety"
	case "blocklist":
		return "blocklist"
	case "prohibited_content":
		return "prohibited_content"
	case "image_safety":
		return "image_safety"
	case "spii":
		return "spii"
	}

	switch normFinish {
	case "safety":
		return "safety"
	case "recitation":
		return "recitation"
	case "language":
		return "language"
	case "blocklist":
		return "blocklist"
	case "prohibited_content":
		return "prohibited_content"
	case "spii":
		return "spii"
	case "malformed_function_call":
		return "malformed_function_call"
	case "image_safety":
		return "image_safety"
	case "image_recitation":
		return "image_recitation"
	case "image_prohibited_content":
		return "image_prohibited_content"
	case "image_other":
		return "image_other"
	case "unexpected_tool_call":
		return "unexpected_tool_call"
	case "too_many_tool_calls":
		return "too_many_tool_calls"
	case "missing_thought_signature":
		return "missing_thought_signature"
	case "content_blocked":
		return "content_blocked"
	case "other":
		return "content_blocked"
	default:
		if normFinish != "" && normFinish != "stop" && normFinish != "max_tokens" {
			return normFinish
		}
		return ""
	}
}

// ParseGeminiError constructs a GeminiAPIError from HTTP response status and raw body.
func ParseGeminiError(statusCode int, rawBody string, correlationID string) *GeminiAPIError {
	var parsed struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}

	msg := strings.TrimSpace(rawBody)
	if err := json.Unmarshal([]byte(rawBody), &parsed); err == nil && parsed.Error.Message != "" {
		msg = parsed.Error.Message
	}

	code := MapHTTPStatusToErrorCode(statusCode, rawBody)

	return &GeminiAPIError{
		StatusCode:    statusCode,
		ErrorCode:     code,
		Message:       msg,
		RawResponse:   rawBody,
		CorrelationID: correlationID,
	}
}
