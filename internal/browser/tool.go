package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/MatheusGustav/mister/internal/tool"
)

// ToolName is the name agents call the screenshot tool by.
const ToolName = "tirar_print"

const (
	defaultSize = "desktop"
	minSide     = 200
	// maxSide is the longest edge the model reads without shrinking the picture.
	maxSide = 2576
)

var presets = map[string]Size{
	"celular": {Name: "celular", Width: 390, Height: 844, Mobile: true},
	"tablet":  {Name: "tablet", Width: 768, Height: 1024, Mobile: true},
	"desktop": {Name: "desktop", Width: 1440, Height: 900},
}

func presetNames() []string {
	names := make([]string, 0, len(presets))
	for name := range presets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type toolInput struct {
	URL           string   `json:"url"`
	Tamanho       string   `json:"tamanho"`
	Tamanhos      []string `json:"tamanhos"`
	Largura       int      `json:"largura"`
	Altura        int      `json:"altura"`
	PaginaInteira bool     `json:"pagina_inteira"`
}

// Tool is the screenshot tool: one page, one picture per size.
func (m *Manager) Tool() tool.Tool {
	names := presetNames()
	return tool.Tool{
		Name: ToolName,
		Description: "Abre uma página no navegador do Mister e devolve o print dela, para conferir a aparência: botões, alinhamento, texto cortado, nada vazando da tela. " +
			"A página precisa estar no ar: suba o servidor do projeto antes, em segundo plano, e derrube depois dos prints. " +
			"Cada chamada abre uma aba limpa, sem login nem cookie de outra chamada, carrega a página, captura e fecha: tela que exige login cai na tela de login. " +
			"Com 'tamanhos' vem um print de cada tamanho na mesma chamada. Com muitos pedidos ao mesmo tempo, o seu espera a vez na fila.",
		Schema: tool.Object([]string{"url"}, map[string]any{
			"url": tool.Str("endereço da página, com http:// ou https:// (ex.: http://localhost:8000/painel)"),
			"tamanho": map[string]any{"type": "string", "enum": names,
				"description": "tamanho da tela: celular (390x844), tablet (768x1024) ou desktop (1440x900). Opcional, padrão desktop"},
			"tamanhos": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": names},
				"description": "vários tamanhos na mesma chamada, um print de cada (ex.: [\"desktop\", \"celular\"]). Use no lugar de 'tamanho'"},
			"largura": tool.Int(fmt.Sprintf("largura da tela em pixels, de %d a %d, para um tamanho fora dos três. Vem junto de 'altura', no lugar de 'tamanho'", minSide, maxSide)),
			"altura":  tool.Int(fmt.Sprintf("altura da tela em pixels, de %d a %d. Vem junto de 'largura'", minSide, maxSide)),
			"pagina_inteira": map[string]any{"type": "boolean",
				"description": fmt.Sprintf("captura a página toda, não só o que cabe na tela. Página alta vem em partes de %d pixels de altura, até %d partes. Opcional, padrão false", sliceHeight, maxSlices)},
		}),
		Parallel: true,
		Run: func(ctx context.Context, _ *tool.Env, raw json.RawMessage) tool.Result {
			in, err := tool.Decode[toolInput](raw)
			if err != nil {
				return tool.Fail("%v", err)
			}
			address, err := checkURL(in.URL)
			if err != nil {
				return tool.Fail("%v", err)
			}
			sizes, err := parseSizes(in)
			if err != nil {
				return tool.Fail("%v", err)
			}
			shots, waited, err := m.Capture(ctx, Request{URL: address, Sizes: sizes, FullPage: in.PaginaInteira})
			if err != nil {
				return tool.Fail("%v", err)
			}
			return report(address, shots, waited)
		},
	}
}

// checkURL accepts only pages served over HTTP: the browser must not be a way
// to read files from the disk.
func checkURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("'url' veio vazia. Informe o endereço da página, com http:// na frente")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("'url' precisa ser um endereço completo, começando com http:// ou https:// (veio %q). Ex.: http://localhost:8000/painel", raw)
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String(), nil
}

// samePage compares two addresses without the part after the #, which the
// browser does not report back.
func samePage(a, b string) bool {
	a, _, _ = strings.Cut(a, "#")
	b, _, _ = strings.Cut(b, "#")
	return a == b
}

// parseSizes resolves the three ways of choosing the screen: one name, a list
// of names or a width and a height. With none, it is the desktop.
func parseSizes(in toolInput) ([]Size, error) {
	custom := in.Largura != 0 || in.Altura != 0
	switch {
	case in.Tamanho != "" && len(in.Tamanhos) > 0:
		return nil, fmt.Errorf("use 'tamanho' ou 'tamanhos', não os dois")
	case custom && (in.Tamanho != "" || len(in.Tamanhos) > 0):
		return nil, fmt.Errorf("'largura' e 'altura' são para um tamanho fora dos três: não combine com 'tamanho' nem 'tamanhos'")
	case custom && (in.Largura == 0 || in.Altura == 0):
		return nil, fmt.Errorf("'largura' e 'altura' vêm juntas: informe as duas")
	case custom:
		for name, side := range map[string]int{"largura": in.Largura, "altura": in.Altura} {
			if side < minSide || side > maxSide {
				return nil, fmt.Errorf("'%s' precisa ficar entre %d e %d pixels (veio %d)", name, minSide, maxSide, side)
			}
		}
		return []Size{{Width: in.Largura, Height: in.Altura}}, nil
	}

	names := in.Tamanhos
	if in.Tamanho != "" {
		names = []string{in.Tamanho}
	}
	if len(names) == 0 {
		names = []string{defaultSize}
	}
	var sizes []Size
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		size, ok := presets[name]
		if !ok {
			return nil, fmt.Errorf("tamanho %q não existe. Use %s, ou 'largura' e 'altura' para outro tamanho", name, strings.Join(presetNames(), ", "))
		}
		if !seen[name] {
			seen[name] = true
			sizes = append(sizes, size)
		}
	}
	return sizes, nil
}

// report writes what the agent reads before the pictures: one line per size,
// with what the picture alone does not tell.
func report(address string, shots []Shot, waited time.Duration) tool.Result {
	var b strings.Builder
	var res tool.Result
	if waited > time.Second {
		fmt.Fprintf(&b, "[esperou %s na fila do navegador]\n", waited.Round(time.Second))
	}
	fmt.Fprintf(&b, "Print de %s:\n", address)
	for _, shot := range shots {
		fmt.Fprintf(&b, "- %s", shot.Size)
		if shot.Status != 0 {
			fmt.Fprintf(&b, ", HTTP %d", shot.Status)
		}
		if shot.Title != "" {
			fmt.Fprintf(&b, ", título %q", shot.Title)
		}
		if shot.Status >= 400 {
			b.WriteString(". Atenção: a página respondeu com erro")
		}
		if shot.FinalURL != "" && !samePage(shot.FinalURL, address) {
			fmt.Fprintf(&b, ". Atenção: a página foi parar em %s", shot.FinalURL)
		}
		if shot.Overflow > 0 {
			fmt.Fprintf(&b, ". Atenção: o conteúdo tem %dpx de largura e vaza da tela (rolagem para o lado)", shot.Overflow)
		}
		if shot.Cut {
			fmt.Fprintf(&b, ". A página tem %dpx de altura: o print vai só até %dpx", shot.PageHeight, sliceHeight*maxSlices)
		}
		b.WriteString("\n")
		res.Images = append(res.Images, shot.Images...)
	}
	res.Content = strings.TrimRight(b.String(), "\n")
	return res
}
