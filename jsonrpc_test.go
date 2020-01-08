package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSONRPC(t *testing.T) {
	ctx := context.WithValue(context.Background(), "data", "Hello world!")

	// Test some sample methods.
	h := NewHandler()

	if err := h.Register("echo", func(s string) string {
		return s
	}); err != nil {
		t.Fatal(err)
	}

	if err := h.Register("multiecho", func(s ...string) string {
		return strings.Join(s, " ")
	}); err != nil {
		t.Fatal(err)
	}

	if err := h.Register("ctx.data", func(ctx context.Context) (string, error) {
		return ctx.Value("data").(string), nil
	}); err != nil {
		t.Fatal(err)
	}

	// Prepare test cases.
	type compare struct {
		In  string
		Out string
	}
	for i, c := range []compare{
		{`{
			"jsonrpc": "2.0",
			"method": "echo",
			"params": "Hello world!"
		}`, `{
			"jsonrpc": "2.0",
			"id": null,
			"result": "Hello world!"
		}`},
		{`{
			"jsonrpc": "2.0",
			"id": "1",
			"method": "multiecho",
			"params": ["Hello", "world!"]
		}`, `{
			"jsonrpc": "2.0",
			"id": "1",
			"result": "Hello world!"
		}`},
		{`{
			"jsonrpc": "2.0",
			"id": 2,
			"method": "ctx.data"
		}`, `{
			"jsonrpc": "2.0",
			"id": 2,
			"result": "Hello world!"
		}`},
	} {
		req := httptest.NewRequest("POST", "/", strings.NewReader(c.In))
		req = req.WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		t.Logf("Running test %d", i)
		expectJSON(t, w.Body, c.Out)
	}
}

func expectJSON(t *testing.T, in *bytes.Buffer, expected string) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(expected)); err != nil {
		t.Fatal(err, expected)
	}
	want := buf.String()

	buf.Reset()
	if err := json.Compact(&buf, in.Bytes()); err != nil {
		t.Fatal(err, in.String())
	}
	got := buf.String()

	if got != want {
		t.Fatalf("expected: %s\ngot: %s", want, got)
	}
}
