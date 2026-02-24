// Package val provides struct field validation utilities.
package val

import (
	"errors"
	"fmt"
	"log"
	"reflect"
	"strconv"
	"strings"
)

// ValidatorFunction is a function that validates a value.
type ValidatorFunction interface{}

// Validator validates struct fields using registered validation functions.
type Validator struct {
	Value     reflect.Value
	lastField string
	fnsMap    map[string][]ValidatorFunction
}

// New creates a new Validator.
func New() *Validator {
	return &Validator{fnsMap: make(map[string][]ValidatorFunction)}
}

// depointer dereferences all pointer layers.
func depointer(value reflect.Value) reflect.Value {
	for value.Kind() == reflect.Ptr {
		value = reflect.Indirect(value)
	}
	return value
}

// traverseAddts traverses dot notation field paths.
func traverseAddts(value reflect.Value, field string) reflect.Value {
	fields := strings.Split(field, ".")
	for _, field := range fields {
		switch value.Kind() {
		case reflect.Struct:
			value = depointer(value.FieldByName(field))
		case reflect.Slice:
			if i, err := strconv.Atoi(field); err != nil {
				log.Panicf("'%v' expected to be an int: %v", field, err)
			} else {
				value = depointer(value.Index(i))
			}
		}
	}
	return value
}

// Check sets the current field to be validated.
func (v *Validator) Check(field string) *Validator {
	if _, ok := v.fnsMap[field]; !ok {
		v.fnsMap[field] = make([]ValidatorFunction, 0)
	}
	v.lastField = field
	return v
}

// Exec runs all validation functions and returns any errors.
func (v *Validator) Exec(value interface{}) []error {
	v.Value = depointer(reflect.ValueOf(value))

	errs := make([]error, 0)
	for field, fns := range v.fnsMap {
		value := traverseAddts(v.Value, field)

		if !value.IsValid() {
			log.Panicf("Field %v does not exist!", field)
		}

		for _, fn := range fns {
			fnVal := reflect.ValueOf(fn)
			errVals := fnVal.Call([]reflect.Value{value})
			if len(errVals) > 0 && !errVals[0].IsNil() {
				err := errVals[0].Interface().(error)
				if err != nil {
					errs = append(errs, NewFieldError(field, err.Error()))
				}
			}
		}
	}

	return errs
}

// Add registers a validation function for the current field.
func (v *Validator) Add(fn ValidatorFunction) *Validator {
	field := v.lastField
	typ := reflect.TypeOf(fn)
	if typ.Kind() != reflect.Func {
		log.Panic("ValidatorFunction must be a function")
	}
	if typ.NumIn() != 1 {
		log.Panic("ValidatorFunction must have one argument")
	}

	v.fnsMap[field] = append(v.fnsMap[field], fn)
	return v
}

// Exists validates that the field is not empty.
func (v *Validator) Exists() *Validator {
	return v.Add(func(i interface{}) error {
		switch value := i.(type) {
		case string:
			if len(value) > 0 {
				return nil
			}
		}
		return errors.New("Field cannot be blank.")
	})
}

// IsEmail validates that the field looks like an email address.
func (v *Validator) IsEmail() *Validator {
	return v.Add(func(value string) error {
		if strings.Contains(value, "@") &&
			strings.Contains(value, ".") &&
			strings.Index(value, "@") < strings.Index(value, ".") &&
			len(value) > 5 {
			return nil
		}
		return errors.New("Field must be an email.")
	})
}

// MinLength validates minimum string length.
func (v *Validator) MinLength(minLength int) *Validator {
	return v.Add(func(value string) error {
		if len(value) >= minLength {
			return nil
		}
		return errors.New(fmt.Sprintf("Field must be atleast %d characters long.", minLength))
	})
}

// Matches validates the field matches one of the given strings.
func (v *Validator) Matches(strs ...string) *Validator {
	return v.Add(func(value string) error {
		for _, str := range strs {
			if str == value {
				return nil
			}
		}
		if len(strs) == 1 {
			return errors.New(fmt.Sprintf("Field must equal '%v', not '%v'.", strs[0], value))
		}
		return errors.New(fmt.Sprintf("Field must be one of ['%v'], not '%v'.", strings.Join(strs, "', '"), value))
	})
}

// Ensure adds a custom validation function.
func (v *Validator) Ensure(fn ValidatorFunction) *Validator {
	return v.Add(fn)
}

// IsPassword validates minimum password requirements.
func (v *Validator) IsPassword() *Validator {
	return v.MinLength(6)
}

// StringValidationContext provides fluent string validation.
type StringValidationContext struct {
	value   string
	IsValid bool
}

// CheckString creates a string validation context.
func CheckString(str string) *StringValidationContext {
	return &StringValidationContext{str, true}
}

// IsPassword validates password requirements.
func (s *StringValidationContext) IsPassword() *StringValidationContext {
	return s.LengthIsGreaterThanOrEqualTo(6)
}

// LengthIsGreaterThanOrEqualTo validates minimum length.
func (s *StringValidationContext) LengthIsGreaterThanOrEqualTo(n int) *StringValidationContext {
	s.IsValid = s.IsValid && (len(s.value) >= n)
	return s
}

// ValidatePassword validates a password string.
func ValidatePassword(password string, errs []string) []string {
	if !CheckString(password).IsPassword().IsValid {
		errs = append(errs, "Password Must be atleast 6 characters long.")
	}
	return errs
}
