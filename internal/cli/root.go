// Package cli is the command tree of the mister binary.
package cli

import (
	"github.com/spf13/cobra"

	"github.com/MatheusGustav/mister/internal/embed"
	"github.com/MatheusGustav/mister/internal/provider"
	"github.com/MatheusGustav/mister/internal/provider/anthropic"
)

// Version is set at build time with -ldflags "-X .../internal/cli.Version=...".
var Version = "dev"

// Deps are the pieces a test replaces.
type Deps struct {
	NewProvider func() (provider.Provider, error)
	// EmbeddingSources is where 'mister install' downloads the local
	// embedding server and its model from.
	EmbeddingSources embed.Sources
}

func DefaultDeps() Deps {
	return Deps{
		EmbeddingSources: embed.DefaultSources(),
		NewProvider: func() (provider.Provider, error) {
			client, err := anthropic.New()
			if err != nil {
				return nil, err
			}
			return client, nil
		},
	}
}

// NewRootCommand builds a fresh command tree.
func NewRootCommand(deps Deps) *cobra.Command {
	var opts chatOptions
	root := &cobra.Command{
		Use:   "mister",
		Short: "Agente de IA de terminal que orquestra threads em paralelo",
		Long: "Sem subcomando, abre a conversa com o host na pasta atual.\n" +
			"A chave da API vem da variável de ambiente ANTHROPIC_API_KEY.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runChat(cmd, deps, opts)
		},
	}
	root.Flags().BoolVar(&opts.resume, "continue", false, "retoma a última sessão desta pasta")
	root.Flags().StringVar(&opts.session, "session", "", "retoma a sessão com este id (veja 'mister sessions')")
	root.Flags().StringVar(&opts.model, "model", "", "modelo do host (sonnet, opus, fable ou um id completo)")
	root.Flags().StringVar(&opts.role, "agent", "", "agente principal (padrão: host)")
	root.MarkFlagsMutuallyExclusive("continue", "session")

	root.AddCommand(newInstallCommand(deps), newRolesCommand(), newSessionsCommand(), newVersionCommand())
	return root
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Mostra a versão",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := cmd.OutOrStdout().Write([]byte("mister " + Version + "\n"))
			return err
		},
	}
}
