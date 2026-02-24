package val

import (
	"testing"
)

type testUser struct {
	Email    string
	Password string
	Name     string
}

func TestValidatorExists(t *testing.T) {
	v := New()
	v.Check("Name").Exists()

	errs := v.Exec(&testUser{Name: "Alice"})
	if len(errs) != 0 {
		t.Errorf("expected no errors for non-empty name, got %v", errs)
	}

	errs = v.Exec(&testUser{Name: ""})
	if len(errs) != 1 {
		t.Errorf("expected 1 error for empty name, got %d", len(errs))
	}
}

func TestValidatorIsEmail(t *testing.T) {
	v := New()
	v.Check("Email").IsEmail()

	errs := v.Exec(&testUser{Email: "test@example.com"})
	if len(errs) != 0 {
		t.Errorf("expected no errors for valid email, got %v", errs)
	}

	errs = v.Exec(&testUser{Email: "notanemail"})
	if len(errs) != 1 {
		t.Errorf("expected 1 error for invalid email, got %d", len(errs))
	}
}

func TestValidatorMinLength(t *testing.T) {
	v := New()
	v.Check("Password").MinLength(6)

	errs := v.Exec(&testUser{Password: "longpassword"})
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}

	errs = v.Exec(&testUser{Password: "short"})
	if len(errs) != 1 {
		t.Errorf("expected 1 error for short password, got %d", len(errs))
	}
}

func TestValidatorMatches(t *testing.T) {
	v := New()
	v.Check("Name").Matches("Alice", "Bob", "Charlie")

	errs := v.Exec(&testUser{Name: "Bob"})
	if len(errs) != 0 {
		t.Errorf("expected no errors for matching name, got %v", errs)
	}

	errs = v.Exec(&testUser{Name: "Dave"})
	if len(errs) != 1 {
		t.Errorf("expected 1 error for non-matching name, got %d", len(errs))
	}
}

func TestValidatorIsPassword(t *testing.T) {
	v := New()
	v.Check("Password").IsPassword()

	errs := v.Exec(&testUser{Password: "securepass"})
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}

	errs = v.Exec(&testUser{Password: "12345"})
	if len(errs) != 1 {
		t.Errorf("expected error for short password, got %d", len(errs))
	}
}

func TestValidatorMultipleChecks(t *testing.T) {
	v := New()
	v.Check("Email").IsEmail()
	v.Check("Password").IsPassword()
	v.Check("Name").Exists()

	errs := v.Exec(&testUser{Email: "", Password: "", Name: ""})
	if len(errs) != 3 {
		t.Errorf("expected 3 errors, got %d: %v", len(errs), errs)
	}
}

func TestFieldError(t *testing.T) {
	fe := NewFieldError("email", "must be valid")
	if fe.Error() != "must be valid" {
		t.Errorf("expected 'must be valid', got %q", fe.Error())
	}
	if fe.Field != "email" {
		t.Errorf("expected field 'email', got %q", fe.Field)
	}
}

func TestValidationError(t *testing.T) {
	err := NewError("validation failed")
	if err.Error() != "validation failed" {
		t.Errorf("expected 'validation failed', got %q", err.Error())
	}
	if err.Type != "validation" {
		t.Errorf("expected type 'validation', got %q", err.Type)
	}
}

func TestValidationErrorMessages(t *testing.T) {
	err := NewError("validation failed")
	err.Fields = []error{
		NewFieldError("email", "invalid email"),
		NewFieldError("name", "required"),
	}

	msgs := err.Messages()
	if msgs["email"] != "invalid email" {
		t.Errorf("expected 'invalid email', got %q", msgs["email"])
	}
	if msgs["name"] != "required" {
		t.Errorf("expected 'required', got %q", msgs["name"])
	}
}

func TestCheckString(t *testing.T) {
	ctx := CheckString("password123")
	if !ctx.IsValid {
		t.Error("expected valid for long string")
	}

	ctx = CheckString("short").IsPassword()
	if ctx.IsValid {
		t.Error("expected invalid for short password")
	}

	ctx = CheckString("validpass").IsPassword()
	if !ctx.IsValid {
		t.Error("expected valid for long password")
	}
}

func TestValidatePassword(t *testing.T) {
	errs := ValidatePassword("goodpass", nil)
	if len(errs) != 0 {
		t.Errorf("expected no errors for good password, got %v", errs)
	}

	errs = ValidatePassword("short", nil)
	if len(errs) != 1 {
		t.Errorf("expected 1 error for short password, got %d", len(errs))
	}
}
