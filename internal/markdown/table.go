package markdown

import (
	"bytes"
	"strings"
)

// normalizeTables rewrites tables so rows carrying more cells than the
// header keep every cell (MultiMarkdown-style loose tables).
//
// goldmark's GFM table parser drops a body row's cells beyond the header's
// column count and refuses to build a table when the header and delimiter
// row disagree on the column count, so an irregular table silently loses
// data or fails to render at all. Widening the header and delimiter rows to
// the widest row of the table makes every cell reachable; the parser pads
// short rows itself.
//
// The rewrite only feeds the renderer — the stored note is untouched.
func normalizeTables(src []byte) []byte {
	if !bytes.Contains(src, []byte("|")) {
		return src
	}
	lines := splitKeepEnds(string(src))
	out := make([]string, 0, len(lines))
	inFence := false
	for i := 0; i < len(lines); {
		raw := lines[i]
		body := trimEOL(raw)
		if isFenceLine(body) {
			inFence = !inFence
			out = append(out, raw)
			i++
			continue
		}
		if inFence || i+1 >= len(lines) {
			out = append(out, raw)
			i++
			continue
		}
		hRaw, dRaw := raw, lines[i+1]
		h, okH := parseTableRowLine(body)
		d, okD := parseTableRowLine(trimEOL(dRaw))
		if !okH || !okD || !isTablePair(h, d) {
			out = append(out, raw)
			i++
			continue
		}
		cols := len(h.cells)
		if len(d.cells) > cols {
			cols = len(d.cells)
		}
		j := i + 2
		for ; j < len(lines); j++ {
			row := trimEOL(lines[j])
			if row == "" {
				break
			}
			r, ok := parseTableRowLine(row)
			if !ok || (r.quote == "") != (h.quote == "") || isBlockStart(r.rest) {
				break
			}
			if n := len(rowCells(r.rest)); n > cols {
				cols = n
			}
		}
		if len(h.cells) < cols {
			hRaw = h.lead + h.quote + joinRow(padCells(h.cells, cols, "")) + lineEnd(hRaw)
		}
		if len(d.cells) < cols {
			dRaw = d.lead + d.quote + joinRow(padCells(d.cells, cols, "---")) + lineEnd(dRaw)
		}
		out = append(out, hRaw, dRaw)
		out = append(out, lines[i+2:j]...)
		i = j
	}
	return []byte(strings.Join(out, ""))
}

// isTablePair reports whether header and delimiter lines form a table
// whose column count may need widening.
func isTablePair(h, d tableLine) bool {
	if h.rest == "" || d.rest == "" {
		return false
	}
	if (h.quote == "") != (d.quote == "") {
		return false
	}
	if isBlockStart(h.rest) || isBlockStart(d.rest) || isThematicBreak(d.rest) {
		return false
	}
	return isDelimiterLine(d.rest)
}

type tableLine struct {
	lead  string // leading whitespace of the source line
	quote string // blockquote marker (">" plus spaces) or ""
	rest  string // line content after lead and quote
	cells []string
}

// parseTableRowLine splits a source line into its indentation, blockquote
// marker and table cells. ok is false for indented code (4+ columns) and
// anything that cannot be part of a table row.
func parseTableRowLine(line string) (tableLine, bool) {
	t := tableLine{}
	trimmed := strings.TrimLeft(line, " \t")
	t.lead = line[:len(line)-len(trimmed)]
	if columnWidth(t.lead) >= 4 {
		return t, false
	}
	rest := trimmed
	if strings.HasPrefix(rest, ">") {
		k := 1
		for k < len(rest) && (rest[k] == ' ' || rest[k] == '\t') {
			k++
		}
		t.quote = rest[:k]
		rest = rest[k:]
		inner := strings.TrimLeft(rest, " \t")
		if columnWidth(rest)-columnWidth(inner) >= 4 {
			return t, false
		}
		rest = inner
	}
	t.rest = rest
	t.cells = rowCells(rest)
	return t, true
}

// rowCells splits a row into its cell texts the way goldmark's table
// parser does: optional outer pipes are stripped, then every unescaped
// pipe separates a cell, each trimmed of surrounding spaces.
func rowCells(line string) []string {
	s := strings.TrimSpace(line)
	if s == "" {
		return nil
	}
	if s[0] == '|' {
		s = s[1:]
	}
	if n := len(s); n > 0 && s[n-1] == '|' {
		s = s[:len(s)-1]
	}
	if s == "" {
		return nil
	}
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '|' && (i == 0 || s[i-1] != '\\') {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(s[i])
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

// padCells extends cells to cols columns with pad: header cells are padded
// with empty cells, delimiter cells with ---.
func padCells(cells []string, cols int, pad string) []string {
	for len(cells) < cols {
		cells = append(cells, pad)
	}
	return cells
}

// joinRow renders cells in canonical pipe style: | a | b |
func joinRow(cells []string) string {
	var b strings.Builder
	b.WriteString("|")
	for _, c := range cells {
		b.WriteString(" ")
		b.WriteString(c)
		b.WriteString(" |")
	}
	return b.String()
}

// isDelimiterLine mirrors goldmark's isTableDelim plus its per-cell
// alignment patterns, so a line is only treated as a delimiter row when
// the table parser would accept it too.
func isDelimiterLine(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	allSep := true
	for i := 0; i < len(t); i++ {
		c := t[i]
		if c != '-' {
			allSep = false
		}
		if c != ' ' && c != '\t' && c != '-' && c != '|' && c != ':' {
			return false
		}
	}
	if allSep {
		return false
	}
	for _, cell := range rowCells(t) {
		if !isDelimiterCell(cell) {
			return false
		}
	}
	return true
}

func isDelimiterCell(cell string) bool {
	if strings.HasPrefix(cell, ":") {
		cell = cell[1:]
	}
	if strings.HasSuffix(cell, ":") {
		cell = cell[:len(cell)-1]
	}
	if cell == "" {
		return false
	}
	for i := 0; i < len(cell); i++ {
		if cell[i] != '-' {
			return false
		}
	}
	return true
}

// isBlockStart reports whether a line opens a new block and therefore
// ends the paragraph a table's delimiter row belongs to.
func isBlockStart(rest string) bool {
	if rest == "" || isFenceLine(rest) {
		return true
	}
	switch rest[0] {
	case '#':
		n := 0
		for n < len(rest) && rest[n] == '#' {
			n++
		}
		return n <= 6 && (n == len(rest) || rest[n] == ' ' || rest[n] == '\t')
	case '>':
		return true
	case '<':
		return len(rest) > 1 && (isASCIILetter(rest[1]) || rest[1] == '!' || rest[1] == '/' || rest[1] == '?')
	}
	if len(rest) >= 2 && (rest[0] == '-' || rest[0] == '*' || rest[0] == '+') &&
		(rest[1] == ' ' || rest[1] == '\t') {
		return true
	}
	// ordered list: "2." or "2)" followed by a space
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i > 0 && i+1 < len(rest) && (rest[i] == '.' || rest[i] == ')') &&
		(rest[i+1] == ' ' || rest[i+1] == '\t') {
		return true
	}
	return isThematicBreak(rest)
}

// isThematicBreak reports whether the line is a thematic break
// (---, ***, _ _ _), which also ends a paragraph.
func isThematicBreak(rest string) bool {
	t := strings.TrimSpace(rest)
	if len(t) < 3 {
		return false
	}
	var sym byte
	n := 0
	for i := 0; i < len(t); i++ {
		c := t[i]
		if c == ' ' || c == '\t' {
			continue
		}
		if c != '-' && c != '*' && c != '_' {
			return false
		}
		if sym == 0 {
			sym = c
		} else if c != sym {
			return false
		}
		n++
	}
	return n >= 3
}

func isFenceLine(line string) bool {
	t := strings.TrimLeft(line, " \t")
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

func isASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func columnWidth(s string) int {
	w := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\t' {
			w += 4
		} else {
			w++
		}
	}
	return w
}

// splitKeepEnds splits on \n keeping each line terminator in place, so the
// pieces can be joined back into the original text.
func splitKeepEnds(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.SplitAfter(s, "\n")
	if last := len(parts) - 1; last >= 0 && parts[last] == "" {
		parts = parts[:last]
	}
	return parts
}

func trimEOL(s string) string {
	return strings.TrimRight(s, "\r\n")
}

func lineEnd(s string) string {
	return s[len(trimEOL(s)):]
}
