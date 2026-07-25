package providerkit

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

func TestReadResponseEnforcesDeclaredAndActualSize(t *testing.T) {
	t.Parallel()

	t.Run("declared content length rejected before read", func(t *testing.T) {
		t.Parallel()

		body := &trackingReadCloser{Reader: strings.NewReader("small")}
		response := &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          body,
			ContentLength: 11,
		}
		_, err := ReadResponse(response, 10)
		assertFailureCode(t, err, connector.FailureResponseTooLarge)
		if body.reads != 0 {
			t.Fatalf("body reads = %d, want zero", body.reads)
		}
		if !body.closed {
			t.Fatal("body was not closed")
		}
	})

	t.Run("exact actual limit accepted", func(t *testing.T) {
		t.Parallel()

		response := testHTTPResponse(
			http.StatusOK,
			-1,
			io.NopCloser(strings.NewReader("0123456789")),
		)
		got, err := ReadResponse(response, 10)
		if err != nil {
			t.Fatalf("ReadResponse: %v", err)
		}
		if string(got.Body) != "0123456789" {
			t.Fatalf("Body = %q", got.Body)
		}
	})

	t.Run("chunked max plus one rejected", func(t *testing.T) {
		t.Parallel()

		body := &trackingReadCloser{Reader: strings.NewReader("01234567890")}
		response := testHTTPResponse(http.StatusOK, -1, body)
		_, err := ReadResponse(response, 10)
		assertFailureCode(t, err, connector.FailureResponseTooLarge)
		if !body.closed {
			t.Fatal("body was not closed")
		}
		if body.bytesRead != 11 {
			t.Fatalf("bytes read = %d, want bounded max+1", body.bytesRead)
		}
	})
}

func TestReadResponseSkipsRepresentationLengthForNoWireBody(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		method string
		status int
	}{
		{name: "HEAD", method: http.MethodHead, status: http.StatusOK},
		{name: "304", method: http.MethodGet, status: http.StatusNotModified},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			response, err := ReadResponse(&http.Response{
				StatusCode:    test.status,
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader("")),
				ContentLength: 1 << 20,
				Request:       &http.Request{Method: test.method},
			}, 8)
			if err != nil {
				t.Fatalf("ReadResponse() error = %v", err)
			}
			if len(response.Body) != 0 {
				t.Fatalf("body = %q, want empty", response.Body)
			}
		})
	}
}

func TestReadResponseUsesStandardLibraryAutomaticGzip(t *testing.T) {
	t.Parallel()

	payload := strings.Repeat("decompressed-", 100)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.Header().Set("Content-Encoding", "gzip")
		gzipWriter := gzip.NewWriter(writer)
		_, _ = gzipWriter.Write([]byte(payload))
		_ = gzipWriter.Close()
	}))
	defer server.Close()

	httpResponse, err := http.Get(server.URL) //nolint:gosec // local httptest server
	if err != nil {
		t.Fatalf("GET test server: %v", err)
	}
	if !httpResponse.Uncompressed {
		t.Fatal("net/http did not mark automatically decompressed response")
	}
	if got := httpResponse.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("automatic gzip retained Content-Encoding %q", got)
	}

	response, err := ReadResponse(httpResponse, int64(len(payload)))
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	if string(response.Body) != payload {
		t.Fatalf("decompressed body length = %d, want %d", len(response.Body), len(payload))
	}

	tooLargeHTTPResponse, err := http.Get(server.URL) //nolint:gosec // local httptest server
	if err != nil {
		t.Fatalf("GET test server for bounded read: %v", err)
	}
	_, err = ReadResponse(tooLargeHTTPResponse, int64(len(payload)-1))
	assertFailureCode(t, err, connector.FailureResponseTooLarge)
}

func TestReadResponseRejectsRemainingContentEncoding(t *testing.T) {
	t.Parallel()

	for _, encoding := range []string{"gzip", "br", "deflate", "gzip, br"} {
		encoding := encoding
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()

			body := &trackingReadCloser{Reader: strings.NewReader("encoded")}
			response := testHTTPResponse(http.StatusOK, -1, body)
			response.Header.Set("Content-Encoding", encoding)
			_, err := ReadResponse(response, 100)
			assertFailureCode(t, err, connector.FailureInvalidResponse)
			if body.reads != 0 {
				t.Fatalf("encoded body reads = %d, want zero", body.reads)
			}
			if !body.closed {
				t.Fatal("encoded body was not closed")
			}
		})
	}

	response := testHTTPResponse(
		http.StatusOK,
		-1,
		io.NopCloser(strings.NewReader("plain")),
	)
	response.Header.Set("Content-Encoding", "identity")
	if _, err := ReadResponse(response, 10); err != nil {
		t.Fatalf("identity encoding: %v", err)
	}
}

func TestResponseDecodeJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		body       string
		wantCode   connector.FailureCode
		wantNumber string
	}{
		{
			name:       "valid exactly one value",
			status:     http.StatusOK,
			body:       `{"number":9007199254740993}`,
			wantNumber: "9007199254740993",
		},
		{
			name:     "empty JSON",
			status:   http.StatusOK,
			body:     "",
			wantCode: connector.FailureInvalidResponse,
		},
		{
			name:     "whitespace JSON",
			status:   http.StatusOK,
			body:     " \n\t",
			wantCode: connector.FailureInvalidResponse,
		},
		{
			name:     "malformed JSON",
			status:   http.StatusOK,
			body:     `{"broken":`,
			wantCode: connector.FailureInvalidResponse,
		},
		{
			name:     "multiple JSON values",
			status:   http.StatusOK,
			body:     `{} {}`,
			wantCode: connector.FailureInvalidResponse,
		},
		{
			name:   "204 no content",
			status: http.StatusNoContent,
			body:   "",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			response := &Response{
				StatusCode: test.status,
				Header:     make(http.Header),
				Body:       []byte(test.body),
			}
			var decoded map[string]any
			err := response.DecodeJSON(&decoded)
			if test.wantCode != "" {
				assertFailureCode(t, err, test.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("DecodeJSON: %v", err)
			}
			if test.wantNumber != "" {
				if got := decoded["number"].(interface{ String() string }).String(); got != test.wantNumber {
					t.Fatalf("number = %q, want %q", got, test.wantNumber)
				}
			}
		})
	}

	t.Run("invalid UTF-8 JSON string", func(t *testing.T) {
		t.Parallel()

		response := &Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       []byte{'{', '"', 'i', 'd', '"', ':', '"', 0xff, '"', '}'},
		}
		var decoded map[string]any
		err := response.DecodeJSON(&decoded)
		assertFailureCode(t, err, connector.FailureInvalidResponse)
	})
}

func TestResponseNoContent(t *testing.T) {
	t.Parallel()

	response := &Response{
		StatusCode: http.StatusOK,
	}
	if response.NoContent() {
		t.Fatal("200 response reported no content")
	}

	response.StatusCode = http.StatusNoContent
	if !response.NoContent() {
		t.Fatal("204 response did not report no content")
	}

	var nilResponse *Response
	if nilResponse.NoContent() {
		t.Fatal("nil response reported no content")
	}
	err := nilResponse.DecodeJSON(&struct{}{})
	assertFailureCode(t, err, connector.FailureInvalidResponse)
}

func TestResponseStatusErrorIsStableAndDoesNotExposeBody(t *testing.T) {
	t.Parallel()

	now := time.Date(
		2026,
		time.July,
		23,
		12,
		0,
		0,
		500_000_000,
		time.UTC,
	)
	tests := []struct {
		name      string
		status    int
		wantCode  connector.FailureCode
		retry     string
		wantRetry int
	}{
		{
			name:     "unauthorized",
			status:   http.StatusUnauthorized,
			wantCode: connector.FailureAuthorizationFailed,
		},
		{
			name:     "forbidden",
			status:   http.StatusForbidden,
			wantCode: connector.FailurePermissionDenied,
		},
		{
			name:     "not found",
			status:   http.StatusNotFound,
			wantCode: connector.FailureNotFound,
		},
		{
			name:     "conflict",
			status:   http.StatusConflict,
			wantCode: connector.FailureConflict,
		},
		{
			name:      "rate limited delta",
			status:    http.StatusTooManyRequests,
			wantCode:  connector.FailureRateLimited,
			retry:     "7",
			wantRetry: 7,
		},
		{
			name:      "rate limited HTTP date rounds up",
			status:    http.StatusTooManyRequests,
			wantCode:  connector.FailureRateLimited,
			retry:     now.Add(1500 * time.Millisecond).Format(http.TimeFormat),
			wantRetry: 2,
		},
		{
			name:     "unprocessable",
			status:   http.StatusUnprocessableEntity,
			wantCode: connector.FailureProviderError,
		},
		{
			name:     "server unavailable",
			status:   http.StatusServiceUnavailable,
			wantCode: connector.FailureUpstreamUnavailable,
		},
		{
			name:     "redirection without follow",
			status:   http.StatusNotModified,
			wantCode: connector.FailureProviderError,
		},
	}

	secretBody := "raw-secret-provider-error"
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			response := &Response{
				StatusCode: test.status,
				Header:     make(http.Header),
				Body:       []byte(secretBody),
			}
			response.Header.Set("Retry-After", test.retry)
			err := response.StatusError(now)
			failure := AsToolFailure(err)
			if failure.Code != test.wantCode ||
				failure.UpstreamStatus != test.status ||
				failure.RetryAfterSeconds != test.wantRetry {
				t.Fatalf("StatusError = %#v", failure)
			}
			if strings.Contains(err.Error(), secretBody) ||
				strings.Contains(failure.Message, secretBody) {
				t.Fatal("StatusError exposed Provider body")
			}
		})
	}

	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusNoContent} {
		response := &Response{StatusCode: status}
		if err := response.StatusError(now); err != nil {
			t.Fatalf("success status %d returned %v", status, err)
		}
	}
	var nilResponse *Response
	assertFailureCode(
		t,
		nilResponse.StatusError(now),
		connector.FailureInvalidResponse,
	)
}

func TestReadResponseRejectsInvalidInputsAnd204Body(t *testing.T) {
	t.Parallel()

	for _, input := range []struct {
		name     string
		response *http.Response
		max      int64
		wantCode connector.FailureCode
	}{
		{
			name:     "nil response",
			max:      10,
			wantCode: connector.FailureInvalidResponse,
		},
		{
			name:     "nil body",
			response: &http.Response{StatusCode: http.StatusOK},
			max:      10,
			wantCode: connector.FailureInvalidResponse,
		},
		{
			name:     "zero limit",
			response: testHTTPResponse(http.StatusOK, 0, http.NoBody),
			max:      0,
			wantCode: connector.FailureConfigurationError,
		},
		{
			name:     "negative limit",
			response: testHTTPResponse(http.StatusOK, 0, http.NoBody),
			max:      -1,
			wantCode: connector.FailureConfigurationError,
		},
		{
			name: "invalid status",
			response: &http.Response{
				StatusCode:    0,
				Header:        make(http.Header),
				Body:          http.NoBody,
				ContentLength: 0,
			},
			max:      1,
			wantCode: connector.FailureInvalidResponse,
		},
		{
			name: "invalid content length",
			response: &http.Response{
				StatusCode:    http.StatusOK,
				Header:        make(http.Header),
				Body:          http.NoBody,
				ContentLength: -2,
			},
			max:      1,
			wantCode: connector.FailureInvalidResponse,
		},
	} {
		input := input
		t.Run(input.name, func(t *testing.T) {
			t.Parallel()
			_, err := ReadResponse(input.response, input.max)
			assertFailureCode(t, err, input.wantCode)
		})
	}

	response := testHTTPResponse(
		http.StatusNoContent,
		1,
		io.NopCloser(strings.NewReader("x")),
	)
	_, err := ReadResponse(response, 10)
	assertFailureCode(t, err, connector.FailureInvalidResponse)
}

func TestReadResponseDetachesHeaderAndBody(t *testing.T) {
	t.Parallel()

	original := testHTTPResponse(
		http.StatusCreated,
		2,
		io.NopCloser(strings.NewReader("ok")),
	)
	original.Header.Set("X-Request-ID", "original")
	response, err := ReadResponse(original, 10)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	original.Header.Set("X-Request-ID", "changed")
	if got := response.Header.Get("X-Request-ID"); got != "original" {
		t.Fatalf("detached header = %q", got)
	}
	if response.StatusCode != http.StatusCreated || string(response.Body) != "ok" {
		t.Fatalf("response = %#v", response)
	}
}

func TestReadResponseMapsReadErrorsWithoutLeaking(t *testing.T) {
	t.Parallel()

	secret := "secret-read-error"
	response := testHTTPResponse(
		http.StatusBadGateway,
		-1,
		&errorReadCloser{err: errors.New(secret)},
	)
	_, err := ReadResponse(response, 100)
	assertFailureCode(t, err, connector.FailureUpstreamUnavailable)
	if strings.Contains(err.Error(), secret) ||
		strings.Contains(AsToolFailure(err).Message, secret) {
		t.Fatal("response read error leaked internal text")
	}

	response = testHTTPResponse(
		http.StatusOK,
		-1,
		&errorReadCloser{err: context.Canceled},
	)
	_, err = ReadResponse(response, 100)
	assertFailureCode(t, err, connector.FailureCanceled)
}

func testHTTPResponse(
	status int,
	contentLength int64,
	body io.ReadCloser,
) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Header:        make(http.Header),
		Body:          body,
		ContentLength: contentLength,
	}
}

func assertFailureCode(
	t *testing.T,
	err error,
	want connector.FailureCode,
) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %s", want)
	}
	failure := AsToolFailure(err)
	if failure == nil || failure.Code != want {
		t.Fatalf("failure = %#v for %v, want %s", failure, err, want)
	}
}

type trackingReadCloser struct {
	io.Reader
	reads     int
	bytesRead int
	closed    bool
}

func (body *trackingReadCloser) Read(buffer []byte) (int, error) {
	body.reads++
	count, err := body.Reader.Read(buffer)
	body.bytesRead += count
	return count, err
}

func (body *trackingReadCloser) Close() error {
	body.closed = true
	return nil
}

type errorReadCloser struct {
	err error
}

func (body *errorReadCloser) Read([]byte) (int, error) {
	return 0, body.err
}

func (body *errorReadCloser) Close() error {
	return nil
}
