package engine

import (
	"reflect"
	"testing"
	"time"
)

func TestScanValue_String(t *testing.T) {
	var s string
	v := reflect.ValueOf(&s).Elem()

	if err := scanValue("hello", v); err != nil {
		t.Fatal(err)
	}
	if s != "hello" {
		t.Errorf("got %q, want %q", s, "hello")
	}
}

func TestScanValue_Int(t *testing.T) {
	var n int64
	v := reflect.ValueOf(&n).Elem()

	if err := scanValue(int64(42), v); err != nil {
		t.Fatal(err)
	}
	if n != 42 {
		t.Errorf("got %d, want %d", n, 42)
	}
}

func TestScanValue_Bool(t *testing.T) {
	var b bool
	v := reflect.ValueOf(&b).Elem()

	if err := scanValue(true, v); err != nil {
		t.Fatal(err)
	}
	if !b {
		t.Error("got false, want true")
	}
}

func TestScanValue_Float(t *testing.T) {
	var f float64
	v := reflect.ValueOf(&f).Elem()

	if err := scanValue(3.14, v); err != nil {
		t.Fatal(err)
	}
	if f != 3.14 {
		t.Errorf("got %f, want 3.14", f)
	}
}

func TestScanValue_Nil(t *testing.T) {
	var s string
	v := reflect.ValueOf(&s).Elem()

	if err := scanValue(nil, v); err != nil {
		t.Fatal(err)
	}
	if s != "" {
		t.Errorf("got %q, want empty string", s)
	}
}

func TestScanValue_IntFromString(t *testing.T) {
	var n int64
	v := reflect.ValueOf(&n).Elem()

	if err := scanValue("42", v); err != nil {
		t.Fatal(err)
	}
	if n != 42 {
		t.Errorf("got %d, want 42", n)
	}
}

func TestScanValue_BoolFromInt(t *testing.T) {
	var b bool
	v := reflect.ValueOf(&b).Elem()

	if err := scanValue(int64(1), v); err != nil {
		t.Fatal(err)
	}
	if !b {
		t.Error("got false, want true")
	}
}

// TestScanJSON_WhitespaceRoundtrip verifies the xorm bug fix:
// JSON with spaces after commas in []string should round-trip correctly.
func TestScanJSON_WhitespaceRoundtrip(t *testing.T) {
	// This is the exact case that xorm v1.1.6 breaks on:
	// Stored as '["authorization_code", "password", "client_credentials"]'
	// xorm fails to deserialize because of spaces after commas
	input := `["authorization_code", "password", "client_credentials"]`

	var result []string
	v := reflect.ValueOf(&result).Elem()

	if err := scanJSON(input, v); err != nil {
		t.Fatalf("scanJSON failed on whitespace JSON: %v", err)
	}

	expected := []string{"authorization_code", "password", "client_credentials"}
	if len(result) != len(expected) {
		t.Fatalf("got %d elements, want %d", len(result), len(expected))
	}
	for i, s := range result {
		if s != expected[i] {
			t.Errorf("element %d: got %q, want %q", i, s, expected[i])
		}
	}
}

// TestScanJSON_CompactJSON verifies compact JSON also works.
func TestScanJSON_CompactJSON(t *testing.T) {
	input := `["a","b","c"]`

	var result []string
	v := reflect.ValueOf(&result).Elem()

	if err := scanJSON(input, v); err != nil {
		t.Fatalf("scanJSON failed: %v", err)
	}

	if len(result) != 3 || result[0] != "a" || result[1] != "b" || result[2] != "c" {
		t.Errorf("unexpected result: %v", result)
	}
}

func TestScanJSON_Map(t *testing.T) {
	input := `{"key": "value", "count": 42}`

	var result map[string]interface{}
	v := reflect.ValueOf(&result).Elem()

	if err := scanJSON(input, v); err != nil {
		t.Fatalf("scanJSON failed: %v", err)
	}

	if result["key"] != "value" {
		t.Errorf("key: got %v, want value", result["key"])
	}
}

func TestScanJSON_EmptyString(t *testing.T) {
	var result []string
	v := reflect.ValueOf(&result).Elem()

	if err := scanJSON("", v); err != nil {
		t.Fatalf("scanJSON failed on empty: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestScanJSON_Null(t *testing.T) {
	var result []string
	v := reflect.ValueOf(&result).Elem()

	if err := scanJSON("null", v); err != nil {
		t.Fatalf("scanJSON failed on null: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestScanTime_RFC3339(t *testing.T) {
	var tm time.Time
	v := reflect.ValueOf(&tm).Elem()

	if err := scanTime("2024-01-15T10:30:00Z", v); err != nil {
		t.Fatal(err)
	}
	if tm.Year() != 2024 || tm.Month() != 1 || tm.Day() != 15 {
		t.Errorf("unexpected time: %v", tm)
	}
}

func TestScanTime_DateTime(t *testing.T) {
	var tm time.Time
	v := reflect.ValueOf(&tm).Elem()

	if err := scanTime("2024-01-15 10:30:00", v); err != nil {
		t.Fatal(err)
	}
	if tm.Year() != 2024 {
		t.Errorf("unexpected time: %v", tm)
	}
}

func TestMarshalJSON_Compact(t *testing.T) {
	data := []string{"a", "b", "c"}
	result, err := marshalJSON(data)
	if err != nil {
		t.Fatal(err)
	}

	// Verify compact output (no spaces)
	expected := `["a","b","c"]`
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestPK_Type(t *testing.T) {
	pk := PK{"admin", "test-app"}
	if len(pk) != 2 {
		t.Errorf("PK length: got %d, want 2", len(pk))
	}
	if pk[0] != "admin" {
		t.Errorf("PK[0]: got %v, want admin", pk[0])
	}
}
