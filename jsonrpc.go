package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
)

// JSONRPC reserved status codes.
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
		return errors.New("jsonrpcID: UnmarshalJSON on nil pointer")
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
}

type response struct {
	Protocol string      `json:"jsonrpc"`
	ID       jsonrpcID   `json:"id"`
	Result   interface{} `json:"result,omitempty"`
	Error    *Error      `json:"error,omitempty"`
}

// Error represents a JSONRPC error. If an Error is returned from a registered
// function, it will be sent directly to the client. Otherwise a generic Error
// is created.
type Error struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

func (err *Error) Error() string {
	return err.Message
}

// Handler is an http.Handler that responds to JSONRPC requests.
type Handler struct {
	registry map[string]*method
}

// NewHandler initializes a new Handler.
func NewHandler() *Handler {
	return &Handler{registry: make(map[string]*method)}
}

// Register a method with the given name. Methods must be valid functions and
// have the following restrictions/features:
//
// - If the first parameter in the method is a context.Context, then it will be
//   passed the context from the request.
// - If the last return value in the method is an error, then this will be
//   returned as a JSONRPC error.
// - Input parameters will be unmarshaled as JSON.
// - If there is a non-error return value, it will be marshaled as JSON.
// - It is not allowed to have more than one non-error return values.
func (h *Handler) Register(name string, fn interface{}) error {
	m, err := newMethod(name, fn)
	if err != nil {
		return err
	}
	h.registry[name] = m
	return nil
}

// RegisterMany is a convenience function. It will call `Register` on each
// key/value pair of the map. If it encounters an error, it will stop
// and return that error.
func (h *Handler) RegisterMany(many map[string]interface{}) error {
	for name, fn := range many {
		if err := h.Register(name, fn); err != nil {
			return err
		}
	}
	return nil
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
	w.Header().Set("Content-Type", "application/json")

	// Unmarshal the request. We do all the usual checks per the protocol.
	var req request
	res := response{Protocol: "2.0"}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if _, ok := err.(*json.SyntaxError); ok {
			res.Error = &Error{
				Code:    StatusParseError,
				Message: err.Error(),
			}
		} else {
			res.Error = &Error{
				Code:    StatusInvalidRequest,
				Message: err.Error(),
			}
		}
		json.NewEncoder(w).Encode(res)
		return
	}
	res.ID = req.ID

	if req.Protocol != "2.0" {
		res.Error = &Error{
			Code:    StatusInvalidRequest,
			Message: "Invalid protocol: expected jsonrpc: 2.0",
		}
		json.NewEncoder(w).Encode(res)
		return
	}

	m, ok := h.registry[req.Method]
	if !ok {
		res.Error = &Error{
			Code:    StatusMethodNotFound,
			Message: fmt.Sprintf("No such method: %s", req.Method),
		}
		json.NewEncoder(w).Encode(res)
		return
	}

	// Call the method.
	result, err := m.call(r.Context(), req.Params)
	if err != nil {
		// Check for pre-existing JSONRPC errors.
		res.Error, ok = err.(*Error)
		if !ok {
			// Create a generic JSONRPC error.
			res.Error = &Error{
				Code:    StatusInternalError,
				Message: err.Error(),
			}
		}
		json.NewEncoder(w).Encode(res)
		return
	}

	// Encode the result.
	res.Result = result
	json.NewEncoder(w).Encode(res)
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
