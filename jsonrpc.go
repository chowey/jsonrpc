/*
Package jsonrpc implements the JSON-RPC 2.0 specification over HTTP.

Regular functions can be registered to a Handler and then called using
standard JSON-RPC 2.0 semantics. The only limitations on functions are as
follows:

	- the first parameter may be a context.Context
	- the remaining parameters must be able to unmarshal from JSON
	- return values must be (optionally) a value and (optionally) an error
	- if there is a return value, it must be able to marshal as JSON

Here is a simple example of a JSON-RPC 2.0 command that echos its input:

	h := jsonrpc.NewHandler()
	h.RegisterMethod("echo", func (in string) string { return in })
	http.ListenAndServe(":8080", h)

You would call this over HTTP with standard JSON-RPC 2.0 semantics:

	=> {"jsonrpc": "2.0", "id": 1, "method": "echo", "params": ["Hello world!"]}
	<= {"jsonrpc": "2.0", "id": 1, "result": "Hello world!"}

As a convenience, structs may also be registered to a Handler. In this case,
each method of the struct is registered using the method "Type.Method".
For example:

	type Echo struct{}

	func (Echo) Echo(s string) string {
		return s
	}

	func main() {
		e := &Echo{}
		h := jsonrpc.NewHandler()
		h.Register(e)
		http.ListenAndServe(":8080", h)
	}

Then you would call this over HTTP as follows:

	=> {"jsonrpc": "2.0", "id": 1, "method": "Echo.Echo", "params": ["Hello world!"]}
	<= {"jsonrpc": "2.0", "id": 1, "result": "Hello world!"}

As a further convenience, you may pass in one or more structs into the
NewHandler constructor. For example:

	http.ListenAndServe(":8080", jsonrpc.NewHandler(&Echo{}))
*/
package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sync"
)

// JSON-RPC 2.0 reserved status codes.
const (
	StatusParseError     = -32700 // Invalid JSON was received by the server. An error occurred on the server while parsing the JSON text.
	StatusInvalidRequest = -32600 // The JSON sent is not a valid Request object.
	StatusMethodNotFound = -32601 // The method does not exist / is not available.
	StatusInvalidParams  = -32602 // Invalid method parameter(s).
	StatusInternalError  = -32603 // Internal JSON-RPC error.
)

type jsonrpcID []byte

func (m jsonrpcID) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	return m, nil
}

func (m *jsonrpcID) UnmarshalJSON(data []byte) error {
	if m == nil {
		return errors.New("id: UnmarshalJSON on nil pointer")
	}

	// Verify that data is either a string or a number.
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok.(type) {
	case string:
	case float64:
	case nil:
	default:
		// Other types are not allowed for JSONRPC IDs.
		return fmt.Errorf("\"id\" is not a valid type: %s", data)
	}

	*m = append((*m)[0:0], data...)
	return nil
}

type request struct {
	Protocol string          `json:"jsonrpc"`
	ID       jsonrpcID       `json:"id"`
	Method   string          `json:"method"`
	Params   json.RawMessage `json:"params"`

	res response
	m   *method
}

func (req *request) call(ctx context.Context) {
	req.res.Protocol = "2.0"
	req.res.ID = req.ID

	// Call the method.
	result, err := req.m.call(ctx, req.Params)
	if err != nil {
		// Check for pre-existing JSONRPC errors.
		if e, ok := err.(*Error); ok && e != nil {
			req.res.Error = e
			return
		}
		// Create a generic JSONRPC error.
		req.res.Error = &Error{
			Code:    StatusInternalError,
			Message: err.Error(),
		}
		return
	}
	req.res.Result = result
}

type response struct {
	errorResponse
	Result interface{} `json:"result"`
}

type errorResponse struct {
	Protocol string    `json:"jsonrpc"`
	ID       jsonrpcID `json:"id"`
	Error    *Error    `json:"error,omitempty"`
}

// Encoder is something that can encode into JSON.
// By default it is a json.Encoder
type Encoder interface {
	Encode(v interface{}) error
}

// Error represents a JSON-RPC 2.0 error. If an Error is returned from a
// registered function, it will be sent directly to the client.
type Error struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

func (err *Error) Error() string {
	return err.Message
}

// Handler is an http.Handler that responds to JSON-RPC 2.0 requests.
type Handler struct {
	registry       map[string]*method
	encoderFactory func(w io.Writer) Encoder
}

// NewHandler initializes a new Handler. If receivers are provided, they will
// be registered.
func NewHandler(rcvrs ...interface{}) *Handler {
	h := &Handler{registry: make(map[string]*method)}
	for _, rcvr := range rcvrs {
		h.Register(rcvr)
	}
	return h
}

// RegisterMethod registers a method under the given name. Methods must be valid
// functions with the following restrictions:
//
//     - the first parameter may be a context.Context
//     - the remaining parameters must be able to unmarshal from JSON
//     - return values must be (optionally) a value and (optionally) an error
//     - if there is a return value, it must be able to marshal as JSON
//
// If the first parameter is a context.Context, then it will receive the context
// from the HTTP request.
func (h *Handler) RegisterMethod(name string, fn interface{}) {
	m, err := newMethod(name, fn)
	if err != nil {
		panic(err)
	}
	h.registry[name] = m
}

// Register is a convenience function. It will call RegisterMethod on each
// method of the provided receiver. The registered method name will follow the
// pattern "Type.Method".
func (h *Handler) Register(rcvr interface{}) {
	v := reflect.ValueOf(rcvr)
	name := reflect.Indirect(v).Type().Name()
	h.registerName(name, v)
}

// RegisterName is like Register but uses the provided name for the type instead
// of the receiver's concrete type.
func (h *Handler) RegisterName(name string, rcvr interface{}) {
	h.registerName(name, reflect.ValueOf(rcvr))
}

func (h *Handler) registerName(name string, v reflect.Value) {
	t := v.Type()
	for i := 0; i < t.NumMethod(); i++ {
		method := t.Method(i)
		// Method must be exported.
		if method.PkgPath != "" {
			continue
		}
		h.RegisterMethod(name+"."+method.Name, v.Method(method.Index).Interface())
	}
}

// ServeConn provides JSON-RPC over any bi-directional stream.
func (h *Handler) ServeConn(ctx context.Context, rw io.ReadWriter) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var l sync.Mutex
	var buf bytes.Buffer

	var wg sync.WaitGroup
	dec := json.NewDecoder(rw)
	enc := h.newEncoder(&buf)
	send := func(res *response) {
		// Write the entire buffer as a single write, to help e.g. a
		// websocket adapter send it as one frame.
		l.Lock()
		defer l.Unlock()

		var err error
		if res.Error != nil {
			err = enc.Encode(res.errorResponse)
		} else {
			err = enc.Encode(res)
		}
		if err == nil {
			_, err = buf.WriteTo(rw)
			buf.Reset()
		}

		// If write fails, the writer is no longer valid.
		if err != nil {
			cancel()
		}
	}

	for {
		req := new(request)
		if !h.decodeRequest(dec, req) {
			if req.res.Error != nil {
				// Errors will only occur for parse errors, in which case we
				// cannot tell if the request was a notification and the client
				// is not expecting a response. Send the error just to be safe.
				send(&req.res)
			}
			// No more values are available.
			wg.Wait()
			return
		}

		// Start the call in its own goroutine.
		wg.Add(1)
		go func() {
			defer wg.Done()

			if req.res.Error == nil {
				// Call the method.
				req.call(ctx)
			}

			if req.res.ID == nil {
				return
			}

			send(&req.res)
		}()
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Deal with HTTP-level errors.
	if r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "Unsupported Content-Type: must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "Unsupported method: must be POST", http.StatusMethodNotAllowed)
		return
	}

	// All other requests return status OK. Errors are returned as JSONRPC.

	dec := json.NewDecoder(r.Body)
	enc := h.newEncoder(w)

	var req request
	if !h.decodeRequest(dec, &req) && req.res.Error == nil {
		req.res.ID = jsonrpcID("null")
		req.res.Error = &Error{
			Code:    StatusInvalidRequest,
			Message: io.EOF.Error(),
		}
	}
	if req.res.Error == nil {
		// Call the method.
		req.call(r.Context())
	}

	if req.res.ID == nil {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.Header().Set("Content-Type", "application/json")
		if req.res.Error != nil {
			enc.Encode(req.res.errorResponse)
		} else {
			enc.Encode(req.res)
		}
	}
}

// Decode a value into the request. If there was an error, the errorResponse
// will be non-nil. Returns false if there are no more values available from
// the decoder.
func (h *Handler) decodeRequest(dec *json.Decoder, req *request) bool {
	req.res.Protocol = "2.0"

	// Unmarshal the request. We do all the usual checks per the protocol.
	if err := dec.Decode(req); err != nil {
		if err == io.EOF {
			return false
		}
		req.res.ID = jsonrpcID("null")
		if _, ok := err.(*json.SyntaxError); ok {
			req.res.Error = &Error{
				Code:    StatusParseError,
				Message: err.Error(),
			}
			return false
		}
		// There was some other read error.
		req.res.Error = &Error{
			Code:    StatusInvalidRequest,
			Message: err.Error(),
		}
		return false
	}

	req.res.ID = req.ID
	if req.Protocol != "2.0" {
		req.res.Error = &Error{
			Code:    StatusInvalidRequest,
			Message: "Invalid protocol: expected jsonrpc: 2.0",
		}
		return true
	}

	m, ok := h.registry[req.Method]
	if !ok {
		req.res.Error = &Error{
			Code:    StatusMethodNotFound,
			Message: fmt.Sprintf("No such method: %s", req.Method),
		}
		return true
	}
	req.m = m
	return true
}

func (h *Handler) newEncoder(w io.Writer) Encoder {
	if h.encoderFactory == nil {
		return json.NewEncoder(w)
	}
	return h.encoderFactory(w)
}

// SetEncoderFactory configures what encoder will be loaded for sending JSON-RPC
// responses. By default the Handler will use json.NewEncoder.
func (h *Handler) SetEncoderFactory(fn func(w io.Writer) Encoder) {
	h.encoderFactory = fn
}

var (
	contextType = reflect.TypeOf((*context.Context)(nil)).Elem()
	errorType   = reflect.TypeOf((*error)(nil)).Elem()
	zeroValue   = reflect.Value{}
)

type method struct {
	reflect.Value
	name string

	hasContext bool
	nargs      int
	ins        []reflect.Type
	variadic   reflect.Type

	hasError    bool
	hasResponse bool
}

func newMethod(name string, fn interface{}) (*method, error) {
	m := &method{Value: reflect.ValueOf(fn), name: name}
	if m.Kind() != reflect.Func {
		return nil, fmt.Errorf("%s: cannot use type as a method: %T", name, fn)
	}
	t := m.Type()

	// Prepare "In" types.
	m.nargs = t.NumIn()
	m.ins = make([]reflect.Type, m.nargs)
	for i := range m.ins {
		m.ins[i] = t.In(i)
	}

	// If the first argument is a context.Context, then it is never unmarshaled
	// from JSON.
	if m.nargs > 0 && m.ins[0] == contextType {
		m.hasContext = true
		m.ins = m.ins[1:]
		m.nargs--
	}

	// If the function is variadic, then the last argument is actually a slice
	// type. We want the type of the slice element.
	if t.IsVariadic() {
		m.variadic = m.ins[len(m.ins)-1].Elem()
		m.ins = m.ins[:len(m.ins)-1]
		m.nargs--
	}

	// Check if the function returns an error.
	i := t.NumOut() - 1
	if i >= 0 && t.Out(i).Implements(errorType) {
		m.hasError = true
		i--
	}

	// Check if the function returns a result.
	if i >= 0 {
		m.hasResponse = true
		i--
	}

	// Check if there are more return arguments. If so, this is illegal.
	if i >= 0 {
		return nil, fmt.Errorf("%s: too many output arguments for method: %T", name, fn)
	}

	return m, nil
}

func (m *method) call(ctx context.Context, params json.RawMessage) (result interface{}, err error) {
	// Prepare raw arguments.
	var args []json.RawMessage
	if len(params) > 0 && string(params) != "null" {
		// Params may be an array or an object.
		if err := json.Unmarshal(params, &args); err != nil {
			args = []json.RawMessage{params}
		}
	}

	// Verify the correct number of arguments.
	if m.variadic != nil {
		if len(args) < m.nargs {
			return nil, &Error{
				Code:    StatusInvalidParams,
				Message: fmt.Sprintf("%s: require at least %d params", m.name, m.nargs),
			}
		}
	} else if len(args) != m.nargs {
		return nil, &Error{
			Code:    StatusInvalidParams,
			Message: fmt.Sprintf("%s: require %d params", m.name, m.nargs),
		}
	}

	// Unmarshal the params.
	var ins, provided []reflect.Value
	if m.hasContext {
		ins = make([]reflect.Value, len(args)+1)
		ins[0] = reflect.ValueOf(ctx)
		provided = ins[1:]
	} else {
		ins = make([]reflect.Value, len(args))
		provided = ins
	}
	for i := range provided {
		var t reflect.Type
		if i < m.nargs {
			t = m.ins[i]
		} else {
			t = m.variadic
		}
		v := reflect.New(t)
		if err := json.Unmarshal(args[i], v.Interface()); err != nil {
			return nil, &Error{
				Code:    StatusInvalidParams,
				Message: fmt.Sprintf("%s: %v", m.name, err),
				Data:    args[i],
			}
		}
		provided[i] = v.Elem()
	}

	// Call the function.
	outs := m.Call(ins)

	// Report error (if any).
	if m.hasError {
		verr := outs[len(outs)-1]
		if !verr.IsNil() {
			return nil, verr.Interface().(error)
		}
	}

	// Report response (if any).
	if m.hasResponse {
		return outs[0].Interface(), nil
	}

	// Otherwise no response.
	return nil, nil
}
