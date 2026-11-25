package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MatheusGustav/mister/internal/config"
	"github.com/MatheusGustav/mister/internal/embed"
)

func newInstallCommand(deps Deps) *cobra.Command {
	var dest string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Instala este binário e cria a pasta do Mister",
		Long: "Copia o binário em execução para a pasta de destino (padrão: ~/.local/bin),\n" +
			"cria a pasta do Mister (~/.mister) com a configuração padrão, se ainda não existir,\n" +
			"e baixa o servidor de embedding da memória (llama.cpp e o modelo EmbeddingGemma 300M,\n" +
			"cerca de 300 MB).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if dest == "" {
				user, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("localizando a pasta do usuário: %w", err)
				}
				dest = filepath.Join(user, ".local", "bin")
			}
			target, copied, err := installBinary(dest)
			if err != nil {
				return err
			}
			if copied {
				fmt.Fprintf(out, "binário instalado em %s\n", target)
			} else {
				fmt.Fprintf(out, "o binário já está em %s\n", target)
			}

			home, err := config.ResolveHome()
			if err != nil {
				return err
			}
			if err := home.Ensure(); err != nil {
				return err
			}
			created, err := home.WriteDefault()
			if err != nil {
				return err
			}
			if created {
				fmt.Fprintf(out, "configuração criada em %s\n", home.ConfigFile())
			} else {
				fmt.Fprintf(out, "configuração mantida em %s\n", home.ConfigFile())
			}
			fmt.Fprintf(out, "agentes próprios vão em %s (um arquivo .md por agente)\n", home.RolesDir())

			local := embed.NewLocal(home.EmbeddingDir())
			local.Sources = deps.EmbeddingSources
			if local.Installed() {
				fmt.Fprintf(out, "servidor de embedding da memória já instalado em %s\n", home.EmbeddingDir())
			} else {
				logf := func(format string, args ...any) { fmt.Fprintf(out, format+"\n", args...) }
				if err := local.Install(cmd.Context(), logf); err != nil {
					return fmt.Errorf("o binário e a configuração foram instalados, mas o servidor de embedding da memória não: %w", err)
				}
				fmt.Fprintf(out, "servidor de embedding da memória instalado em %s\n", home.EmbeddingDir())
			}

			if !inPath(dest) {
				fmt.Fprintf(out, "atenção: %s não está no PATH; adicione para chamar 'mister' de qualquer pasta\n", dest)
			}
			if os.Getenv("ANTHROPIC_API_KEY") == "" {
				fmt.Fprintln(out, "atenção: a variável ANTHROPIC_API_KEY não está definida; o Mister precisa dela para conversar")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dest, "dest", "", "pasta onde o binário é instalado (padrão: ~/.local/bin)")
	return cmd
}

// installBinary copies the running executable into dir, replacing an older
// copy atomically. It reports false when the executable already lives there.
func installBinary(dir string) (target string, copied bool, err error) {
	self, err := os.Executable()
	if err != nil {
		return "", false, fmt.Errorf("localizando o binário em execução: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	target = filepath.Join(dir, "mister")
	if resolved, err := filepath.EvalSymlinks(target); err == nil && resolved == self {
		return target, false, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, fmt.Errorf("criando %s: %w", dir, err)
	}

	src, err := os.Open(self)
	if err != nil {
		return "", false, fmt.Errorf("abrindo o binário: %w", err)
	}
	defer src.Close()
	tmp, err := os.CreateTemp(dir, ".mister-*")
	if err != nil {
		return "", false, fmt.Errorf("criando o arquivo em %s: %w", dir, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return "", false, fmt.Errorf("copiando o binário: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return "", false, fmt.Errorf("marcando o binário como executável: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", false, fmt.Errorf("gravando o binário: %w", err)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return "", false, fmt.Errorf("instalando em %s: %w", target, err)
	}
	return target, true, nil
}

func inPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.TrimRight(p, "/") == strings.TrimRight(dir, "/") {
			return true
		}
	}
	return false
}
