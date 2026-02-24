package val

// Error is a top-level validation error.
type Error struct {
	Fields  []error
	Message string
	Type    string
}

func (e Error) Error() string {
	return e.Message
}

// Messages returns a map of field error messages.
func (e Error) Messages() map[string]string {
	m := make(map[string]string)
	for _, f := range e.Fields {
		if err, ok := f.(*FieldError); ok {
			m[err.Field] = err.Message
		}
	}
	return m
}

// NewError creates a new validation error.
func NewError(message string) *Error {
	err := new(Error)
	err.Message = message
	err.Type = "validation"
	return err
}

// FieldError is a per-field validation error.
type FieldError struct {
	Field   string
	Message string
}

func (f FieldError) Error() string {
	return f.Message
}

// NewFieldError creates a new field error.
func NewFieldError(field string, message string) *FieldError {
	return &FieldError{field, message}
}
