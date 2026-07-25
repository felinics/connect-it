package testkit

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"testing"
)

// AssertHeader compares one request header without ever printing other
// headers, which may contain credentials.
func AssertHeader(t testing.TB, request *http.Request, name, want string) {
	t.Helper()
	if request == nil {
		t.Error("request is nil")
		return
	}
	values, present := headerValuesFold(request.Header, name)
	if !present || len(values) != 1 || values[0] != want {
		t.Errorf("header %q did not match the expected value", name)
	}
}

// AssertNoHeader verifies that name is absent.
func AssertNoHeader(t testing.TB, request *http.Request, name string) {
	t.Helper()
	if request == nil {
		t.Error("request is nil")
		return
	}
	if _, present := headerValuesFold(request.Header, name); present {
		t.Errorf("header %q was present", name)
	}
}

func headerValuesFold(
	header http.Header,
	name string,
) (values []string, present bool) {
	for candidate, candidateValues := range header {
		if http.CanonicalHeaderKey(candidate) ==
			http.CanonicalHeaderKey(name) {
			present = true
			values = append(values, candidateValues...)
		}
	}
	return values, present
}

// AssertQuery compares one query value without printing the full URL.
func AssertQuery(t testing.TB, request *http.Request, name, want string) {
	t.Helper()
	if request == nil || request.URL == nil {
		t.Error("request URL is nil")
		return
	}
	values, present := request.URL.Query()[name]
	if !present || len(values) != 1 || values[0] != want {
		t.Errorf("query %q did not match the expected value", name)
	}
}

// DecodeJSONBody decodes exactly one JSON value from a request body.
func DecodeJSONBody(t testing.TB, request *http.Request, destination any) {
	t.Helper()
	body, ok := readBody(t, request)
	if !ok {
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(destination); err != nil {
		t.Errorf("decode JSON request body: %v", err)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Errorf("JSON request body has trailing content: %v", err)
	}
}

// AssertFormBody compares the decoded form body.
func AssertFormBody(
	t testing.TB,
	request *http.Request,
	want url.Values,
) {
	t.Helper()
	body, ok := readBody(t, request)
	if !ok {
		return
	}
	got, err := url.ParseQuery(string(body))
	if err != nil {
		t.Errorf("decode form request body: %v", err)
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Error("form body did not match the expected values")
	}
}

func readBody(t testing.TB, request *http.Request) ([]byte, bool) {
	t.Helper()
	if request == nil || request.Body == nil {
		t.Error("request body is nil")
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, 32<<20+1))
	if err != nil {
		t.Errorf("read request body: %v", err)
		return nil, false
	}
	if len(body) > 32<<20 {
		t.Error("request body exceeds testkit limit")
		return nil, false
	}
	return body, true
}
