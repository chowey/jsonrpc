# jsonrpc

[![GoDoc](https://godoc.org/github.com/chowey/jsonrpc?status.svg)](https://godoc.org/github.com/chowey/jsonrpc)
![Go](https://github.com/chowey/jsonrpc/workflows/Go/badge.svg)
[![Go Report Card](https://goreportcard.com/badge/github.com/chowey/jsonrpc)](https://goreportcard.com/report/github.com/chowey/jsonrpc)

Standards-compliant JSON-RPC 2.0 over HTTP for Go.

## How to use

Create a new Handler and register methods to it. A method is any Go function with certain restrictions:

* The method may contain a `context.Context` as its first argument.
* The method must only have JSON-serializable arguments otherwise.
* The method may return a JSON-serializable object as its first return value.
* The method may return an error as its last return value.
* The method must not have any other return values.

The JSON-RPC 2.0 handler only responds to POST requests.

```go
h := jsonrpc.NewHandler()
h.RegisterMethod("echo", func (in string) string {
	return in
})
http.ListenAndServe(":8080", h)
```

## Context

A method has access to the `context.Context` from the `http.Request`. A common use case is to use middleware to attach security credentials to the context, and then extract those credentials in the method to validate the user's authorization or return user-specific data.

```go
h.RegisterMethod("secure", func(ctx context.Context) (string, error) {
	_, ok := ctx.Value("secure-cookie")
	if !ok {
		return "", fmt.Errorf("Invalid credentials.")
	}
	return "Top secret data", nil
})
```

## JSON-RPC Errors

If you want to provide a JSON-RPC 2.0 error, use the `Error` struct. This lets you provide a custom error code and custom data.

```go
h.RegisterMethod("bad", func (data json.RawMessage) error {
	return &jsonrpc.Error{
		Code: 101,
		Message: "This endpoint is bad.",
		Data: data,
	}
})
```

## Multiple Registration

For convenience, you may register all methods on a value at once using `Register`.

```go
a := SomeNewApi()
h.Register(a)
```

## Motivation

When used this way, JSON-RPC 2.0 endpoints become self-documenting. They correspond exactly to their Go functions. They are testable.

This also allows almost any Go function to be used (unlike the built-in `rpc/jsonrpc` package in the Go standard library).
