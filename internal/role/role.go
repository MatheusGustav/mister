// Package role loads agent roles: instructions, allowed tools and model. A
// role is a markdown file with a small header, so a new role needs no code.
// To the user a role is an "agente" ('mister agentes', ~/.mister/agentes); the
// code keeps "role" for the definition and "agent" for the one running.
package role

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed agentes/*.md
var native embed.FS

// Names of the native roles.
const (
	Host   = "host"
	Thread = "thread"
)

type Role struct {
	Name        string
	Description string
	// Model is a short name from the config ("opus") or a full model id.
	Model  string
	Effort string
	Tools  []string
	// Instructions is the fixed text every agent of the role starts with.
	Instructions string
	// Source is the file the role came from ("nativo" for the built-in ones).
	Source string
}

var validEfforts = map[string]bool{"": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// ValidEffort reports whether the effort name is one the roles accept.
func ValidEffort(effort string) bool { return validEfforts[effort] }

// Load returns the native roles overridden by the roles found in dirs, in
// order: a later directory wins over an earlier one.
func Load(dirs ...string) (map[string]Role, error) {
	roles := make(map[string]Role)

	entries, err := native.ReadDir("agentes")
	if err != nil {
		return nil, fmt.Errorf("lendo os agentes nativos: %w", err)
	}
	for _, e := range entries {
		data, err := native.ReadFile("agentes/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("lendo o agente nativo %s: %w", e.Name(), err)
		}
		r, err := Parse(string(data), strings.TrimSuffix(e.Name(), ".md"))
		if err != nil {
			return nil, fmt.Errorf("agente nativo %s: %w", e.Name(), err)
		}
		r.Source = "nativo"
		roles[r.Name] = r
	}

	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.md"))
		if err != nil {
			return nil, fmt.Errorf("listando os agentes de %s: %w", dir, err)
		}
		sort.Strings(files)
		for _, file := range files {
			data, err := os.ReadFile(file)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("lendo o agente %s: %w", file, err)
			}
			r, err := Parse(string(data), strings.TrimSuffix(filepath.Base(file), ".md"))
			if err != nil {
				return nil, fmt.Errorf("agente %s: %w", file, err)
			}
			r.Source = file
			roles[r.Name] = r
		}
	}
	return roles, nil
}

// Parse reads a role file: a header between "---" lines with "key: value"
// pairs (name, description, model, effort, tools), then the instructions.
func Parse(content, defaultName string) (Role, error) {
	r := Role{Name: defaultName}
	body := content

	if rest, ok := strings.CutPrefix(strings.TrimLeft(content, "\xef\xbb\xbf \t\r\n"), "---"); ok {
		header, after, found := strings.Cut(rest, "\n---")
		if !found {
			return r, errors.New("o cabeçalho abre com '---' mas não fecha")
		}
		body = strings.TrimPrefix(after, "-")
		for n, line := range strings.Split(header, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				return r, fmt.Errorf("linha %d do cabeçalho sem 'chave: valor': %q", n+1, line)
			}
			value = strings.Trim(strings.TrimSpace(value), `"'`)
			switch strings.TrimSpace(key) {
			case "name":
				r.Name = value
			case "description":
				r.Description = value
			case "model":
				r.Model = value
			case "effort":
				r.Effort = value
			case "tools":
				r.Tools = splitList(value)
			default:
				return r, fmt.Errorf("chave desconhecida no cabeçalho: %q (aceitas: name, description, model, effort, tools)", strings.TrimSpace(key))
			}
		}
	}

	r.Instructions = strings.TrimSpace(body)
	switch {
	case r.Name == "":
		return r, errors.New("agente sem nome")
	case r.Instructions == "":
		return r, errors.New("agente sem instruções")
	case !validEfforts[r.Effort]:
		return r, fmt.Errorf("effort %q inválido (aceitos: low, medium, high, xhigh, max)", r.Effort)
	}
	return r, nil
}

func splitList(value string) []string {
	value = strings.Trim(value, "[]")
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.Trim(strings.TrimSpace(item), `"'`); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// System builds the fixed prompt of a role: its instructions followed by the
// owner's rules and the project's rules. Agents of the same role in the same
// project get the same text, byte for byte, so they share the prompt cache.
func System(r Role, ownerRules, projectRules string) string {
	var b strings.Builder
	b.WriteString(r.Instructions)
	if rules := strings.TrimSpace(ownerRules); rules != "" {
		b.WriteString("\n\n# Regras do dono\n\n")
		b.WriteString(rules)
	}
	if rules := strings.TrimSpace(projectRules); rules != "" {
		b.WriteString("\n\n# Regras do projeto\n\n")
		b.WriteString(rules)
	}
	return b.String()
}
