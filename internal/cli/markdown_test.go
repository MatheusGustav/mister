package cli

import (
	"strings"
	"testing"
)

func bold(s string) string      { return boldOn + s + boldOff }
func dim(s string) string       { return dimOn + s + dimOff }
func italic(s string) string    { return italicOn + s + italicOff }
func code(s string) string      { return codeOn + s + codeOff }
func underline(s string) string { return underlineOn + s + underlineOff }

var markdownCases = []struct {
	name string
	in   string
	want string
}{
	{"título", "# Título do texto\n", bold("Título do texto") + "\n"},
	{"negrito dentro de título", "## Um **dois** três\n", bold("Um dois três") + "\n"},
	{"ênfases", "um **forte** e *leve* e `cod` e ***os dois***\n",
		"um " + bold("forte") + " e " + italic("leve") + " e " + code("cod") + " e " + italic(bold("os dois")) + "\n"},
	{"sublinhado como ênfase", "isto é __forte__ e _leve_ aqui\n", "isto é " + bold("forte") + " e " + italic("leve") + " aqui\n"},
	{"listas", "- item um\n  * sub item\n1. primeiro passo\n", "• item um\n  • sub item\n1. primeiro passo\n"},
	{"citação", "> citação longa aqui\n", dim("│ ") + "citação longa aqui\n"},
	{"bloco de código", "```go\nx := a*b*c // **não**\n```\ndepois do bloco\n",
		dim("```go") + "\n" + code("x := a*b*c // **não**") + "\n" + dim("```") + "\ndepois do bloco\n"},
	{"link", "veja [o site](https://x.y) agora\n", "veja " + underline("o site") + " " + dim("(https://x.y)") + " agora\n"},
	{"colchete que não é link", "a lista [1, 2] e o item [x] ficam\n", "a lista [1, 2] e o item [x] ficam\n"},
	{"nome com sublinhado", "use snake_case_name e __init__.py aqui\n", "use snake_case_name e " + bold("init") + ".py aqui\n"},
	{"asterisco solto", "2 * 3 * 4 e também *.go e *.md\n", "2 * 3 * 4 e também *.go e *.md\n"},
	{"ênfase que não fecha", "isto **não fecha nunca\n", "isto **não fecha nunca\n"},
	{"escape", `\*literal\* e \# também` + "\n", "*literal* e # também\n"},
	{"régua", "antes\n\n---\n\ndepois\n", "antes\n\n" + dim(strings.Repeat("─", 40)) + "\n\ndepois\n"},
	{"tabela", "| a | b |\n|---|--:|\n| 1 | **22** |\nfim da tabela\n",
		bold("a") + dim(" │ ") + " " + bold("b") + "\n" + dim("─"+"─┼─"+"──") + "\n" +
			"1" + dim(" │ ") + bold("22") + "\nfim da tabela\n"},
	{"barra que não é tabela", "| só uma linha\n\nsegue o texto\n", "| só uma linha\n\nsegue o texto\n"},
	{"sem quebra no fim", "fim sem quebra de linha", "fim sem quebra de linha\n"},
	{"linha curta", "ok\n", "ok\n"},
}

func render(pieces []string) string {
	var out strings.Builder
	m := &markdown{}
	for _, piece := range pieces {
		out.WriteString(m.Write(piece))
	}
	out.WriteString(m.Flush())
	// A code line printed in pieces opens and closes its colour at each piece.
	return strings.ReplaceAll(out.String(), codeOff+codeOn, "")
}

func TestMarkdown(t *testing.T) {
	t.Parallel()
	for _, tt := range markdownCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := render([]string{tt.in}); got != tt.want {
				t.Errorf("de uma vez:\n saiu %q\nquero %q", got, tt.want)
			}
			var runes []string
			for _, r := range tt.in {
				runes = append(runes, string(r))
			}
			if got := render(runes); got != tt.want {
				t.Errorf("letra por letra:\n saiu %q\nquero %q", got, tt.want)
			}
			for size := 2; size <= 7; size++ {
				var pieces []string
				for rest := []rune(tt.in); len(rest) > 0; {
					n := min(size, len(rest))
					pieces = append(pieces, string(rest[:n]))
					rest = rest[n:]
				}
				if got := render(pieces); got != tt.want {
					t.Errorf("em pedaços de %d:\n saiu %q\nquero %q", size, got, tt.want)
				}
			}
		})
	}
}

// A paragraph shows up while it arrives, without waiting for its end.
func TestMarkdown_PrintsTheParagraphAsItArrives(t *testing.T) {
	t.Parallel()
	m := &markdown{}
	if got := m.Write("um texto que **ainda"); got != "um texto que " {
		t.Errorf("o que já está decidido deveria sair: %q", got)
	}
	if got := m.Write(" chega** aos poucos"); got != bold("ainda chega")+" aos poucos" {
		t.Errorf("a ênfase sai quando fecha: %q", got)
	}
	if got := m.Flush(); got != "\n" {
		t.Errorf("no fim só falta a quebra: %q", got)
	}
}

func TestMarkdown_TableWiderThanTheTerminalStaysAsItCame(t *testing.T) {
	t.Parallel()
	m := &markdown{width: func() int { return 10 }}
	table := "| coluna um | coluna dois |\n|---|---|\n| valor | outro valor |\n"
	if got := m.Write(table) + m.Flush(); got != table {
		t.Errorf("tabela que não cabe sai como veio: %q", got)
	}
}

// Whatever was waiting goes out before a status line, and the text that
// follows starts clean.
func TestPrinter_FlushesMarkdownBeforeStatus(t *testing.T) {
	t.Parallel()
	var screen strings.Builder
	p := &printer{out: &screen, err: &screen, md: &markdown{}}
	p.text("vou pesquisar *isso")
	p.status("  › [host] pesquisar_web")
	p.text("# Resultado da busca\n")
	p.endLine()
	want := "vou pesquisar *isso\n  › [host] pesquisar_web\n" + bold("Resultado da busca") + "\n"
	if screen.String() != want {
		t.Errorf("tela:\n saiu %q\nquero %q", screen.String(), want)
	}
}
