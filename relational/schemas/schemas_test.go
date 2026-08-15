package schemas

import (
	"reflect"
	"testing"

	xschemas "github.com/hanzoai/xorm/schemas"
)

// The point of this package is that migrating an import path is the WHOLE
// migration: a value built here must BE the engine's value, not a copy that
// merely looks like one. Type identity is what makes that true, so assert it
// rather than assume it — an alias silently turned into a defined type would
// still compile here and break every consumer that passes one across.
func TestTypesAreTheEngineTypes(t *testing.T) {
	for _, c := range []struct {
		name       string
		ours, engs any
	}{
		{"PK", PK{}, xschemas.PK{}},
		{"Quoter", Quoter{}, xschemas.Quoter{}},
		{"Table", Table{}, xschemas.Table{}},
		{"Column", Column{}, xschemas.Column{}},
		{"Index", Index{}, xschemas.Index{}},
		{"SQLType", SQLType{}, xschemas.SQLType{}},
		{"DBType", DBType(""), xschemas.DBType("")},
		{"Collation", Collation{}, xschemas.Collation{}},
		{"Version", Version{}, xschemas.Version{}},
	} {
		if o, e := reflect.TypeOf(c.ours), reflect.TypeOf(c.engs); o != e {
			t.Errorf("%s is %v, engine's is %v — not an alias", c.name, o, e)
		}
	}
}

// A constructor must hand back the engine's own value, so a caller can pass it
// straight into the engine.
func TestConstructorsReturnEngineValues(t *testing.T) {
	pk := NewPK("owner", "name")
	var _ *xschemas.PK = pk
	if got := []any(*pk); len(got) != 2 || got[0] != "owner" || got[1] != "name" {
		t.Errorf("NewPK lost its values: %v", got)
	}

	idx := NewIndex("idx_owner", 1)
	var _ *xschemas.Index = idx
	if idx.Name != "idx_owner" {
		t.Errorf("NewIndex lost its name: %q", idx.Name)
	}

	tbl := NewTable("t", reflect.TypeOf(struct{}{}))
	var _ *xschemas.Table = tbl
	if tbl.Name != "t" {
		t.Errorf("NewTable lost its name: %q", tbl.Name)
	}
}

// The dialect names must equal the engine's, or a consumer selecting a database
// through this package would select nothing.
func TestDBTypeConstantsMatchTheEngine(t *testing.T) {
	for _, c := range []struct {
		ours, engs DBType
	}{
		{POSTGRES, xschemas.POSTGRES},
		{SQLITE, xschemas.SQLITE},
		{MYSQL, xschemas.MYSQL},
		{MSSQL, xschemas.MSSQL},
		{ORACLE, xschemas.ORACLE},
		{DAMENG, xschemas.DAMENG},
		{GBASE8S, xschemas.GBASE8S},
	} {
		if c.ours != c.engs {
			t.Errorf("dialect constant %q != engine's %q", c.ours, c.engs)
		}
	}
}
