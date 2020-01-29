package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	alt_json "github.com/helloeave/json"
)

type Echoer struct{}

func (Echoer) Echo(s string) string {
	return s
}

func (Echoer) DelayEcho(s string, ms int) string {
	time.Sleep(time.Duration(ms) * time.Millisecond)
	return s
}

func TestJSONRPC(t *testing.T) {
	ctx := context.WithValue(context.Background(), "data", "Hello world!")

	// Test some sample methods.
	h := NewHandler(&Echoer{})

	h.RegisterMethod("echo", func(s string) string {
		return s
	})
	h.RegisterMethod("multiecho", func(s ...string) string {
		return strings.Join(s, " ")
	})
	h.RegisterMethod("ctx.data", func(ctx context.Context) (string, error) {
		return ctx.Value("data").(string), nil
	})
	h.RegisterMethod("nil.error", func() (string, error) {
		var err *Error
		return "Hello world!", err
	})
	h.RegisterMethod("nil.result", func() {})
	h.RegisterMethod("error", func(s string) error {
		return errors.New(s)
	})
	h.RegisterMethod("prefixecho", func(prefix string, s ...string) string {
		return prefix + strings.Join(s, " ")
	})
	h.RegisterMethod("chan", func(c chan int) {})

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
		}`, ``},
		{`{
			"jsonrpc": "2.0",
			"id": null,
			"method": "echo",
			"params": "Hello world!"
		}`, `{
			"jsonrpc": "2.0",
			"id": null,
			"result": "Hello world!"
		}`},
		{`{
			"jsonrpc": "2.0",
			"method": "Echoer.Echo",
			"params": "Hello world!"
		}`, ``},
		{`{
			"jsonrpc": "2.0",
			"id": null,
			"method": "Echoer.Echo",
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
		{`{
			"jsonrpc": "2.0",
			"method": "nil.result"
		}`, ``},
		{`{
			"jsonrpc": "2.0",
			"id": null,
			"method": "nil.result"
		}`, `{
			"jsonrpc": "2.0",
			"id": null,
			"result": null
		}`},
		{`{
			"jsonrpc": "2.0",
			"id": null,
			"method": "error",
			"params": ["custom error"]
		}`, `{
			"jsonrpc": "2.0",
			"id": null,
			"error": {
				"code": -32603,
				"message": "custom error",
				"data": null
			}
		}`},

		// Test error cases.
		{``, `{
			"jsonrpc": "2.0",
			"id": null,
			"error": {
				"code": -32600,
				"message": "EOF",
				"data": null
			}
		}`},
		{`{
			jsonrpc: "2.0",
			id: null,
			method: "echo",
			params: "Hello world!"
		}`, `{
			"jsonrpc": "2.0",
			"id": null,
			"error": {
				"code": -32700,
				"message": "invalid character 'j' looking for beginning of object key string",
				"data": null
			}
		}`},
		{`{
			"jsonrpc": "2.0",
			"id": null,
			"method": "unknown"
		}`, `{
			"jsonrpc": "2.0",
			"id": null,
			"error": {
				"code": -32601,
				"message": "No such method: unknown",
				"data": null
			}
		}`},
		{`{
			"jsonrpc": "1.0",
			"id": null,
			"method": "echo",
			"params": "Hello world!"
		}`, `{
			"jsonrpc": "2.0",
			"id": null,
			"error": {
				"code": -32600,
				"message": "Invalid protocol: expected jsonrpc: 2.0",
				"data": null
			}
		}`},
		{`{
			"jsonrpc": "2.0",
			"id": null,
			"method": "echo",
			"params": ["Hello", "world!"]
		}`, `{
			"jsonrpc": "2.0",
			"id": null,
			"error": {
				"code": -32602,
				"message": "echo: require 1 params",
				"data": null
			}
		}`},
		{`{
			"jsonrpc": "2.0",
			"id": null,
			"method": "prefixecho",
			"params": []
		}`, `{
			"jsonrpc": "2.0",
			"id": null,
			"error": {
				"code": -32602,
				"message": "prefixecho: require at least 1 params",
				"data": null
			}
		}`},
		{`{
			"jsonrpc": "2.0",
			"id": null,
			"method": "chan",
			"params": "Hello world!"
		}`, `{
			"jsonrpc": "2.0",
			"id": null,
			"error": {
				"code": -32602,
				"message": "chan: json: cannot unmarshal string into Go value of type chan int",
				"data": "Hello world!"
			}
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

	(func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("nil *jsonrpc.Error did not panic")
			}
		}()
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{
			"jsonrpc": "2.0",
			"id": 3,
			"method": "nil.error"
		}`))
		req = req.WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
	})()
}

func expectJSON(t *testing.T, in *bytes.Buffer, expected string) {
	if expected == "" {
		got := in.String()
		if got != "" {
			t.Fatalf("expected no response, got: %s", got)
		}
		return
	}

	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(expected)); err != nil {
		t.Fatalf("parsing expected: %s\nencountered error: %v", expected, err)
	}
	want := buf.String()

	buf.Reset()
	if err := json.Compact(&buf, in.Bytes()); err != nil {
		t.Fatalf("parsing response: %s\nencountered error: %v", in.String(), err)
	}
	got := buf.String()

	if got != want {
		t.Fatalf("expected: %s\ngot: %s", want, got)
	}
}

func TestAlternateEncoder(t *testing.T) {

	type container struct {
		Slice []string
	}

	h := NewHandler()
	h.RegisterMethod("foo", func() container {
		return container{}
	})

	h.RegisterMethod("bar", func() container {
		return container{[]string{"hello", "world"}}
	})

	h2 := NewHandler()
	h2.SetEncoderFactory(func(w io.Writer) Encoder {
		enc := alt_json.NewEncoder(w)
		enc.SetNilSafeCollection(true)
		return enc
	})
	h2.RegisterMethod("foo", func() container {
		return container{}
	})

	// Prepare test cases.
	type compare struct {
		In   string
		Out  string
		Dest http.Handler
	}
	for i, c := range []compare{
		{`{
			"jsonrpc": "2.0",
			"method": "foo",
			"params": null
		}`, ``, h},
		{`{
			"jsonrpc": "2.0",
			"id": null,
			"method": "foo",
			"params": null
		}`, `{
			"jsonrpc": "2.0",
			"id": null,
			"result": {"Slice": null}
		}`, h},
		{`{
			"jsonrpc": "2.0",
			"id": null,
			"method": "bar",
			"params": null
		}`, `{
			"jsonrpc": "2.0",
			"id": null,
			"result": {"Slice": ["hello", "world"]}
		}`, h},
		{`{
			"jsonrpc": "2.0",
			"id": null,
			"method": "foo",
			"params": null
		}`, `{
			"jsonrpc": "2.0",
			"id": null,
			"result": {"Slice": []}
		}`, h2},
	} {
		req := httptest.NewRequest("POST", "/", strings.NewReader(c.In))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		c.Dest.ServeHTTP(w, req)
		t.Logf("Running test %d", i)
		expectJSON(t, w.Body, c.Out)
	}
}

func TestBidirectional(t *testing.T) {
	h := NewHandler(Echoer{})

	var buf bytes.Buffer
	pr, pw := io.Pipe()
	stream := struct {
		io.Reader
		io.Writer
	}{pr, &buf}

	go func() {
		pw.Write([]byte(`{
			"jsonrpc": "2.0",
			"id": 1,
			"method": "Echoer.DelayEcho",
			"params": ["Hello world!", 200]
		}`))
		pw.Write([]byte(`{
			"jsonrpc": "2.0",
			"id": 2,
			"method": "Echoer.DelayEcho",
			"params": ["Hello world!", 100]
		}`))
		pw.Write([]byte(`{
			"jsonrpc": "2.0",
			"method": "Echoer.Echo",
			"params": ["Notification"]
		}`))
		pw.Write([]byte(`{
			"jsonrpc": "2.0",
			"id": null,
			"method": "error",
			"params": ["Error"]
		}`))
		pw.Close()
	}()
	h.ServeConn(context.Background(), stream)

	got := buf.String()
	want := `{"jsonrpc":"2.0","id":null,"error":{"code":-32601,"message":"No such method: error","data":null}}
{"jsonrpc":"2.0","id":2,"result":"Hello world!"}
{"jsonrpc":"2.0","id":1,"result":"Hello world!"}
`
	if got != want {
		t.Fatalf("expected: %s\ngot: %s", want, got)
	}
}
