package orm_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/orm/query"
)

type userSummary struct {
	ID    string
	Label string
}

func TestTypedMapGenericMethod(t *testing.T) {
	relational(t, func(t *testing.T, db *query.DB) {
		ctx := context.Background()

		typed := orm.Select[typedUser](db, "users").OrderBy("id ASC")

		// Call Map with a closure transforming typedUser to userSummary.
		// Go 1.27 infers U = userSummary automatically from closure return type.
		summaries, err := typed.Map(ctx, func(u typedUser) userSummary {
			return userSummary{
				ID:    u.ID,
				Label: fmt.Sprintf("%s (%s)", u.ID, u.Email),
			}
		})
		if err != nil {
			t.Fatalf("Map failed: %v", err)
		}

		if len(summaries) != 3 {
			t.Fatalf("expected 3 summaries, got %d", len(summaries))
		}
		if summaries[0].Label != "u1 (alice@x.io)" {
			t.Errorf("unexpected summary[0]: %+v", summaries[0])
		}
		if summaries[1].Label != "u2 (bob@x.io)" {
			t.Errorf("unexpected summary[1]: %+v", summaries[1])
		}
	})
}

func TestTypedOneMapGenericMethod(t *testing.T) {
	relational(t, func(t *testing.T, db *query.DB) {
		ctx := context.Background()

		typed := orm.Select[typedUser](db, "users").OrderBy("id ASC")

		// Call OneMap transforming single typedUser to string
		email, err := typed.OneMap(ctx, func(u typedUser) string {
			return u.Email
		})
		if err != nil {
			t.Fatalf("OneMap failed: %v", err)
		}
		if email == nil || *email != "alice@x.io" {
			t.Errorf("expected alice@x.io, got %v", email)
		}
	})
}
