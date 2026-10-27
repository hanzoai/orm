package val

import (
	"fmt"
	"slices"
)

func ExampleValidator() {
	type User struct {
		Name  string
		Email string
	}

	v := New()
	v.Check("Name").Exists()
	v.Check("Email").IsEmail()

	// Failing input: blank name and a malformed email.
	bad := &User{Name: "", Email: "nope"}
	badErrs := v.Exec(bad)
	msgs := make([]string, 0, len(badErrs))
	for _, err := range badErrs {
		msgs = append(msgs, err.Error())
	}
	// Exec iterates the registered fields in map order, so sort to keep
	// the example output stable.
	slices.Sort(msgs)
	for _, msg := range msgs {
		fmt.Println(msg)
	}

	// Passing input.
	good := &User{Name: "Ada", Email: "ada@example.com"}
	if errs := v.Exec(good); len(errs) == 0 {
		fmt.Println("no validation errors")
	}

	// Output:
	// Field cannot be blank.
	// Field must be an email.
	// no validation errors
}
