package markdown

import (
	"strings"
	"testing"
)

func TestNormalizeTables_keepsExtraCells(t *testing.T) {
	src := "| A | B |\n|---|---|\n| 1 | 2 |\n| 3 | 4 | 5 |\n"
	out := normalizeTables([]byte(src))
	want := "| A | B |  |\n| --- | --- | --- |\n| 1 | 2 |\n| 3 | 4 | 5 |\n"
	if string(out) != want {
		t.Errorf("normalizeTables() =\n%q\nwant\n%q", out, want)
	}
}

func TestNormalizeTables_widensHeaderForWideBody(t *testing.T) {
	// Only the body row is too wide: header and delimiter gain a column,
	// the delimiter with ---, the header with an empty cell.
	src := "| A | B |\n| --- | --- |\n| 1 | 2 | 3 | 4 |\n"
	out := normalizeTables([]byte(src))
	if !strings.Contains(string(out), "| A | B |  |  |\n") {
		t.Errorf("header not widened:\n%s", out)
	}
	if !strings.Contains(string(out), "| --- | --- | --- | --- |\n") {
		t.Errorf("delimiter not widened:\n%s", out)
	}
}

func TestNormalizeTables_headerWiderThanDelimiter(t *testing.T) {
	src := "| A | B | C |\n| --- | --- |\n| 1 | 2 | 3 |\n"
	out := normalizeTables([]byte(src))
	if !strings.Contains(string(out), "| --- | --- | --- |") {
		t.Errorf("delimiter not widened:\n%s", out)
	}
}

func TestNormalizeTables_regularTableUntouched(t *testing.T) {
	for _, src := range []string{
		"| A | B |\n|---|---|\n| 1 | 2 |\n",
		"a | b\n---\n",
		"- | a | b |\n  | --- | --- |\n  | 1 | 2 |\n",
		"# heading | x\n\n| a | b |\n",
	} {
		if got := string(normalizeTables([]byte(src))); got != src {
			t.Errorf("normalizeTables(%q) = %q, want unchanged", src, got)
		}
	}
}

func TestNormalizeTables_fencedCodeUntouched(t *testing.T) {
	src := "```\n| a | b | c |\n|---|---|\n| 1 | 2 | 3 | 4 |\n```\n"
	if got := string(normalizeTables([]byte(src))); got != src {
		t.Errorf("fenced code must not be rewritten:\n%q", got)
	}
}

func TestNormalizeTables_setextHeadingUntouched(t *testing.T) {
	// A paragraph followed by --- is a setext heading, never a table:
	// the delimiter row is all dashes and must stay untouched.
	src := "a | b\n---\n"
	if got := string(normalizeTables([]byte(src))); got != src {
		t.Errorf("setext heading rewritten: %q", got)
	}
}

func TestNormalizeTables_blockquoteTableWidened(t *testing.T) {
	src := "> | A | B |\n> | --- | --- |\n> | 1 | 2 | 3 |\n"
	out := string(normalizeTables([]byte(src)))
	if !strings.Contains(out, "> | A | B |  |") {
		t.Errorf("quoted header not widened:\n%s", out)
	}
	if !strings.Contains(out, "> | --- | --- | --- |") {
		t.Errorf("quoted delimiter not widened:\n%s", out)
	}
}

func TestNormalizeTables_idempotent(t *testing.T) {
	src := "| A | B |\n|---|---|\n| 1 | 2 | 3 |\n\nmore | text\n---\n"
	once := normalizeTables([]byte(src))
	twice := normalizeTables(once)
	if string(once) != string(twice) {
		t.Errorf("not idempotent:\nonce:  %q\ntwice: %q", once, twice)
	}
}

func TestRender_irregularTableKeepsEveryCell(t *testing.T) {
	out := render(t, "| Name | Qty |\n|---|---|\n| apple | 3 | 4 |\n| pear |\n")
	for _, want := range []string{"<table>", "<td>3</td>", "<td>4</td>", "<td>pear</td>"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// Short rows are still padded out to the full column count.
	if n := strings.Count(out, "<td></td>"); n < 1 {
		t.Errorf("short row not padded (%d empty cells):\n%s", n, out)
	}
}

func TestRender_headerWiderThanDelimiterRendersTable(t *testing.T) {
	// Before the fix this rendered as a plain paragraph: goldmark refuses
	// a table whose header and delimiter disagree on the column count.
	out := render(t, "| A | B | C |\n|---|---|\n| 1 | 2 | 3 |\n")
	for _, want := range []string{"<table>", "<th>C</th>", "<td>3</td>"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRender_irregularTableKeepsEscapedPipeCell(t *testing.T) {
	out := render(t, "| a \\| b | c |\n|---|---|\n| 1 | 2 | 3 |\n")
	for _, want := range []string{"<td>1</td>", "<td>2</td>", "<td>3</td>"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRender_irregularTableInBlockquote(t *testing.T) {
	out := render(t, "> | A | B |\n> |---|---|\n> | 1 | 2 | 3 |\n")
	if !strings.Contains(out, "<blockquote>") || !strings.Contains(out, "<td>3</td>") {
		t.Errorf("quoted irregular table broken:\n%s", out)
	}
}

func TestRender_setextHeadingStillHeading(t *testing.T) {
	out := render(t, "a | b\n---\n")
	if strings.Contains(out, "<table>") || !strings.Contains(out, "<h2") {
		t.Errorf("setext heading became a table:\n%s", out)
	}
}
