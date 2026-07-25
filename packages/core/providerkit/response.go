package providerkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/memohai/connect-it/packages/core/connector"
)

// Response is a bounded, fully-read Provider response. Header and Body are
// detached from the original http.Response.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// ReadResponse closes and reads response with both declared Content-Length and
// actual max+1 byte enforcement. maxBytes applies to bytes after net/http's
// standard automatic gzip decompression.
//
// Remaining content encodings, including gzip when automatic decompression was
// disabled by the caller, are rejected. This prevents handlers from adding
// ad-hoc, differently bounded decompression paths.
func ReadResponse(response *http.Response, maxBytes int64) (*Response, error) {
	if maxBytes <= 0 || maxBytes == math.MaxInt64 {
		return nil, knownError(
			connector.FailureConfigurationError,
			defaultSafeMessage(connector.FailureConfigurationError),
		)
	}
	if err := validateResponseEnvelope(response); err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if err := validateResponseBody(response, maxBytes); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, knownError(
				connector.FailureCanceled,
				defaultSafeMessage(connector.FailureCanceled),
				WithCause(err),
				statusOption(response.StatusCode),
			)
		}
		return nil, knownError(
			connector.FailureUpstreamUnavailable,
			defaultSafeMessage(connector.FailureUpstreamUnavailable),
			WithCause(err),
			statusOption(response.StatusCode),
		)
	}
	if int64(len(body)) > maxBytes {
		return nil, knownError(
			connector.FailureResponseTooLarge,
			defaultSafeMessage(connector.FailureResponseTooLarge),
			statusOption(response.StatusCode),
		)
	}
	if response.StatusCode == http.StatusNoContent && len(body) != 0 {
		return nil, knownError(
			connector.FailureInvalidResponse,
			defaultSafeMessage(connector.FailureInvalidResponse),
			statusOption(response.StatusCode),
		)
	}

	return &Response{
		StatusCode: response.StatusCode,
		Header:     response.Header.Clone(),
		Body:       append([]byte(nil), body...),
	}, nil
}

func validateResponseEnvelope(response *http.Response) error {
	if response == nil || response.Body == nil ||
		response.StatusCode < 100 || response.StatusCode > 599 ||
		response.ContentLength < -1 {
		return knownError(
			connector.FailureInvalidResponse,
			defaultSafeMessage(connector.FailureInvalidResponse),
		)
	}
	return nil
}

func validateResponseBody(response *http.Response, maxBytes int64) error {
	if unsupportedContentEncoding(response.Header.Values("Content-Encoding")) {
		return knownError(
			connector.FailureInvalidResponse,
			defaultSafeMessage(connector.FailureInvalidResponse),
			statusOption(response.StatusCode),
		)
	}
	if !responseHasNoWireBody(response) &&
		response.ContentLength > maxBytes {
		return knownError(
			connector.FailureResponseTooLarge,
			defaultSafeMessage(connector.FailureResponseTooLarge),
			statusOption(response.StatusCode),
		)
	}
	return nil
}

// responseHasNoWireBody follows net/http's response-body rules. For HEAD and
// 304, Content-Length describes the selected representation rather than bytes
// carried by this response, so it must not trigger the declared-length cap.
// The actual body reader remains bounded in every case.
func responseHasNoWireBody(response *http.Response) bool {
	if response == nil {
		return false
	}
	if response.Request != nil &&
		response.Request.Method == http.MethodHead {
		return true
	}
	return response.StatusCode >= 100 && response.StatusCode <= 199 ||
		response.StatusCode == http.StatusNoContent ||
		response.StatusCode == http.StatusNotModified
}

// NoContent reports whether the Provider returned HTTP 204.
func (response *Response) NoContent() bool {
	return response != nil && response.StatusCode == http.StatusNoContent
}

// DecodeJSON decodes exactly one JSON value. Empty JSON is invalid except for
// an HTTP 204 response, which is an explicitly supported no-content success.
func (response *Response) DecodeJSON(destination any) error {
	if response == nil || destination == nil {
		return knownError(
			connector.FailureInvalidResponse,
			defaultSafeMessage(connector.FailureInvalidResponse),
		)
	}
	if response.NoContent() && len(response.Body) == 0 {
		return nil
	}
	if len(bytes.TrimSpace(response.Body)) == 0 {
		return knownError(
			connector.FailureInvalidResponse,
			defaultSafeMessage(connector.FailureInvalidResponse),
			statusOption(response.StatusCode),
		)
	}
	if !utf8.Valid(response.Body) {
		return knownError(
			connector.FailureInvalidResponse,
			defaultSafeMessage(connector.FailureInvalidResponse),
			statusOption(response.StatusCode),
		)
	}

	decoder := json.NewDecoder(bytes.NewReader(response.Body))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return knownError(
			connector.FailureInvalidResponse,
			defaultSafeMessage(connector.FailureInvalidResponse),
			WithCause(err),
			statusOption(response.StatusCode),
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return knownError(
			connector.FailureInvalidResponse,
			defaultSafeMessage(connector.FailureInvalidResponse),
			WithCause(err),
			statusOption(response.StatusCode),
		)
	}
	return nil
}

// StatusError returns a safe, generic error for a non-2xx status. Provider
// adapters may instead decode a documented error envelope and construct a more
// specific error, but raw response content must never enter the message.
func (response *Response) StatusError(now time.Time) error {
	if response == nil {
		return knownError(
			connector.FailureInvalidResponse,
			defaultSafeMessage(connector.FailureInvalidResponse),
		)
	}
	if response.StatusCode >= 200 && response.StatusCode <= 299 {
		return nil
	}

	code := statusFailureCode(response.StatusCode)
	if code == "" {
		code = connector.FailureProviderError
	}
	options := []ErrorOption{statusOption(response.StatusCode)}
	if response.StatusCode == http.StatusTooManyRequests {
		if delay, ok := ParseRetryAfter(
			response.Header.Get("Retry-After"),
			now,
		); ok {
			seconds := int64(delay / time.Second)
			if delay%time.Second != 0 {
				seconds++
			}
			if seconds <= int64(math.MaxInt) {
				options = append(
					options,
					WithRetryAfterSeconds(int(seconds)),
				)
			}
		}
	}
	return knownError(code, defaultSafeMessage(code), options...)
}

func unsupportedContentEncoding(values []string) bool {
	for _, value := range values {
		for _, encoding := range strings.Split(value, ",") {
			encoding = strings.TrimSpace(strings.ToLower(encoding))
			if encoding != "" && encoding != "identity" {
				return true
			}
		}
	}
	return false
}

func statusOption(status int) ErrorOption {
	if status >= 100 && status <= 599 {
		return WithUpstreamStatus(status)
	}
	return func(*errorOptions) error { return nil }
}
