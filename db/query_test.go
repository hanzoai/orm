package db

import (
	"testing"
)

func TestParseFilterString(t *testing.T) {
	tests := []struct {
		input string
		field string
		op    string
	}{
		{"Name=", "Name", "="},
		{"Age>", "Age", ">"},
		{"Score>=", "Score", ">="},
		{"Status!=", "Status", "!="},
		{"Count<", "Count", "<"},
		{"Value<=", "Value", "<="},
		{"NoOp", "NoOp", "="},
	}

	for _, tt := range tests {
		field, op := ParseFilterString(tt.input)
		if field != tt.field || op != tt.op {
			t.Errorf("ParseFilterString(%q) = (%q, %q), want (%q, %q)",
				tt.input, field, op, tt.field, tt.op)
		}
	}
}

func TestNormalizeOp(t *testing.T) {
	tests := []struct {
		input  string
		expect string
	}{
		{"=", "="},
		{"==", "="},
		{"!=", "!="},
		{"<>", "!="},
		{">", ">"},
		{">=", ">="},
		{"<", "<"},
		{"<=", "<="},
		{"unknown", "="},
	}

	for _, tt := range tests {
		got := NormalizeOp(tt.input)
		if got != tt.expect {
			t.Errorf("NormalizeOp(%q) = %q, want %q", tt.input, got, tt.expect)
		}
	}
}

func TestToJSONFieldName(t *testing.T) {
	tests := []struct {
		input  string
		expect string
	}{
		{"", ""},
		{"Name", "name"},
		{"FirstName", "firstName"},
		{"id", "id"},
		{"Account.TransactionHash", "account.transactionHash"},
		{"Nested.Deep.Field", "nested.deep.field"},
	}

	for _, tt := range tests {
		got := ToJSONFieldName(tt.input)
		if got != tt.expect {
			t.Errorf("ToJSONFieldName(%q) = %q, want %q", tt.input, got, tt.expect)
		}
	}
}

func TestLowercaseFirst(t *testing.T) {
	tests := []struct {
		input  string
		expect string
	}{
		{"", ""},
		{"A", "a"},
		{"Alice", "alice"},
		{"already", "already"},
		{"ABC", "aBC"},
	}

	for _, tt := range tests {
		got := LowercaseFirst(tt.input)
		if got != tt.expect {
			t.Errorf("LowercaseFirst(%q) = %q, want %q", tt.input, got, tt.expect)
		}
	}
}

func TestDecodeCursor(t *testing.T) {
	cursor, err := DecodeCursor("test-cursor")
	if err != nil {
		t.Fatal(err)
	}
	if cursor.String() != "test-cursor" {
		t.Errorf("expected 'test-cursor', got %q", cursor.String())
	}
}
