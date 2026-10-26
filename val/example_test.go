package val_test

import (
	"fmt"

	"github.com/hanzoai/orm/val"
)

type User struct {
	Name  string
	Email string
}

func ExampleValidator() {
	v := val.New()
	v.Check("Name").Exists()
	v.Check("Email").IsEmail()

	// Failing input
	invalid := User{Name: "Alice", Email: "not-an-email"}
	errs := v.Exec(&invalid)
	for _, err := range errs {
		fmt.Println(err)
	}

	// Passing input
	valid := User{Name: "Alice", Email: "alice@example.com"}
	errs = v.Exec(&valid)
	fmt.Printf("valid errors: %d\n", len(errs))

	// Output:
	// Field must be an email.
	// valid errors: 0
}
