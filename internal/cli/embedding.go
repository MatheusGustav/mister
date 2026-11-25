package cli

import (
	"os"

	"github.com/MatheusGustav/mister/internal/config"
	"github.com/MatheusGustav/mister/internal/embed"
	"github.com/MatheusGustav/mister/internal/memory"
)

// newEmbedder builds the embedding provider the config asks for: a server
// given by URL, or the local one managed by the Mister. The returned local
// server is nil when a URL is configured.
func newEmbedder(home config.Home, cfg config.Config, sources embed.Sources) (memory.Embedder, *embed.Local) {
	model := embed.Model{Name: cfg.Embedding.Model, QueryPrefix: cfg.Embedding.QueryPrefix, DocPrefix: cfg.Embedding.NotePrefix}
	if cfg.Embedding.URL != "" {
		key := ""
		if cfg.Embedding.KeyEnv != "" {
			key = os.Getenv(cfg.Embedding.KeyEnv)
		}
		return embed.NewHTTP(cfg.Embedding.URL, key, model), nil
	}
	local := embed.NewLocal(home.EmbeddingDir(), model)
	local.Sources = sources
	return local, local
}
