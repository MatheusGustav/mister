package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

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
			"e adianta o download do servidor de embedding da memória (llama.cpp e o modelo\n" +
			"EmbeddingGemma 300M, cerca de 300 MB) e do ripgrep que a ferramenta de busca usa.\n" +
			"Sem este comando, cada um é baixado na primeira vez que for usado, assim como o\n" +
			"navegador dos prints.",
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

			logf := func(format string, args ...any) { fmt.Fprintf(out, format+"\n", args...) }
			local := embed.NewLocal(home.EmbeddingDir())
			local.Sources = deps.EmbeddingSources
			if local.Installed() {
				fmt.Fprintf(out, "servidor de embedding da memória já instalado em %s\n", home.EmbeddingDir())
			} else {
				if err := local.Install(cmd.Context(), logf); err != nil {
					return fmt.Errorf("o binário e a configuração foram instalados, mas o servidor de embedding da memória não: %w", err)
				}
				fmt.Fprintf(out, "servidor de embedding da memória instalado em %s\n", home.EmbeddingDir())
			}

			if _, err := os.Stat(home.RipgrepFile()); err == nil {
				fmt.Fprintf(out, "ripgrep da busca já instalado em %s\n", home.RipgrepFile())
			} else {
				logf("baixando o ripgrep da busca")
				if err := installRipgrep(cmd.Context(), home.RipgrepFile(), deps.RipgrepSource, logf); err != nil {
					return fmt.Errorf("o binário, a configuração e o servidor de embedding foram instalados, mas o ripgrep da busca não: %w", err)
				}
				fmt.Fprintf(out, "ripgrep da busca instalado em %s\n", home.RipgrepFile())
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

// RipgrepSource pins the ripgrep the Mister downloads: exact file, by checksum.
type RipgrepSource struct {
	URL    string
	SHA256 string
	// Binary is the path of the rg executable inside the archive.
	Binary string
}

// DefaultRipgrepSource is ripgrep 15.1.0 for Linux x86_64, statically linked.
func DefaultRipgrepSource() RipgrepSource {
	return RipgrepSource{
		URL:    "https://github.com/BurntSushi/ripgrep/releases/download/15.1.0/ripgrep-15.1.0-x86_64-unknown-linux-musl.tar.gz",
		SHA256: "1c9297be4a084eea7ecaedf93eb03d058d6faae29bbc57ecdaf5063921491599",
		Binary: "ripgrep-15.1.0-x86_64-unknown-linux-musl/rg",
	}
}

// ripgrepRetry spaces the download attempts when one fails: the search has
// other ways to run meanwhile.
const ripgrepRetry = 10 * time.Minute

// ripgrepOnDemand returns what the search tool calls before each search: it
// downloads the ripgrep to target the first time, when 'mister install' did
// not bring it before.
func ripgrepOnDemand(target string, src RipgrepSource, logf func(format string, args ...any)) func(context.Context) {
	installer := &embed.OnDemand{RetryAfter: ripgrepRetry}
	installed := func() bool {
		info, err := os.Stat(target)
		return err == nil && !info.IsDir()
	}
	return func(ctx context.Context) {
		_ = installer.Ensure(ctx, installed, func(ctx context.Context) error {
			logf("o ripgrep ainda não está no disco: baixando agora, só desta vez")
			err := installRipgrep(ctx, target, src, logf)
			switch {
			case err == nil:
				logf("ripgrep instalado em %s", target)
			case ctx.Err() == nil:
				logf("não consegui baixar o ripgrep: %v. A busca segue com o ripgrep do sistema, ou sem ele; tento de novo depois de %s", err, ripgrepRetry)
			}
			return err
		})
	}
}

// installRipgrep downloads the archive, checks it against the pinned checksum
// and leaves only the rg executable at target.
func installRipgrep(ctx context.Context, target string, src RipgrepSource, logf func(format string, args ...any)) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("o ripgrep só é baixado para linux x86_64 (aqui é %s/%s)", runtime.GOOS, runtime.GOARCH)
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("criando %s: %w", dir, err)
	}
	tmp, err := os.MkdirTemp(dir, ".rg-*")
	if err != nil {
		return fmt.Errorf("criando a pasta do download em %s: %w", dir, err)
	}
	defer os.RemoveAll(tmp)
	archive := filepath.Join(tmp, "rg.tar.gz")
	if err := embed.Download(ctx, src.URL, src.SHA256, archive, logf); err != nil {
		return err
	}
	unpacked := filepath.Join(tmp, "conteudo")
	if err := embed.ExtractTarGz(archive, unpacked); err != nil {
		return fmt.Errorf("extraindo o ripgrep: %w", err)
	}
	if err := os.Rename(filepath.Join(unpacked, src.Binary), target); err != nil {
		return fmt.Errorf("o arquivo baixado não trouxe o rg em %s: %w", src.Binary, err)
	}
	return nil
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
