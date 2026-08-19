package analyzer_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/abdo-s1/reversibility-engine/internal/analyzer"
	"github.com/abdo-s1/reversibility-engine/internal/domain"
)

func TestNormalizeStatement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"already normal", "DROP TABLE users;", "DROP TABLE users;"},
		{"collapses newlines", "ALTER TABLE orders\n  ADD COLUMN notes text;", "ALTER TABLE orders ADD COLUMN notes text;"},
		{"collapses tabs and runs", "DROP\t\tTABLE   users;", "DROP TABLE users;"},
		{"trims both ends", "\n  DROP TABLE users;  \n", "DROP TABLE users;"},
		{"empty", "", ""},
		{"whitespace only", "   \n\t ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := analyzer.NormalizeStatement(tt.in); got != tt.want {
				t.Errorf("NormalizeStatement(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Formatting must not change the output. Two statements that differ only in whitespace have to
// normalize identically, or reformatting a migration would change the certificate digest.
func TestNormalizeStatementIsFormatInsensitive(t *testing.T) {
	t.Parallel()

	variants := []string{
		"ALTER TABLE orders ADD COLUMN notes text;",
		"ALTER  TABLE  orders  ADD  COLUMN  notes  text;",
		"ALTER TABLE orders\n    ADD COLUMN notes text;",
		"\tALTER TABLE orders\r\n\tADD COLUMN notes text;\n",
	}

	first := analyzer.NormalizeStatement(variants[0])
	for _, v := range variants[1:] {
		if got := analyzer.NormalizeStatement(v); got != first {
			t.Errorf("NormalizeStatement(%q) = %q, want %q", v, got, first)
		}
	}
}

func TestNormalizeStatementRespectsBound(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", domain.MaxStatementLength*3)

	got := analyzer.NormalizeStatement(long)
	if utf8.RuneCountInString(got) != domain.MaxStatementLength {
		t.Errorf("truncated length = %d runes, want exactly %d", utf8.RuneCountInString(got), domain.MaxStatementLength)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated statement does not signal that it was cut: %q", got)
	}

	// A statement exactly at the bound must survive untouched.
	exact := strings.Repeat("b", domain.MaxStatementLength)
	if got := analyzer.NormalizeStatement(exact); got != exact {
		t.Errorf("a statement exactly at the bound was modified")
	}
}

// Truncation must cut on rune boundaries. Splitting a multi-byte identifier would emit invalid
// UTF-8 into a JSON certificate, which downstream parsers reject.
func TestNormalizeStatementTruncatesOnRuneBoundaries(t *testing.T) {
	t.Parallel()

	got := analyzer.NormalizeStatement(strings.Repeat("Ω", domain.MaxStatementLength*2))

	if !utf8.ValidString(got) {
		t.Errorf("truncation produced invalid UTF-8: %q", got)
	}
	if utf8.RuneCountInString(got) != domain.MaxStatementLength {
		t.Errorf("truncated length = %d runes, want %d", utf8.RuneCountInString(got), domain.MaxStatementLength)
	}
}
