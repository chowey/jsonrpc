# JSON-RPC

[![GoDoc](https://godoc.org/github.com/chowey/jsonrpc?status.svg)](https://godoc.org/github.com/chowey/jsonrpc)

Standards-compliant JSON-RPC for go.

## How to use

Create a new Handler and register methods to it. A method is any Go function with certain restrictions:

* The method may contain a `context.Context` as its first argument.
* The method must only have JSON-serializable arguments otherwise.
* The method may return a JSON-serializable object as its first return value.
* The method may return an error as its last return value.
* The method must not have any other return values.

The JSON-RPC handler only responds to POST requests.

```go
h := jsonrpc.NewHandler()
h.Register("echo", func (in string) string {
	return in
})
http.ListenAndServe(":8080", h)
```

## Context

A JSON-RPC method has access to the `context.Context` from the `http.Request`. A common use case is to use middleware to attach security credentials to the context, and then extract those credentials in the JSON-RPC method to validate the user's authorization or return user-specific data.

```go
h.Register("secure", func(ctx context.Context) (string, error) {
	_, ok := ctx.Value("secure-cookie")
	if !ok {
		return "", fmt.Errorf("Invalid credentials.")
	}
	return "Top secret data", nil
})
```

## JSON-RPC Errors

If you want to provide a JSON-RPC error, use the `Error` struct. This lets you provide a custom error code and custom data.

```go
h.Register("bad", func (data json.RawMessage) error {
	return &jsonrpc.Error{
		Code: 101,
		Message: "This endpoint is bad.",
		Data: data,
	}
})
```

## Multiple Registration

For convenience, you may register many methods at once using `RegisterMany`.

```go
a := SomeNewApi()
h.RegisterMany(map[string]interface{}{
	"echo": a.Echo,
	"secure": a.Secure,
	"bad": a.Bad,
})
```

## Motivation

When used this way, JSON-RPC endpoints become self-documenting. They correspond exactly to their go functions. They are testable.

This also allows almost any go function to be used (unlike the built-in `rpc/jsonrpc` package in the Go standard library).

JSON-RPC is a very simple standard that is well-defined.
