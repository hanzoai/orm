package names

import "testing"

func TestSnakeMapper_Obj2Table(t *testing.T) {
	m := SnakeMapper{}

	tests := []struct {
		input string
		want  string
	}{
		{"UserName", "user_name"},
		{"ID", "id"},
		{"HTMLParser", "html_parser"},
		{"JSONData", "json_data"},
		{"SimpleTest", "simple_test"},
		{"A", "a"},
		{"Ab", "ab"},
		{"ABCDef", "abc_def"},
		{"UserID", "user_id"},
		{"OAuth2Token", "o_auth2_token"},
		{"CreatedAt", "created_at"},
		{"", ""},
	}

	for _, tt := range tests {
		got := m.Obj2Table(tt.input)
		if got != tt.want {
			t.Errorf("SnakeMapper.Obj2Table(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSnakeMapper_Table2Obj(t *testing.T) {
	m := SnakeMapper{}

	tests := []struct {
		input string
		want  string
	}{
		{"user_name", "UserName"},
		{"id", "Id"},
		{"created_at", "CreatedAt"},
		{"json_data", "JsonData"},
		{"simple", "Simple"},
		{"", ""},
	}

	for _, tt := range tests {
		got := m.Table2Obj(tt.input)
		if got != tt.want {
			t.Errorf("SnakeMapper.Table2Obj(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSameMapper(t *testing.T) {
	m := SameMapper{}

	if got := m.Obj2Table("UserName"); got != "UserName" {
		t.Errorf("SameMapper.Obj2Table = %q, want %q", got, "UserName")
	}
	if got := m.Table2Obj("user_name"); got != "user_name" {
		t.Errorf("SameMapper.Table2Obj = %q, want %q", got, "user_name")
	}
}

func TestPrefixMapper(t *testing.T) {
	m := PrefixMapper{
		Mapper: SnakeMapper{},
		Prefix: "t_",
	}

	if got := m.Obj2Table("UserName"); got != "t_user_name" {
		t.Errorf("PrefixMapper.Obj2Table = %q, want %q", got, "t_user_name")
	}
	if got := m.Table2Obj("t_user_name"); got != "UserName" {
		t.Errorf("PrefixMapper.Table2Obj = %q, want %q", got, "UserName")
	}
}

func TestGonicMapper(t *testing.T) {
	m := NewGonicMapper()

	tests := []struct {
		input string
		want  string
	}{
		{"UserID", "user_id"},
		{"HTTPServer", "http_server"},
		{"HTMLURL", "html_url"},
		{"SimpleTest", "simple_test"},
	}

	for _, tt := range tests {
		got := m.Obj2Table(tt.input)
		if got != tt.want {
			t.Errorf("GonicMapper.Obj2Table(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
