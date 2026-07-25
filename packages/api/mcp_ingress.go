package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
)

const (
	mcpMaxRequestBodyBytes    int64 = 36 << 20
	mcpMaxConcurrentBodyReads       = 4
	mcpBodyReadTimeout              = 25 * time.Second

	mcpBodyTooLargeMessage = "MCP request body exceeds the 36 MiB limit"
)

// mcpIngress bounds the number of raw MCP bodies retained while the SDK
// decodes and handles them. Four 36 MiB slots cap the raw-body portion of that
// working set at roughly 144 MiB; decoded values remain bounded separately by
// the Engine and Tool limits.
type mcpIngress struct {
	maxBodyBytes int64
	readTimeout  time.Duration
	slots        chan struct{}
}

func newMCPIngress() *mcpIngress {
	return newMCPIngressWithLimits(
		mcpMaxRequestBodyBytes,
		mcpMaxConcurrentBodyReads,
		mcpBodyReadTimeout,
	)
}

func newMCPIngressWithLimits(
	maxBodyBytes int64,
	concurrency int,
	readTimeout time.Duration,
) *mcpIngress {
	if maxBodyBytes <= 0 || concurrency <= 0 || readTimeout <= 0 {
		panic("api: invalid MCP ingress limits")
	}
	return &mcpIngress{
		maxBodyBytes: maxBodyBytes,
		readTimeout:  readTimeout,
		slots:        make(chan struct{}, concurrency),
	}
}

// limitBody runs after Session authentication and before the MCP SDK handler.
// The slot remains held until the SDK returns so the bounded raw body cannot
// escape the admission budget during JSON decode.
func (ingress *mcpIngress) limitBody(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		request := c.Request()
		if request.Body == nil || request.Body == http.NoBody {
			return next(c)
		}
		if request.ContentLength > ingress.maxBodyBytes {
			_ = request.Body.Close()
			return writeError(
				c,
				http.StatusRequestEntityTooLarge,
				"input_too_large",
				mcpBodyTooLargeMessage,
			)
		}

		readContext, cancelRead := context.WithTimeout(
			request.Context(),
			ingress.readTimeout,
		)
		defer cancelRead()

		select {
		case ingress.slots <- struct{}{}:
			defer func() { <-ingress.slots }()
		case <-readContext.Done():
			_ = request.Body.Close()
			return writeError(
				c,
				http.StatusRequestTimeout,
				"request_timeout",
				"MCP request body was not received in time",
			)
		}

		limited := http.MaxBytesReader(
			c.Response(),
			request.Body,
			ingress.maxBodyBytes,
		)
		// A context cancellation cannot reliably interrupt a real server-side
		// request-body Read because net/http serializes Read and Close on the
		// connection body. Set the socket read deadline through the response
		// controller; the Close goroutine below remains the fallback for
		// synthetic/custom bodies that do not expose a connection deadline.
		controller := http.NewResponseController(c.Response())
		readDeadlineSet := false
		if deadline, ok := readContext.Deadline(); ok {
			if deadlineErr := controller.SetReadDeadline(deadline); deadlineErr == nil {
				readDeadlineSet = true
			}
		}
		readDone := make(chan struct{})
		closeDone := make(chan struct{})
		go func() {
			defer close(closeDone)
			select {
			case <-readContext.Done():
				_ = limited.Close()
			case <-readDone:
			}
		}()

		body, readErr := io.ReadAll(limited)
		close(readDone)
		<-closeDone
		_ = limited.Close()
		if readDeadlineSet {
			_ = controller.SetReadDeadline(time.Time{})
		}

		var maxBytesErr *http.MaxBytesError
		var netErr net.Error
		switch {
		case errors.As(readErr, &maxBytesErr):
			return writeError(
				c,
				http.StatusRequestEntityTooLarge,
				"input_too_large",
				mcpBodyTooLargeMessage,
			)
		case readContext.Err() != nil ||
			(errors.As(readErr, &netErr) && netErr.Timeout()):
			return writeError(
				c,
				http.StatusRequestTimeout,
				"request_timeout",
				"MCP request body was not received in time",
			)
		case readErr != nil:
			return writeError(
				c,
				http.StatusBadRequest,
				"invalid_request",
				"MCP request body could not be read",
			)
		}

		// Restore a bounded body for the SDK. Keep the caller's original
		// context: the short read deadline must not become a Tool execution
		// deadline.
		request.Body = io.NopCloser(bytes.NewReader(body))
		request.ContentLength = int64(len(body))
		request.TransferEncoding = nil
		c.SetRequest(request)
		cancelRead()
		return next(c)
	}
}
