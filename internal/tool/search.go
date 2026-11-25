package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	searchMaxMatches  = 200
	searchMaxLineLen  = 300
	searchMaxFileSize = 2 << 20
	globMaxResults    = 500
)

// skippedDirs are never searched: version control and dependency trees.
var skippedDirs = map[string]bool{".git": true, "node_modules": true, ".venv": true, "__pycache__": true}

// SearchTools returns the content search and the glob tool. ownRipgrep is the
// ripgrep 'mister install' keeps; when that file is not there the search uses
// the one in PATH, and without any it walks the files itself.
func SearchTools(ownRipgrep string) []Tool {
	return []Tool{grepTool(ownRipgrep), globTool()}
}

func ripgrepPath(own string) string {
	if own != "" {
		if info, err := os.Stat(own); err == nil && !info.IsDir() {
			return own
		}
	}
	if found, err := exec.LookPath("rg"); err == nil {
		return found
	}
	return ""
}

func grepTool(ownRipgrep string) Tool {
	type input struct {
		Padrao  string `json:"padrao"`
		Caminho string `json:"caminho"`
		Glob    string `json:"glob"`
	}
	return Tool{
		Name: "buscar",
		Description: "Procura um texto ou expressão regular dentro dos arquivos do projeto e devolve arquivo:linha:trecho. " +
			"Use 'caminho' para limitar a uma pasta ou arquivo e 'glob' para filtrar pelos nomes (ex.: *.go). " +
			fmt.Sprintf("Devolve no máximo %d ocorrências.", searchMaxMatches),
		Schema: object([]string{"padrao"}, map[string]any{
			"padrao":  str("expressão regular (sintaxe RE2); para texto literal com símbolos, escape com \\"),
			"caminho": str("pasta ou arquivo onde procurar (opcional, padrão: a pasta de trabalho)"),
			"glob":    str("filtro de nome de arquivo, ex.: *.go ou *.{ts,tsx} (opcional)"),
		}),
		Parallel: true,
		Run: func(ctx context.Context, env *Env, raw json.RawMessage) Result {
			in, err := decode[input](raw)
			if err != nil {
				return Fail("%v", err)
			}
			if in.Padrao == "" {
				return Fail("'padrao' veio vazio. Informe o texto ou a expressão a procurar.")
			}
			if in.Caminho == "" {
				in.Caminho = "."
			}
			root, err := env.Workspace.Resolve(in.Caminho, false)
			if err != nil {
				return Fail("%v", err)
			}
			if _, err := os.Stat(root); err != nil {
				return Fail("%s", describeFSError("procurar em", in.Caminho, err))
			}
			re, err := regexp.Compile(in.Padrao)
			if err != nil {
				return Fail("expressão regular inválida: %v. Para procurar texto literal, escape os símbolos com \\.", err)
			}

			var lines []string
			if rg := ripgrepPath(ownRipgrep); rg != "" {
				lines, err = grepWithRipgrep(ctx, rg, in.Padrao, in.Glob, root)
			} else {
				lines, err = grepWalking(ctx, re, in.Glob, root)
			}
			if err != nil {
				return Fail("a busca falhou: %v", err)
			}
			if len(lines) == 0 {
				return OK("nenhuma ocorrência de %q.", in.Padrao)
			}
			return OK("%s", formatMatches(lines, env.Workspace))
		},
	}
}

func grepWithRipgrep(ctx context.Context, rg, pattern, glob, root string) ([]string, error) {
	args := []string{"--line-number", "--no-heading", "--with-filename", "--color", "never",
		"--max-columns", fmt.Sprint(searchMaxLineLen), "--max-columns-preview", "--hidden", "--glob", "!.git"}
	if glob != "" {
		args = append(args, "--glob", glob)
	}
	args = append(args, "--regexp", pattern, "--", root)

	cmd := exec.CommandContext(ctx, rg, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		// Exit code 1 is ripgrep's "no match".
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil, nil
		}
		// Exit code 2 with matches: a path could not be read (no permission),
		// the rest was searched.
		if !errors.As(err, &exit) || exit.ExitCode() != 2 || stdout.Len() == 0 {
			return nil, fmt.Errorf("ripgrep: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
	}
	return splitLines(stdout.String(), searchMaxMatches+1), nil
}

// grepWalking is the search used when ripgrep is not installed.
func grepWalking(ctx context.Context, re *regexp.Regexp, glob, root string) ([]string, error) {
	var lines []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if skippedDirs[d.Name()] && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if glob != "" && !matchName(glob, d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > searchMaxFileSize {
			return nil
		}
		lines = append(lines, grepFile(re, path)...)
		if len(lines) > searchMaxMatches {
			return filepath.SkipAll
		}
		return nil
	})
	return lines, err
}

func grepFile(re *regexp.Regexp, path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for n := 1; scanner.Scan(); n++ {
		line := scanner.Text()
		if !utf8.ValidString(line) || strings.ContainsRune(line, 0) {
			return nil
		}
		if re.MatchString(line) {
			if len(line) > searchMaxLineLen {
				line = line[:searchMaxLineLen] + "…"
			}
			out = append(out, fmt.Sprintf("%s:%d:%s", path, n, line))
		}
	}
	return out
}

func formatMatches(lines []string, ws *Workspace) string {
	var out strings.Builder
	prefix := ws.Root() + string(os.PathSeparator)
	for i, line := range lines {
		if i == searchMaxMatches {
			fmt.Fprintf(&out, "[há mais ocorrências: refine o padrão, o caminho ou o glob]\n")
			break
		}
		out.WriteString(strings.TrimPrefix(line, prefix))
		out.WriteByte('\n')
	}
	return out.String()
}

func splitLines(text string, limit int) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		out = append(out, line)
		if len(out) == limit {
			break
		}
	}
	return out
}

// matchName matches a file name against a glob, with {a,b} alternatives.
func matchName(glob, name string) bool {
	for _, g := range expandBraces(glob) {
		if ok, _ := filepath.Match(filepath.Base(g), name); ok {
			return true
		}
	}
	return false
}

func expandBraces(pattern string) []string {
	open := strings.IndexByte(pattern, '{')
	if open < 0 {
		return []string{pattern}
	}
	closing := strings.IndexByte(pattern[open:], '}')
	if closing < 0 {
		return []string{pattern}
	}
	closing += open
	var out []string
	for _, alt := range strings.Split(pattern[open+1:closing], ",") {
		out = append(out, expandBraces(pattern[:open]+alt+pattern[closing+1:])...)
	}
	return out
}

func globTool() Tool {
	type input struct {
		Padrao  string `json:"padrao"`
		Caminho string `json:"caminho"`
	}
	return Tool{
		Name: "glob",
		Description: "Acha arquivos pelo nome com um padrão glob: * casa dentro de uma pasta, ** atravessa pastas, {a,b} são alternativas. " +
			"Exemplos: **/*.go, internal/**/*_test.go, *.{json,md}. Devolve os caminhos relativos à pasta de trabalho.",
		Schema: object([]string{"padrao"}, map[string]any{
			"padrao":  str("padrão glob"),
			"caminho": str("pasta de onde partir (opcional, padrão: a pasta de trabalho)"),
		}),
		Parallel: true,
		Run: func(ctx context.Context, env *Env, raw json.RawMessage) Result {
			in, err := decode[input](raw)
			if err != nil {
				return Fail("%v", err)
			}
			if in.Padrao == "" {
				return Fail("'padrao' veio vazio. Exemplo: **/*.go")
			}
			if in.Caminho == "" {
				in.Caminho = "."
			}
			root, err := env.Workspace.Resolve(in.Caminho, false)
			if err != nil {
				return Fail("%v", err)
			}
			if info, err := os.Stat(root); err != nil || !info.IsDir() {
				return Fail("%q não é uma pasta que existe.", in.Caminho)
			}
			patterns := expandBraces(filepath.ToSlash(in.Padrao))
			for _, p := range patterns {
				if _, err := filepath.Match(strings.ReplaceAll(p, "**", "*"), ""); err != nil {
					return Fail("padrão glob inválido %q: %v", in.Padrao, err)
				}
			}

			var found []string
			walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if d.IsDir() {
					if skippedDirs[d.Name()] && path != root {
						return filepath.SkipDir
					}
					return nil
				}
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return nil
				}
				for _, p := range patterns {
					if matchPath(strings.Split(p, "/"), strings.Split(filepath.ToSlash(rel), "/")) {
						found = append(found, env.Workspace.Rel(path))
						break
					}
				}
				if len(found) > globMaxResults {
					return filepath.SkipAll
				}
				return nil
			})
			if walkErr != nil {
				return Fail("a busca por nome falhou: %v", walkErr)
			}
			if len(found) == 0 {
				return OK("nenhum arquivo casa com %q.", in.Padrao)
			}
			sort.Strings(found)
			truncated := len(found) > globMaxResults
			if truncated {
				found = found[:globMaxResults]
			}
			out := strings.Join(found, "\n") + "\n"
			if truncated {
				out += "[há mais arquivos: refine o padrão]\n"
			}
			return OK("%s", out)
		},
	}
}

// matchPath matches path segments against pattern segments, where "**" stands
// for any number of directories.
func matchPath(pattern, path []string) bool {
	if len(pattern) == 0 {
		return len(path) == 0
	}
	if pattern[0] == "**" {
		for i := 0; i <= len(path); i++ {
			if matchPath(pattern[1:], path[i:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	if ok, _ := filepath.Match(pattern[0], path[0]); !ok {
		return false
	}
	return matchPath(pattern[1:], path[1:])
}
