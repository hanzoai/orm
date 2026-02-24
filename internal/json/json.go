// Package json provides JSON encoding/decoding utilities.
package json

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// isDevelopment checks if we're in development mode.
var isDevelopment = func() bool {
	env := os.Getenv("HANZO_ENV")
	if env == "" {
		env = os.Getenv("GAE_ENV")
	}
	return env == "" || strings.ToLower(env) == "development" || strings.ToLower(env) == "dev"
}()

// Encode marshals a value to a JSON string.
func Encode(value interface{}) string {
	return string(EncodeBytes(value))
}

// EncodeBytes marshals a value to JSON bytes.
func EncodeBytes(value interface{}) []byte {
	var b []byte
	var err error

	if isDevelopment {
		b, err = json.MarshalIndent(value, "", "  ")
	} else {
		b, err = json.Marshal(value)
	}

	if err != nil {
		fmt.Printf("json encode error: %v\n", err)
	}
	return b
}

// EncodeIndentBytes marshals with custom indentation.
func EncodeIndentBytes(value interface{}, prefix, indent string) []byte {
	b, err := json.MarshalIndent(value, prefix, indent)
	if err != nil {
		fmt.Printf("json encode error: %v\n", err)
	}
	return b
}

// EncodeRaw marshals to json.RawMessage.
func EncodeRaw(value interface{}) json.RawMessage {
	return json.RawMessage(EncodeBytes(value))
}

// EncodeBuffer marshals to a bytes.Buffer.
func EncodeBuffer(value interface{}) *bytes.Buffer {
	return bytes.NewBuffer(EncodeBytes(value))
}

// Decode reads and unmarshals from an io.ReadCloser.
func Decode(body io.ReadCloser, dst interface{}) error {
	data, err := io.ReadAll(body)
	body.Close()
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

// DecodeBytes unmarshals from bytes.
func DecodeBytes(data []byte, dst interface{}) error {
	return json.Unmarshal(data, dst)
}

// DecodeBuffer unmarshals from a buffer.
func DecodeBuffer(buf *bytes.Buffer, dst interface{}) error {
	return json.Unmarshal(buf.Bytes(), dst)
}

// Unmarshal is an alias for json.Unmarshal.
var Unmarshal = json.Unmarshal
