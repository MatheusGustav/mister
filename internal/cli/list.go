package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/MatheusGustav/mister/internal/agent"
	"github.com/MatheusGustav/mister/internal/config"
	"github.com/MatheusGustav/mister/internal/role"
)

func newRolesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: "Lista os agentes disponíveis nesta pasta",
		Long: "Os agentes nativos (host e thread) podem ser trocados e novos agentes criados com um arquivo .md\n" +
			"em ~/.mister/agents ou em .mister/agents no projeto. O arquivo abre com um cabeçalho:\n\n" +
			"  ---\n  name: documentador\n  description: só escreve documentação\n  model: sonnet\n  effort: medium\n" +
			"  tools: ler_arquivo, escrever_arquivo, editar_arquivo, listar_pasta, buscar, glob\n  ---\n  <instruções do agente>",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := config.ResolveHome()
			if err != nil {
				return err
			}
			project, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("descobrindo a pasta atual: %w", err)
			}
			roles, err := role.Load(home.RolesDir(), filepath.Join(project, ".mister", "agents"))
			if err != nil {
				return err
			}
			names := make([]string, 0, len(roles))
			for name := range roles {
				names = append(names, name)
			}
			sort.Strings(names)

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "AGENTE\tMODELO\tESFORÇO\tORIGEM\tFERRAMENTAS")
			for _, name := range names {
				r := roles[name]
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Name, dash(r.Model), dash(r.Effort), r.Source, strings.Join(r.Tools, ", "))
			}
			return w.Flush()
		},
	}
}

func newSessionsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "Lista as sessões guardadas",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := config.ResolveHome()
			if err != nil {
				return err
			}
			sessions, err := agent.ListSessions(home.SessionsDir())
			if err != nil {
				return err
			}
			if len(sessions) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "nenhuma sessão guardada")
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SESSÃO\tATUALIZADA\tTHREADS\tPASTA")
			for _, s := range sessions {
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", s.ID, s.Updated.Format("02/01/2006 15:04"), len(s.Threads), s.Project)
			}
			return w.Flush()
		},
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
