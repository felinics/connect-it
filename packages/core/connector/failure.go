package connector

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"
)

// MaxSafeMessageBytes is the maximum user/Agent-visible failure message.
const MaxSafeMessageBytes = 512

// ErrInvalidToolFailure reports an invalid public ToolFailure shape without
// echoing the potentially sensitive invalid value.
var ErrInvalidToolFailure = errors.New("invalid tool failure")

// FailureCode 是对 Agent 稳定公开的 Tool 执行失败码。
type FailureCode string

const (
	FailureInvalidInput        FailureCode = "invalid_input"
	FailureInputTooLarge       FailureCode = "input_too_large"
	FailureToolUnavailable     FailureCode = "tool_unavailable"
	FailureConfigurationError  FailureCode = "configuration_error"
	FailureAuthorizationFailed FailureCode = "authorization_failed"
	FailurePermissionDenied    FailureCode = "permission_denied"
	FailureNotFound            FailureCode = "not_found"
	FailureConflict            FailureCode = "conflict"
	FailureRateLimited         FailureCode = "rate_limited"
	FailureProviderError       FailureCode = "provider_error"
	FailureInvalidResponse     FailureCode = "invalid_response"
	FailureUpstreamUnavailable FailureCode = "upstream_unavailable"
	FailureTimeout             FailureCode = "timeout"
	FailureCanceled            FailureCode = "canceled"
	FailureResponseTooLarge    FailureCode = "response_too_large"
	FailurePolicyDenied        FailureCode = "policy_denied"
	FailureInternalError       FailureCode = "internal_error"
)

// failureMessages 是失败码集合与其稳定默认文案的唯一来源：
// 新增一个码只需在此登记一次，Valid 与 DefaultFailureMessage 自动保持一致。
var failureMessages = map[FailureCode]string{
	FailureInvalidInput:        "invalid input",
	FailureInputTooLarge:       "input exceeds the allowed size",
	FailureToolUnavailable:     "tool is unavailable",
	FailureConfigurationError:  "provider is not configured correctly",
	FailureAuthorizationFailed: "provider authorization failed",
	FailurePermissionDenied:    "provider permission denied",
	FailureNotFound:            "provider resource not found",
	FailureConflict:            "provider reported a conflict",
	FailureRateLimited:         "provider rate limit exceeded",
	FailureProviderError:       "provider request failed",
	FailureInvalidResponse:     "provider returned an invalid response",
	FailureUpstreamUnavailable: "provider is temporarily unavailable",
	FailureTimeout:             "provider request timed out",
	FailureCanceled:            "request was canceled",
	FailureResponseTooLarge:    "provider response exceeds the allowed size",
	FailurePolicyDenied:        "provider request was denied by policy",
	FailureInternalError:       "internal error",
}

// Valid 报告 code 是否属于平台稳定的 Tool 执行失败码集合。
func (c FailureCode) Valid() bool {
	_, ok := failureMessages[c]
	return ok
}

// Temporary 报告该失败码是否值得重试（限流/上游不可用/超时）。
func (c FailureCode) Temporary() bool {
	switch c {
	case FailureRateLimited,
		FailureUpstreamUnavailable,
		FailureTimeout:
		return true
	default:
		return false
	}
}

// FailureCodeForStatus 把上游 HTTP 状态码映射为稳定失败码。
// 无法归类的状态返回空码（调用方需自行选择兜底码）。
func FailureCodeForStatus(status int) FailureCode {
	switch status {
	case http.StatusUnauthorized:
		return FailureAuthorizationFailed
	case http.StatusForbidden:
		return FailurePermissionDenied
	case http.StatusNotFound:
		return FailureNotFound
	case http.StatusConflict:
		return FailureConflict
	case http.StatusTooManyRequests:
		return FailureRateLimited
	default:
		if status >= 500 && status <= 599 {
			return FailureUpstreamUnavailable
		}
		if status >= 400 && status <= 499 {
			return FailureProviderError
		}
		return ""
	}
}

// ToolFailure 是 Provider/Tool 边界产生的安全失败信息。
// Message 不得包含原始 Provider body 或其他敏感详情；UpstreamStatus 仅供内部审计，
// 不应直接回显给 Agent。
type ToolFailure struct {
	Code              FailureCode
	Message           string
	UpstreamStatus    int
	RetryAfterSeconds int
	// CredentialInvalid is an internal execution signal. It is deliberately
	// excluded from JSON and may only accompany authorization_failed. Public
	// adapters must project the allowlisted ToolFailure fields instead of
	// serializing this struct wholesale.
	CredentialInvalid bool `json:"-"`
}

// Failure builds a public failure envelope from compile-time constants.
// It never returns an error: an invalid code, message, status or retry delay
// is clamped fail-closed to internal_error with the stable internal message,
// and the invalid input is never echoed. Callers that relay Provider-supplied
// values must use NewToolFailure and handle the validation error instead.
func Failure(
	code FailureCode,
	message string,
	upstreamStatus int,
	retryAfterSeconds int,
) *ToolFailure {
	failure := &ToolFailure{
		Code:              code,
		Message:           message,
		UpstreamStatus:    upstreamStatus,
		RetryAfterSeconds: retryAfterSeconds,
	}
	if !failure.Valid() {
		return clampedFailure()
	}
	return failure
}

// CredentialInvalidFailure builds the explicit credential-invalid signal from
// compile-time constants, clamping invalid input exactly like Failure.
// A generic authorization_failed result must use Failure and does not change
// Connection state.
func CredentialInvalidFailure(message string, upstreamStatus int) *ToolFailure {
	failure := &ToolFailure{
		Code:              FailureAuthorizationFailed,
		Message:           message,
		UpstreamStatus:    upstreamStatus,
		CredentialInvalid: true,
	}
	if !failure.Valid() {
		return clampedFailure()
	}
	return failure
}

// clampedFailure 是唯一的 fail-closed 兜底失败：不携带任何非法输入。
func clampedFailure() *ToolFailure {
	return &ToolFailure{
		Code:    FailureInternalError,
		Message: DefaultFailureMessage(FailureInternalError),
	}
}

// NewToolFailure validates the complete public failure envelope.
func NewToolFailure(
	code FailureCode,
	message string,
	upstreamStatus int,
	retryAfterSeconds int,
) (*ToolFailure, error) {
	failure := &ToolFailure{
		Code:              code,
		Message:           message,
		UpstreamStatus:    upstreamStatus,
		RetryAfterSeconds: retryAfterSeconds,
	}
	if !failure.Valid() {
		return nil, ErrInvalidToolFailure
	}
	return failure, nil
}

// NewCredentialInvalidFailure constructs the explicit signal used when a
// Provider has definitively reported that the credential is revoked, expired,
// or otherwise unusable. A generic authorization_failed result must use
// NewToolFailure and does not change Connection state.
func NewCredentialInvalidFailure(
	message string,
	upstreamStatus int,
) (*ToolFailure, error) {
	failure := &ToolFailure{
		Code:              FailureAuthorizationFailed,
		Message:           message,
		UpstreamStatus:    upstreamStatus,
		CredentialInvalid: true,
	}
	if !failure.Valid() {
		return nil, ErrInvalidToolFailure
	}
	return failure, nil
}

// Valid reports whether the failure may cross an API or MCP boundary.
func (f *ToolFailure) Valid() bool {
	if f == nil ||
		!f.Code.Valid() ||
		!ValidSafeMessage(f.Message) ||
		f.RetryAfterSeconds < 0 ||
		(f.CredentialInvalid &&
			f.Code != FailureAuthorizationFailed) {
		return false
	}
	return f.UpstreamStatus == 0 ||
		(f.UpstreamStatus >= 100 && f.UpstreamStatus <= 599)
}

// IndicatesCredentialInvalid reports whether this is a valid, explicit
// credential-invalid signal. Checking only the public code or HTTP status is
// intentionally insufficient: 403 and ordinary authorization failures are
// state-neutral.
func (f *ToolFailure) IndicatesCredentialInvalid() bool {
	return f != nil && f.CredentialInvalid && f.Valid()
}

// NormalizeToolFailure returns a defensive copy safe for public emission.
// Invalid public structs collapse to one stable internal error and never echo
// the invalid code/message/status.
func NormalizeToolFailure(f *ToolFailure) ToolFailure {
	if f != nil && f.Valid() {
		return *f
	}
	return *clampedFailure()
}

// ValidSafeMessage enforces the common public-message contract.
func ValidSafeMessage(message string) bool {
	if message == "" ||
		len(message) > MaxSafeMessageBytes ||
		!utf8.ValidString(message) ||
		strings.TrimSpace(message) != message {
		return false
	}
	for _, r := range message {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// DefaultFailureMessage returns a bounded stable message for a FailureCode.
// 未知码回退到 internal error，不回显该码。
func DefaultFailureMessage(code FailureCode) string {
	if message, ok := failureMessages[code]; ok {
		return message
	}
	return failureMessages[FailureInternalError]
}
