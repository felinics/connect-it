package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/labstack/echo/v4"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/connect-it/packages/service/sessions"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

func TestMCPIngressBodyBoundaryBeforeSDK(t *testing.T) {
	t.Run("exactly 36 MiB reaches SDK", func(t *testing.T) {
		ingress := newMCPIngress()
		var nextCalled bool
		next := ingress.limitBody(func(c echo.Context) error {
			nextCalled = true
			if got := c.Request().ContentLength; got != mcpMaxRequestBodyBytes {
				t.Fatalf("rebuilt ContentLength = %d", got)
			}
			return c.NoContent(http.StatusNoContent)
		})
		request := httptest.NewRequest(
			http.MethodPost,
			"/mcp",
			&fixedByteReader{remaining: mcpMaxRequestBodyBytes},
		)
		request.ContentLength = -1
		recorder := httptest.NewRecorder()

		if err := next(newEchoContext(request, recorder)); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusNoContent || !nextCalled {
			t.Fatalf("status=%d nextCalled=%v", recorder.Code, nextCalled)
		}
		if got := len(ingress.slots); got != 0 {
			t.Fatalf("slots held after downstream return = %d", got)
		}
	})

	t.Run("36 MiB plus one byte returns stable 413", func(t *testing.T) {
		ingress := newMCPIngress()
		var nextCalled bool
		next := ingress.limitBody(func(echo.Context) error {
			nextCalled = true
			return nil
		})
		request := httptest.NewRequest(
			http.MethodPost,
			"/mcp",
			&fixedByteReader{remaining: mcpMaxRequestBodyBytes + 1},
		)
		request.ContentLength = -1
		recorder := httptest.NewRecorder()

		if err := next(newEchoContext(request, recorder)); err != nil {
			t.Fatal(err)
		}
		assertIngressError(t, recorder, http.StatusRequestEntityTooLarge, "input_too_large")
		if nextCalled {
			t.Fatal("oversize body reached SDK handler")
		}
		if got := len(ingress.slots); got != 0 {
			t.Fatalf("slots held after 413 = %d", got)
		}
	})
}

func TestMCPIngressRejectsDeclaredOversizeWithoutReadingOrAcquiring(t *testing.T) {
	ingress := newMCPIngress()
	body := &trackingReadCloser{reader: bytes.NewReader([]byte("{}"))}
	request := httptest.NewRequest(http.MethodPost, "/mcp", body)
	request.ContentLength = mcpMaxRequestBodyBytes + 1
	recorder := httptest.NewRecorder()
	var nextCalled bool

	err := ingress.limitBody(func(echo.Context) error {
		nextCalled = true
		return nil
	})(newEchoContext(request, recorder))
	if err != nil {
		t.Fatal(err)
	}
	assertIngressError(t, recorder, http.StatusRequestEntityTooLarge, "input_too_large")
	if nextCalled || body.reads.Load() != 0 || !body.closed.Load() {
		t.Fatalf(
			"next=%v reads=%d closed=%v",
			nextCalled,
			body.reads.Load(),
			body.closed.Load(),
		)
	}
	if got := len(ingress.slots); got != 0 {
		t.Fatalf("declared oversize acquired slots = %d", got)
	}
}

func TestMCPIngressAcquireFailureDoesNotConsumeSlot(t *testing.T) {
	ingress := newMCPIngressWithLimits(1024, 1, time.Second)
	ingress.slots <- struct{}{} // Simulate an admitted request.
	body := &trackingReadCloser{reader: bytes.NewReader([]byte("{}"))}
	request := httptest.NewRequest(http.MethodPost, "/mcp", body)
	ctx, cancel := context.WithCancel(request.Context())
	cancel()
	request = request.WithContext(ctx)
	recorder := httptest.NewRecorder()

	err := ingress.limitBody(func(echo.Context) error {
		t.Fatal("request without a slot reached downstream")
		return nil
	})(newEchoContext(request, recorder))
	if err != nil {
		t.Fatal(err)
	}
	assertIngressError(t, recorder, http.StatusRequestTimeout, "request_timeout")
	if got := len(ingress.slots); got != 1 {
		t.Fatalf("acquire failure changed occupied slots to %d", got)
	}
	if !body.closed.Load() {
		t.Fatal("body was not closed after acquire failure")
	}
	<-ingress.slots
	if got := len(ingress.slots); got != 0 {
		t.Fatalf("test slot cleanup failed: %d", got)
	}
}

func TestMCPIngressReadTimeoutClosesBodyAndReleasesSlot(t *testing.T) {
	ingress := newMCPIngressWithLimits(1024, 1, 20*time.Millisecond)
	body := newBlockingReadCloser()
	request := httptest.NewRequest(http.MethodPost, "/mcp", body)
	recorder := httptest.NewRecorder()
	done := make(chan error, 1)

	go func() {
		done <- ingress.limitBody(func(echo.Context) error {
			return errors.New("blocked body unexpectedly reached downstream")
		})(newEchoContext(request, recorder))
	}()
	<-body.readStarted
	if got := len(ingress.slots); got != 1 {
		t.Fatalf("reading request held %d slots, want 1", got)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("body read deadline did not unblock request")
	}
	assertIngressError(t, recorder, http.StatusRequestTimeout, "request_timeout")
	if !body.closed.Load() {
		t.Fatal("timed out read did not close body")
	}
	if got := len(ingress.slots); got != 0 {
		t.Fatalf("slot held after timed out read = %d", got)
	}

	// Prove the released slot is reusable without a timing-based assertion.
	nextRecorder := httptest.NewRecorder()
	nextRequest := httptest.NewRequest(
		http.MethodPost,
		"/mcp",
		bytes.NewReader([]byte("{}")),
	)
	if err := ingress.limitBody(func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})(newEchoContext(nextRequest, nextRecorder)); err != nil {
		t.Fatal(err)
	}
	if nextRecorder.Code != http.StatusNoContent {
		t.Fatalf("request after read timeout status = %d", nextRecorder.Code)
	}
}

func TestMCPIngressReadDeadlineInterruptsRealSlowSocket(t *testing.T) {
	ingress := newMCPIngressWithLimits(1024, 1, 75*time.Millisecond)
	echoServer := echo.New()
	echoServer.POST("/mcp", ingress.limitBody(func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	}))
	httpServer := httptest.NewServer(echoServer)
	t.Cleanup(httpServer.Close)

	address := strings.TrimPrefix(httpServer.URL, "http://")
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}

	startedAt := time.Now()
	_, err = io.WriteString(
		connection,
		"POST /mcp HTTP/1.1\r\n"+
			"Host: "+address+"\r\n"+
			"Content-Type: application/json\r\n"+
			"Content-Length: 64\r\n"+
			"Connection: close\r\n\r\n"+
			"{",
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(
		bufio.NewReader(connection),
		&http.Request{Method: http.MethodPost},
	)
	if err != nil {
		t.Fatalf("read timeout response: %v", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusRequestTimeout {
		t.Fatalf(
			"status = %d body = %q",
			response.StatusCode,
			responseBody,
		)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("slow socket was interrupted after %s, want under 1s", elapsed)
	}
	if got := len(ingress.slots); got != 0 {
		t.Fatalf("slot held after real socket timeout = %d", got)
	}
}

func TestMCPIngressRunsAfterSessionAuthentication(t *testing.T) {
	host := &mcpHost{}
	ingress := newMCPIngressWithLimits(1, 1, time.Second)
	var sdkCalled bool
	endpoint := host.requireSession(ingress.limitBody(func(echo.Context) error {
		sdkCalled = true
		return nil
	}))
	request := httptest.NewRequest(
		http.MethodPost,
		"/mcp",
		bytes.NewReader([]byte("{}")),
	)
	recorder := httptest.NewRecorder()

	if err := endpoint(newEchoContext(request, recorder)); err != nil {
		t.Fatal(err)
	}
	assertIngressError(t, recorder, http.StatusUnauthorized, "unauthorized")
	if sdkCalled || len(ingress.slots) != 0 {
		t.Fatalf(
			"unauthenticated request reached SDK/admission: sdk=%v slots=%d",
			sdkCalled,
			len(ingress.slots),
		)
	}
}

func TestMCPRequireSessionCanceledContextIsStableInternalError(t *testing.T) {
	pool := testutil.NewDB(t)
	host := &mcpHost{sessions: sessions.New(store.New(pool), nil)}
	var nextCalled bool
	endpoint := host.requireSession(func(echo.Context) error {
		nextCalled = true
		return nil
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/mcp",
		bytes.NewReader([]byte("{}")),
	)
	request.Header.Set("Authorization", "Bearer any-token")
	ctx, cancel := context.WithCancel(request.Context())
	cancel()
	request = request.WithContext(ctx)
	recorder := httptest.NewRecorder()
	logs := new(bytes.Buffer)
	echoServer := echo.New()
	echoServer.Logger.SetOutput(logs)

	if err := endpoint(echoServer.NewContext(request, recorder)); err != nil {
		t.Fatal(err)
	}
	assertIngressError(t, recorder, http.StatusInternalServerError, "internal")
	if nextCalled {
		t.Fatal("canceled session resolution reached downstream handler")
	}
	if !strings.Contains(logs.String(), "mcp session resolution failed") ||
		!strings.Contains(logs.String(), context.Canceled.Error()) {
		t.Fatalf("canceled context was not observable: %q", logs.String())
	}
}

func TestMCPIngressReleasesSlotWhenDownstreamReturnsError(t *testing.T) {
	ingress := newMCPIngressWithLimits(1024, 1, time.Second)
	wantErr := errors.New("downstream failed")
	request := httptest.NewRequest(
		http.MethodPost,
		"/mcp",
		bytes.NewReader([]byte("{}")),
	)
	recorder := httptest.NewRecorder()

	err := ingress.limitBody(func(echo.Context) error {
		if got := len(ingress.slots); got != 1 {
			t.Fatalf("downstream held %d slots, want 1", got)
		}
		return wantErr
	})(newEchoContext(request, recorder))
	if !errors.Is(err, wantErr) {
		t.Fatalf("downstream error = %v, want %v", err, wantErr)
	}
	if got := len(ingress.slots); got != 0 {
		t.Fatalf("slot held after downstream error = %d", got)
	}
}

func TestMCPIngressAdmissionHasFourProductionSlots(t *testing.T) {
	ingress := newMCPIngress()
	if got := cap(ingress.slots); got != 4 {
		t.Fatalf("production MCP admission slots = %d, want 4", got)
	}
	if got := ingress.maxBodyBytes * int64(cap(ingress.slots)); got != 144<<20 {
		t.Fatalf("raw body admission budget = %d, want 144 MiB", got)
	}
}

func TestMCPIngressAllowsLegalSDKBodyAboveOneMiB(t *testing.T) {
	var receivedBytes atomic.Int64
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server {
			server := mcp.NewServer(
				&mcp.Implementation{Name: "ingress-test", Version: "1"},
				nil,
			)
			server.AddTool(
				&mcp.Tool{
					Name:        "large_input",
					InputSchema: &jsonschema.Schema{Type: "object"},
				},
				func(
					_ context.Context,
					request *mcp.CallToolRequest,
				) (*mcp.CallToolResult, error) {
					receivedBytes.Store(int64(len(request.Params.Arguments)))
					return &mcp.CallToolResult{
						Content: []mcp.Content{
							&mcp.TextContent{Text: "ok"},
						},
					}, nil
				},
			)
			return server
		},
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
	echoServer := echo.New()
	echoServer.Any(
		"/mcp",
		newMCPIngress().limitBody(echo.WrapHandler(handler)),
	)
	httpServer := httptest.NewServer(echoServer)
	t.Cleanup(httpServer.Close)

	client := mcp.NewClient(
		&mcp.Implementation{Name: "ingress-client", Version: "1"},
		nil,
	)
	session, err := client.Connect(
		context.Background(),
		&mcp.StreamableClientTransport{
			Endpoint: httpServer.URL + "/mcp",
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	argumentValue := map[string]string{
		"padding": strings.Repeat("x", (1<<20)+4096),
	}
	arguments, err := json.Marshal(argumentValue)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(
		context.Background(),
		&mcp.CallToolParams{
			Name:      "large_input",
			Arguments: argumentValue,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("large legal SDK call failed: %+v", result)
	}
	if got := receivedBytes.Load(); got != int64(len(arguments)) ||
		got <= 1<<20 {
		t.Fatalf(
			"SDK received %d argument bytes, want %d (>1 MiB)",
			got,
			len(arguments),
		)
	}
}

func newEchoContext(
	request *http.Request,
	recorder *httptest.ResponseRecorder,
) echo.Context {
	return echo.New().NewContext(request, recorder)
}

func assertIngressError(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	wantStatus int,
	wantCode string,
) {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d body = %q", recorder.Code, recorder.Body)
	}
	var response ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("error response is not JSON: %v", err)
	}
	if response.Error != wantCode || response.Message == "" {
		t.Fatalf("error response = %#v", response)
	}
}

type fixedByteReader struct {
	remaining int64
}

func (reader *fixedByteReader) Read(buffer []byte) (int, error) {
	if reader.remaining == 0 {
		return 0, io.EOF
	}
	count := int64(len(buffer))
	if count > reader.remaining {
		count = reader.remaining
	}
	for index := range buffer[:count] {
		buffer[index] = 'x'
	}
	reader.remaining -= count
	return int(count), nil
}

type trackingReadCloser struct {
	reader io.Reader
	reads  atomic.Int64
	closed atomic.Bool
}

func (body *trackingReadCloser) Read(buffer []byte) (int, error) {
	body.reads.Add(1)
	return body.reader.Read(buffer)
}

func (body *trackingReadCloser) Close() error {
	body.closed.Store(true)
	return nil
}

type blockingReadCloser struct {
	readStarted chan struct{}
	unblock     chan struct{}
	startOnce   sync.Once
	closeOnce   sync.Once
	closed      atomic.Bool
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{
		readStarted: make(chan struct{}),
		unblock:     make(chan struct{}),
	}
}

func (body *blockingReadCloser) Read([]byte) (int, error) {
	body.startOnce.Do(func() { close(body.readStarted) })
	<-body.unblock
	return 0, io.ErrClosedPipe
}

func (body *blockingReadCloser) Close() error {
	body.closed.Store(true)
	body.closeOnce.Do(func() { close(body.unblock) })
	return nil
}
