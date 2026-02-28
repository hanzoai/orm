// Package names provides naming convention mappers for converting between
// Go struct field names and database column/table names.
// Compatible with xorm's naming interfaces.
package names

import (
	"strings"
	"unicode"
)

// Mapper defines the interface for name conversion between Go and SQL.
type Mapper interface {
	Obj2Table(string) string
	Table2Obj(string) string
}

// SnakeMapper converts CamelCase to snake_case and back.
type SnakeMapper struct{}

func (SnakeMapper) Obj2Table(name string) string {
	return camelToSnake(name)
}

func (SnakeMapper) Table2Obj(name string) string {
	return snakeToCamel(name)
}

// SameMapper performs no conversion — names pass through unchanged.
type SameMapper struct{}

func (SameMapper) Obj2Table(name string) string { return name }
func (SameMapper) Table2Obj(name string) string { return name }

// PrefixMapper adds a prefix to table names.
type PrefixMapper struct {
	Mapper Mapper
	Prefix string
}

func (m PrefixMapper) Obj2Table(name string) string {
	return m.Prefix + m.Mapper.Obj2Table(name)
}

func (m PrefixMapper) Table2Obj(name string) string {
	name = strings.TrimPrefix(name, m.Prefix)
	return m.Mapper.Table2Obj(name)
}

// GonicMapper is like SnakeMapper but treats common initialisms as single
// words (e.g. "ID" → "id", "HTTP" → "http", "URL" → "url").
type GonicMapper struct {
	Acronyms map[string]bool
}

// NewGonicMapper returns a GonicMapper with common Go initialisms.
func NewGonicMapper() GonicMapper {
	return GonicMapper{
		Acronyms: map[string]bool{
			"ID": true, "HTTP": true, "URL": true, "API": true,
			"SQL": true, "SSH": true, "JSON": true, "XML": true,
			"HTML": true, "CSS": true, "DNS": true, "RPC": true,
			"TCP": true, "UDP": true, "IP": true, "TLS": true,
			"SSL": true, "UUID": true, "URI": true, "EOF": true,
		},
	}
}

func (m GonicMapper) Obj2Table(name string) string {
	return gonicCamelToSnake(name, m.Acronyms)
}

func (m GonicMapper) Table2Obj(name string) string {
	return snakeToCamel(name)
}

// camelToSnake converts CamelCase to snake_case.
func camelToSnake(s string) string {
	var result strings.Builder
	result.Grow(len(s) + 4)

	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := rune(s[i-1])
				if unicode.IsLower(prev) || unicode.IsDigit(prev) {
					result.WriteByte('_')
				} else if unicode.IsUpper(prev) && i+1 < len(s) && unicode.IsLower(rune(s[i+1])) {
					result.WriteByte('_')
				}
			}
			result.WriteRune(unicode.ToLower(r))
		} else {
			result.WriteRune(r)
		}
	}

	return result.String()
}

// gonicCamelToSnake converts CamelCase to snake_case, treating known
// acronyms as single tokens.
func gonicCamelToSnake(s string, acronyms map[string]bool) string {
	var result strings.Builder
	result.Grow(len(s) + 4)

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		// Check for known acronym at this position
		if unicode.IsUpper(runes[i]) {
			found := false
			for acr := range acronyms {
				acrRunes := []rune(acr)
				if i+len(acrRunes) <= len(runes) {
					match := true
					for j, ar := range acrRunes {
						if runes[i+j] != ar {
							match = false
							break
						}
					}
					if match {
						// Check that the acronym isn't followed by a lowercase letter
						// (which would mean it's not a standalone acronym)
						atEnd := i+len(acrRunes) >= len(runes)
						nextIsUpper := !atEnd && unicode.IsUpper(runes[i+len(acrRunes)])
						nextIsNonAlpha := !atEnd && !unicode.IsLetter(runes[i+len(acrRunes)])
						if atEnd || nextIsUpper || nextIsNonAlpha {
							if i > 0 {
								result.WriteByte('_')
							}
							result.WriteString(strings.ToLower(acr))
							i += len(acrRunes) - 1
							found = true
							break
						}
					}
				}
			}
			if found {
				continue
			}
		}

		if unicode.IsUpper(runes[i]) {
			if i > 0 {
				prev := runes[i-1]
				if unicode.IsLower(prev) || unicode.IsDigit(prev) {
					result.WriteByte('_')
				} else if unicode.IsUpper(prev) && i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
					result.WriteByte('_')
				}
			}
			result.WriteRune(unicode.ToLower(runes[i]))
		} else {
			result.WriteRune(runes[i])
		}
	}

	return result.String()
}

// snakeToCamel converts snake_case to CamelCase.
func snakeToCamel(s string) string {
	parts := strings.Split(s, "_")
	var result strings.Builder
	result.Grow(len(s))

	for _, p := range parts {
		if p == "" {
			continue
		}
		runes := []rune(p)
		runes[0] = unicode.ToUpper(runes[0])
		result.WriteString(string(runes))
	}

	return result.String()
}
