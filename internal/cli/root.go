// Package cli is the command tree of the mister binary.
package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/MatheusGustav/mister/internal/browser"
	"github.com/MatheusGustav/mister/internal/embed"
	"github.com/MatheusGustav/mister/internal/provider"
	"github.com/MatheusGustav/mister/internal/provider/anthropic"
)

// Version is set at build time with -ldflags "-X .../internal/cli.Version=...".
var Version = "dev"

// Deps are the pieces a test replaces.
type Deps struct {
	NewProvider func() (provider.Provider, error)
	// EmbeddingSources is where the local embedding server and its model are
	// downloaded from, by 'mister install' or on the first memory search.
	EmbeddingSources embed.Sources
	// RipgrepSource is where the ripgrep of the search tool is downloaded
	// from, by 'mister install' or on the first search.
	RipgrepSource RipgrepSource
	// BrowserSource is where the screenshot tool downloads its browser from,
	// on the first screenshot.
	BrowserSource browser.Source
}

func DefaultDeps() Deps {
	return Deps{
		EmbeddingSources: embed.DefaultSources(),
		RipgrepSource:    DefaultRipgrepSource(),
		BrowserSource:    browser.DefaultSource(),
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
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.resume && opts.session != "" {
				return errors.New("use --continue ou --session, não os dois")
			}
			return runChat(cmd, deps, opts)
		},
	}
	root.SetFlagErrorFunc(flagError)
	root.Flags().BoolVar(&opts.resume, "continue", false, "retoma a última sessão desta pasta")
	root.Flags().StringVar(&opts.session, "session", "", "retoma a sessão com este id (veja 'mister sessions')")
	root.Flags().StringVar(&opts.model, "model", "", "modelo do host (sonnet, opus, fable ou um id completo)")
	root.Flags().StringVar(&opts.role, "agent", "", "agente principal (padrão: host)")

	root.AddCommand(newInstallCommand(deps), newRolesCommand(), newSessionsCommand(), newVersionCommand())
	return root
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Mostra a versão",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := cmd.OutOrStdout().Write([]byte("mister " + Version + "\n"))
			return err
		},
	}
}

// noArgs refuses anything after the command, in Portuguese.
func noArgs(cmd *cobra.Command, args []string) error {
	switch {
	case len(args) == 0:
		return nil
	case cmd.HasSubCommands():
		return fmt.Errorf("comando desconhecido %q; veja '%s --help'", args[0], cmd.CommandPath())
	}
	return fmt.Errorf("'%s' não recebe argumento (veio %q); veja '%s --help'", cmd.CommandPath(), args[0], cmd.CommandPath())
}

// flagError says in Portuguese what is wrong with an option.
func flagError(cmd *cobra.Command, err error) error {
	help := fmt.Sprintf("; veja '%s --help'", cmd.CommandPath())
	var unknown *pflag.NotExistError
	var noValue *pflag.ValueRequiredError
	var badValue *pflag.InvalidValueError
	switch {
	case errors.As(err, &unknown):
		return fmt.Errorf("opção desconhecida: %s%s", specified(unknown.GetSpecifiedName(), unknown.GetSpecifiedShortnames()), help)
	case errors.As(err, &noValue):
		return fmt.Errorf("a opção %s precisa de um valor%s", specified(noValue.GetSpecifiedName(), noValue.GetSpecifiedShortnames()), help)
	case errors.As(err, &badValue):
		return fmt.Errorf("valor %q não serve para a opção --%s%s", badValue.GetValue(), badValue.GetFlag().Name, help)
	}
	return fmt.Errorf("opção inválida: %w%s", err, help)
}

// specified writes an option the way it was typed: by name or by its letter.
func specified(name, shorthands string) string {
	if shorthands != "" {
		return "-" + shorthands
	}
	return "--" + name
}
