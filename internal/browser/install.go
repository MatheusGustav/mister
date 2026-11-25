package browser

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/MatheusGustav/mister/internal/embed"
)

// Source pins the browser the Mister downloads: exact file, by checksum.
type Source struct {
	Version string
	URL     string
	SHA256  string
	// Binary is the path of the executable inside the archive.
	Binary string
}

// DefaultSource is chrome-headless-shell 158.0.8127.88 for Linux x86_64, from
// Chrome for Testing. Google publishes no checksum for it: this one was taken
// from the file on 25/11/2026.
func DefaultSource() Source {
	return Source{
		Version: "158.0.8127.88",
		URL:     "https://storage.googleapis.com/chrome-for-testing-public/158.0.8127.88/linux64/chrome-headless-shell-linux64.zip",
		SHA256:  "63401333cc587645bfaeea7f0c90958b327d249251e23954a47f30112ca23d5b",
		Binary:  "chrome-headless-shell-linux64/chrome-headless-shell",
	}
}

func (m *Manager) execPath() string {
	if m.Exec != "" {
		return m.Exec
	}
	return filepath.Join(m.Dir, m.Source.Version, m.Source.Binary)
}

// Installed reports whether the browser is on disk.
func (m *Manager) Installed() bool {
	info, err := os.Stat(m.execPath())
	return err == nil && !info.IsDir()
}

// ensureInstalled downloads the browser when it is not on disk yet. Callers
// that arrive during a download wait for it instead of starting another.
func (m *Manager) ensureInstalled(ctx context.Context) error {
	if m.Installed() {
		return nil
	}
	select {
	case m.installing <- struct{}{}:
		defer func() { <-m.installing }()
	case <-ctx.Done():
		return fmt.Errorf("esperando o download do navegador: %w", ctx.Err())
	}
	if m.Installed() {
		return nil
	}
	return m.install(ctx)
}

// install downloads and unpacks the browser beside its final folder and only
// then moves it there, so a download cut in the middle leaves nothing that
// looks installed.
func (m *Manager) install(ctx context.Context) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("o navegador só é baixado para linux x86_64 (aqui é %s/%s)", runtime.GOOS, runtime.GOARCH)
	}
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return fmt.Errorf("criando %s: %w", m.Dir, err)
	}
	tmp, err := os.MkdirTemp(m.Dir, ".download-*")
	if err != nil {
		return fmt.Errorf("criando a pasta do download em %s: %w", m.Dir, err)
	}
	defer os.RemoveAll(tmp)

	m.logf("baixando o navegador dos prints (chrome-headless-shell %s), só desta vez", m.Source.Version)
	archive := filepath.Join(tmp, "navegador.zip")
	if err := embed.Download(ctx, m.Source.URL, m.Source.SHA256, archive, m.logf); err != nil {
		return err
	}
	unpacked := filepath.Join(tmp, "conteudo")
	if err := extractZip(archive, unpacked); err != nil {
		return fmt.Errorf("extraindo o navegador: %w", err)
	}
	if info, err := os.Stat(filepath.Join(unpacked, m.Source.Binary)); err != nil || info.IsDir() {
		return fmt.Errorf("o arquivo baixado não trouxe o navegador em %s", m.Source.Binary)
	}
	target := filepath.Join(m.Dir, m.Source.Version)
	// Another Mister may have finished the same install in the meantime.
	if err := os.Rename(unpacked, target); err != nil && !m.Installed() {
		return fmt.Errorf("instalando o navegador em %s: %w", target, err)
	}
	m.logf("navegador instalado em %s", target)
	return nil
}

// extractZip unpacks an archive into dir, refusing entries that would land
// outside it.
func extractZip(archive, dir string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer reader.Close()

	for _, entry := range reader.File {
		target := filepath.Join(dir, entry.Name)
		if !strings.HasPrefix(target, filepath.Clean(dir)+string(os.PathSeparator)) {
			return fmt.Errorf("entrada %q sai da pasta de destino", entry.Name)
		}
		mode := entry.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case mode.IsRegular():
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := extractZipFile(entry, target, mode.Perm()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("entrada %q não é arquivo nem pasta", entry.Name)
		}
	}
	return nil
}

func extractZipFile(entry *zip.File, target string, perm os.FileMode) error {
	in, err := entry.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
