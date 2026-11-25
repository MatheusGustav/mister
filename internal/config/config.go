// Package config resolves the Mister home directory and its settings file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// HomeEnv overrides the default home directory (~/.mister).
const HomeEnv = "MISTER_HOME"

// Config is the content of config.json. A missing field keeps its default.
type Config struct {
	// Models maps the short names roles use to the provider's model ids.
	Models map[string]string `json:"modelos"`
	// MaxThreads is how many threads may run at the same time.
	MaxThreads int `json:"max_threads"`
	// HeavyLimit is how many heavy commands may run at the same time.
	HeavyLimit int `json:"limite_pesado"`
	// CompactTokens is the context size that triggers a compaction.
	CompactTokens int `json:"compactar_tokens"`
	// MaxTokens is the output limit of one model answer.
	MaxTokens int `json:"max_tokens"`
	// MaxIterations stops an agent run that never ends.
	MaxIterations int `json:"max_iteracoes"`
	// WebSearch is "auto" (the provider's native search when it has one, Exa
	// otherwise) or "exa".
	WebSearch string `json:"pesquisa_web"`
	// WebSearchMaxUses limits native searches per model answer.
	WebSearchMaxUses int `json:"pesquisa_web_max_usos"`
	// Embedding is the provider the memory searches with.
	Embedding Embedding `json:"embedding"`
}

// Embedding configures the embedding provider. With an empty URL the Mister
// runs its own local server (installed by 'mister install'); with a URL it
// talks to any server in the OpenAI /v1/embeddings format.
type Embedding struct {
	// URL is the API root, for example "http://localhost:11434/v1".
	URL   string `json:"url"`
	Model string `json:"modelo"`
	// KeyEnv names the environment variable with the API key, when one is needed.
	KeyEnv string `json:"chave_env"`
	// QueryPrefix and NotePrefix are the task prefixes of the model; "{titulo}"
	// in NotePrefix becomes the title of the note.
	QueryPrefix string `json:"prefixo_busca"`
	NotePrefix  string `json:"prefixo_nota"`
	// EntryThreshold and WalkThreshold are the least similarity for a note to
	// be recalled and for a linked note to be followed. They depend on the model.
	EntryThreshold float64 `json:"limiar_entrada"`
	WalkThreshold  float64 `json:"limiar_caminhada"`
}

func Default() Config {
	return Config{
		Models: map[string]string{
			"fable":  "claude-fable-5-2",
			"opus":   "claude-opus-5-5",
			"sonnet": "claude-sonnet-5-5",
			"haiku":  "claude-haiku-5-5",
		},
		MaxThreads:       3,
		HeavyLimit:       1,
		CompactTokens:    120_000,
		MaxTokens:        32_000,
		MaxIterations:    200,
		WebSearch:        "auto",
		WebSearchMaxUses: 5,
		// Prefixes and thresholds of EmbeddingGemma 300M, measured in Portuguese
		// on 24/11/2026: the right note scores 0.52 or more, an unrelated one
		// 0.37 or less.
		Embedding: Embedding{
			Model:          "embeddinggemma-300m",
			QueryPrefix:    "task: search result | query: ",
			NotePrefix:     "title: {titulo} | text: ",
			EntryThreshold: 0.45,
			WalkThreshold:  0.40,
		},
	}
}

// Model resolves a short name to a model id; an unknown name is taken as an id.
func (c Config) Model(name string) string {
	if id, ok := c.Models[name]; ok {
		return id
	}
	return name
}

// Home is the directory with the settings, sessions, roles and memory.
type Home struct {
	Dir string
}

func ResolveHome() (Home, error) {
	if dir := os.Getenv(HomeEnv); dir != "" {
		return Home{Dir: dir}, nil
	}
	user, err := os.UserHomeDir()
	if err != nil {
		return Home{}, fmt.Errorf("localizando a pasta do usuário: %w", err)
	}
	return Home{Dir: filepath.Join(user, ".mister")}, nil
}

func (h Home) ConfigFile() string   { return filepath.Join(h.Dir, "config.json") }
func (h Home) RolesDir() string     { return filepath.Join(h.Dir, "papeis") }
func (h Home) SessionsDir() string  { return filepath.Join(h.Dir, "sessoes") }
func (h Home) MemoryDir() string    { return filepath.Join(h.Dir, "memoria") }
func (h Home) RulesFile() string    { return filepath.Join(h.Dir, "MISTER.md") }
func (h Home) EmbeddingDir() string { return filepath.Join(h.Dir, "embedding") }
func (h Home) IndexFile() string    { return filepath.Join(h.Dir, "indice.json") }

// Ensure creates the home directory and its subfolders.
func (h Home) Ensure() error {
	for _, dir := range []string{h.Dir, h.RolesDir(), h.SessionsDir(), h.MemoryDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("criando %s: %w", dir, err)
		}
	}
	return nil
}

// Load reads config.json over the defaults. A missing file is not an error.
func (h Home) Load() (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(h.ConfigFile())
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("lendo %s: %w", h.ConfigFile(), err)
	}
	// Unmarshal merges "modelos" into the default map, so the short names the
	// file does not mention keep working.
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s não é um JSON válido: %w", h.ConfigFile(), err)
	}
	if cfg.Models == nil {
		cfg.Models = Default().Models
	}
	return cfg, cfg.validate()
}

func (c Config) validate() error {
	switch {
	case c.MaxThreads < 1:
		return errors.New("config: max_threads precisa ser 1 ou mais")
	case c.HeavyLimit < 1:
		return errors.New("config: limite_pesado precisa ser 1 ou mais")
	case c.CompactTokens < 1000:
		return errors.New("config: compactar_tokens precisa ser 1000 ou mais")
	case c.MaxTokens < 1:
		return errors.New("config: max_tokens precisa ser 1 ou mais")
	case c.MaxIterations < 1:
		return errors.New("config: max_iteracoes precisa ser 1 ou mais")
	case c.WebSearch != "auto" && c.WebSearch != "exa":
		return fmt.Errorf("config: pesquisa_web %q inválido; use \"auto\" ou \"exa\"", c.WebSearch)
	case c.WebSearchMaxUses < 1:
		return errors.New("config: pesquisa_web_max_usos precisa ser 1 ou mais")
	case c.Embedding.Model == "":
		return errors.New("config: embedding.modelo não pode ficar vazio")
	case c.Embedding.EntryThreshold <= 0 || c.Embedding.EntryThreshold > 1 || c.Embedding.WalkThreshold <= 0 || c.Embedding.WalkThreshold > 1:
		return errors.New("config: embedding.limiar_entrada e embedding.limiar_caminhada precisam ficar entre 0 e 1")
	}
	return nil
}

// WriteDefault creates config.json with the defaults when it does not exist.
func (h Home) WriteDefault() (created bool, err error) {
	if _, err := os.Stat(h.ConfigFile()); err == nil {
		return false, nil
	}
	data, err := json.MarshalIndent(Default(), "", "  ")
	if err != nil {
		return false, fmt.Errorf("montando o config padrão: %w", err)
	}
	if err := os.WriteFile(h.ConfigFile(), append(data, '\n'), 0o644); err != nil {
		return false, fmt.Errorf("gravando %s: %w", h.ConfigFile(), err)
	}
	return true, nil
}
