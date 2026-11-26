package cli

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Terminal styles. Each one is undone by its own code, so they nest.
const (
	boldOn       = "\x1b[1m"
	boldOff      = "\x1b[22m"
	dimOn        = "\x1b[2m"
	dimOff       = "\x1b[22m"
	italicOn     = "\x1b[3m"
	italicOff    = "\x1b[23m"
	underlineOn  = "\x1b[4m"
	underlineOff = "\x1b[24m"
	codeOn       = "\x1b[36m"
	codeOff      = "\x1b[39m"
)

// decideAfter is how much of a line must arrive before its kind (heading,
// list item, paragraph) is taken as known and it starts to be printed.
const decideAfter = 8

var (
	headingRe  = regexp.MustCompile(`^#{1,6}\s+(.*)$`)
	quoteRe    = regexp.MustCompile(`^(\s*)>\s?(.*)$`)
	bulletRe   = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	orderedRe  = regexp.MustCompile(`^(\s*\d+[.)]\s+)(.*)$`)
	numberRe   = regexp.MustCompile(`^\s*\d+[.)]?\s*$`)
	ruleRe     = regexp.MustCompile(`^\s*([-*_]\s*){3,}$`)
	dividerRe  = regexp.MustCompile(`^:?-+:?$`)
	styleRe    = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	fenceMarks = []string{"```", "~~~"}
)

// markdown turns the markdown of an answer into terminal styles while the
// answer is still arriving. A paragraph is printed as it comes; what cannot be
// decided yet (an emphasis not closed, a table not finished) waits.
type markdown struct {
	// width returns the columns of the terminal, or 0 when unknown.
	width func() int

	// line is the part of the current line not printed yet.
	line []rune
	// started says the beginning of the current line is already on the screen.
	started bool
	heading bool
	// inCode says the text is inside a fenced block.
	inCode bool
	table  []string
}

// Write takes the next piece of the answer and returns what can be printed.
func (m *markdown) Write(delta string) string {
	var out strings.Builder
	for _, r := range delta {
		switch r {
		case '\r':
		case '\n':
			m.endLine(&out)
		default:
			m.line = append(m.line, r)
		}
	}
	m.partial(&out)
	return out.String()
}

// Flush returns what was waiting, as it is, and forgets where the text was:
// the answer ended or something else is about to be printed.
func (m *markdown) Flush() string {
	var out strings.Builder
	if len(m.line) > 0 || m.started {
		m.endLine(&out)
	}
	m.flushTable(&out)
	m.inCode = false
	return out.String()
}

// partial prints what is already certain of a line that has not ended.
func (m *markdown) partial(out *strings.Builder) {
	if len(m.line) == 0 {
		return
	}
	if !m.started {
		line := string(m.line)
		trimmed := strings.TrimLeft(line, " \t")
		if m.inCode {
			// The line that closes the block is made of fence marks only.
			if strings.Trim(trimmed, "`~") == "" {
				return
			}
			m.started = true
		} else {
			undecided := len(m.line) < decideAfter ||
				strings.HasPrefix(trimmed, "|") || isFence(trimmed) ||
				strings.Trim(trimmed, "-*_ \t") == "" || numberRe.MatchString(line)
			if undecided {
				return
			}
			m.flushTable(out)
			prefix, rest, heading := blockPrefix(line)
			out.WriteString(prefix)
			m.started, m.heading, m.line = true, heading, []rune(rest)
		}
	}
	if m.inCode {
		out.WriteString(codeOn + string(m.line) + codeOff)
		m.line = m.line[:0]
		return
	}
	done, n := m.inline(m.line, false)
	out.WriteString(done)
	m.line = m.line[n:]
}

func (m *markdown) endLine(out *strings.Builder) {
	line := string(m.line)
	m.line = m.line[:0]
	switch {
	case m.started && m.inCode:
		out.WriteString(codeOn + line + codeOff)
	case m.started:
		done, _ := m.inline([]rune(line), true)
		out.WriteString(done)
		if m.heading {
			out.WriteString(boldOff)
		}
	case m.inCode:
		if closing := strings.TrimSpace(line); len(closing) >= 3 && strings.Trim(closing, "`~") == "" {
			m.inCode = false
			out.WriteString(dimOn + line + dimOff)
		} else {
			out.WriteString(codeOn + line + codeOff)
		}
	case strings.HasPrefix(strings.TrimLeft(line, " \t"), "|"):
		m.table = append(m.table, line)
		return
	default:
		m.flushTable(out)
		m.wholeLine(out, line)
	}
	m.started, m.heading = false, false
	out.WriteString("\n")
}

// wholeLine prints a line that arrived entire, without the line break.
func (m *markdown) wholeLine(out *strings.Builder, line string) {
	switch {
	case isFence(strings.TrimLeft(line, " \t")):
		m.inCode = true
		out.WriteString(dimOn + line + dimOff)
	case ruleRe.MatchString(line):
		out.WriteString(dimOn + strings.Repeat("─", 40) + dimOff)
	default:
		prefix, rest, heading := blockPrefix(line)
		m.heading = heading
		done, _ := m.inline([]rune(rest), true)
		out.WriteString(prefix + done)
		if heading {
			out.WriteString(boldOff)
		}
		m.heading = false
	}
}

func isFence(trimmed string) bool {
	for _, mark := range fenceMarks {
		if strings.HasPrefix(trimmed, mark) {
			return true
		}
	}
	return false
}

// blockPrefix splits a line into what its kind puts in front of it, already
// styled, and the text that follows.
func blockPrefix(line string) (prefix, rest string, heading bool) {
	if m := headingRe.FindStringSubmatch(line); m != nil {
		return boldOn, m[1], true
	}
	if m := quoteRe.FindStringSubmatch(line); m != nil {
		return m[1] + dimOn + "│ " + dimOff, m[2], false
	}
	if m := bulletRe.FindStringSubmatch(line); m != nil {
		return m[1] + "• ", m[2], false
	}
	if m := orderedRe.FindStringSubmatch(line); m != nil {
		return m[1], m[2], false
	}
	return "", line, false
}

// inline styles emphasis, code and links inside a line. When the line has not
// ended (final false), it stops at the first mark it cannot decide yet and
// reports how much of s it used.
func (m *markdown) inline(s []rune, final bool) (string, int) {
	var b strings.Builder
	literal := func(from, n int) int {
		b.WriteString(string(s[from : from+n]))
		return from + n
	}
	for i := 0; i < len(s); {
		r := s[i]
		switch {
		case r == '\\':
			if i+1 == len(s) {
				if !final {
					return b.String(), i
				}
				i = literal(i, 1)
			} else if unicode.IsPunct(s[i+1]) || unicode.IsSymbol(s[i+1]) {
				i = literal(i+1, 1)
			} else {
				i = literal(i, 1)
			}
		case r == '`':
			n := runLength(s, i)
			end := -1
			if i+n < len(s) {
				end = closer(s, i+n, '`', n, final)
			}
			if end < 0 {
				if !final {
					return b.String(), i
				}
				i = literal(i, n)
				break
			}
			b.WriteString(codeOn + string(s[i+n:end]) + codeOff)
			i = end + n
		case r == '*' || r == '_':
			n := runLength(s, i)
			if i+n == len(s) && !final {
				return b.String(), i
			}
			if n > 3 || !opens(s, i, n) {
				i = literal(i, n)
				break
			}
			end := closer(s, i+n, r, n, final)
			if end < 0 {
				if !final {
					return b.String(), i
				}
				i = literal(i, n)
				break
			}
			inner, _ := m.inline(s[i+n:end], true)
			if n >= 2 && !m.heading {
				inner = boldOn + inner + boldOff
			}
			if n != 2 {
				inner = italicOn + inner + italicOff
			}
			b.WriteString(inner)
			i = end + n
		case r == '[':
			text, url, next, ok := link(s, i)
			switch {
			case ok:
				label, _ := m.inline([]rune(text), true)
				b.WriteString(underlineOn + label + underlineOff)
				if url != text {
					b.WriteString(" " + dimOn + "(" + url + ")" + dimOff)
				}
				i = next
			case next < 0 && !final:
				return b.String(), i
			default:
				i = literal(i, 1)
			}
		default:
			i = literal(i, 1)
		}
	}
	return b.String(), len(s)
}

func runLength(s []rune, i int) int {
	n := 1
	for i+n < len(s) && s[i+n] == s[i] {
		n++
	}
	return n
}

// opens reports whether the marks at s[i] can start an emphasis: text must
// follow at once, and an underscore in the middle of a word is just a letter.
func opens(s []rune, i, n int) bool {
	if i+n >= len(s) || unicode.IsSpace(s[i+n]) {
		return false
	}
	return s[i] != '_' || i == 0 || !wordRune(s[i-1])
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// closer finds where the run of n marks that ends a span begins, from index
// from on, or -1. A run that touches the end of s may still grow or be
// followed by a letter, so it only counts when the line has ended.
func closer(s []rune, from int, mark rune, n int, final bool) int {
	for i := from; i < len(s); {
		if s[i] != mark {
			i++
			continue
		}
		length := runLength(s, i)
		atEnd := i+length == len(s)
		ok := length == n && (final || !atEnd)
		if mark != '`' {
			// An emphasis has text inside and ends right after a character.
			ok = ok && i > from && !unicode.IsSpace(s[i-1])
		}
		if mark == '_' {
			ok = ok && (atEnd || !wordRune(s[i+length]))
		}
		if ok {
			return i
		}
		i += length
	}
	return -1
}

// link reads [text](url) at s[i]. next is the index after it; -1 when the
// link may still be arriving.
func link(s []rune, i int) (text, url string, next int, ok bool) {
	end := indexOf(s, i+1, ']')
	if end < 0 || end+1 == len(s) {
		return "", "", -1, false
	}
	if s[end+1] != '(' {
		return "", "", i + 1, false
	}
	closing := indexOf(s, end+2, ')')
	if closing < 0 {
		return "", "", -1, false
	}
	return string(s[i+1 : end]), string(s[end+2 : closing]), closing + 1, true
}

func indexOf(s []rune, from int, r rune) int {
	for i := from; i < len(s); i++ {
		if s[i] == r {
			return i
		}
	}
	return -1
}

// flushTable prints the rows collected so far as a table with aligned
// columns. Rows that do not form a table, or a table wider than the terminal,
// go out as they came.
func (m *markdown) flushTable(out *strings.Builder) {
	rows := m.table
	m.table = nil
	if len(rows) == 0 {
		return
	}
	raw := func() {
		for _, row := range rows {
			done, _ := m.inline([]rune(row), true)
			out.WriteString(done + "\n")
		}
	}
	if len(rows) < 2 {
		raw()
		return
	}
	divider := cells(rows[1])
	for _, cell := range divider {
		if !dividerRe.MatchString(cell) {
			raw()
			return
		}
	}

	var table [][]string
	widths := make([]int, len(divider))
	for i, row := range rows {
		if i == 1 {
			continue
		}
		styled := make([]string, len(divider))
		for col, cell := range cells(row) {
			if col >= len(divider) {
				break
			}
			styled[col], _ = m.inline([]rune(cell), true)
			widths[col] = max(widths[col], visibleWidth(styled[col]))
		}
		table = append(table, styled)
	}
	total := 3 * (len(widths) - 1)
	for _, w := range widths {
		total += w
	}
	if cols := m.columns(); cols > 0 && total > cols {
		raw()
		return
	}

	for i, row := range table {
		for col, cell := range row {
			if col > 0 {
				out.WriteString(dimOn + " │ " + dimOff)
			}
			pad := strings.Repeat(" ", widths[col]-visibleWidth(cell))
			if i == 0 {
				cell = boldOn + cell + boldOff
			}
			if strings.HasSuffix(divider[col], ":") && !strings.HasPrefix(divider[col], ":") {
				out.WriteString(pad + cell)
			} else if col == len(row)-1 {
				out.WriteString(cell)
			} else {
				out.WriteString(cell + pad)
			}
		}
		out.WriteString("\n")
		if i == 0 {
			parts := make([]string, len(widths))
			for col, w := range widths {
				parts[col] = strings.Repeat("─", w)
			}
			out.WriteString(dimOn + strings.Join(parts, "─┼─") + dimOff + "\n")
		}
	}
}

func (m *markdown) columns() int {
	if m.width == nil {
		return 0
	}
	return m.width()
}

// cells splits a table row at the bars that are not escaped.
func cells(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	if strings.HasSuffix(row, "|") && !strings.HasSuffix(row, `\|`) {
		row = strings.TrimSuffix(row, "|")
	}
	var out []string
	var cell strings.Builder
	runes := []rune(row)
	for i := 0; i < len(runes); i++ {
		switch {
		case runes[i] == '\\' && i+1 < len(runes) && runes[i+1] == '|':
			cell.WriteRune('|')
			i++
		case runes[i] == '|':
			out = append(out, strings.TrimSpace(cell.String()))
			cell.Reset()
		default:
			cell.WriteRune(runes[i])
		}
	}
	return append(out, strings.TrimSpace(cell.String()))
}

// visibleWidth counts the characters of a styled text that take room on the
// screen.
func visibleWidth(styled string) int {
	return utf8.RuneCountInString(styleRe.ReplaceAllString(styled, ""))
}
