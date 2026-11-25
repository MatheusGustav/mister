package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHome_Load(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		file    string
		wantErr string
		check   func(t *testing.T, cfg Config)
	}{
		{
			name: "sem arquivo vale o padrão",
			check: func(t *testing.T, cfg Config) {
				if cfg.MaxThreads != 3 || cfg.HeavyLimit != 1 || cfg.WebSearch != "auto" || cfg.Model("opus") != "claude-opus-5-5" {
					t.Errorf("padrão inesperado: %+v", cfg)
				}
				if cfg.BrowserTabs != 3 || cfg.BrowserIdleMinutes != 5 || cfg.BrowserLoadSeconds != 30 {
					t.Errorf("padrão do navegador inesperado: %+v", cfg)
				}
			},
		},
		{
			name: "arquivo troca só o que traz",
			file: `{"max_threads": 6, "modelos": {"sonnet": "outro-id"}}`,
			check: func(t *testing.T, cfg Config) {
				if cfg.MaxThreads != 6 || cfg.HeavyLimit != 1 {
					t.Errorf("max_threads=%d limite_pesado=%d", cfg.MaxThreads, cfg.HeavyLimit)
				}
				if cfg.BrowserTabs != 3 || cfg.BrowserIdleMinutes != 5 || cfg.BrowserLoadSeconds != 30 {
					t.Errorf("arquivo sem os campos do navegador deveria ficar com o padrão: %+v", cfg)
				}
				if cfg.Model("sonnet") != "outro-id" || cfg.Model("fable") != "claude-fable-5-2" {
					t.Errorf("modelos: %v", cfg.Models)
				}
				if cfg.Model("claude-x") != "claude-x" {
					t.Error("nome desconhecido deveria valer como id")
				}
			},
		},
		{name: "modelos nulo", file: `{"modelos": null}`, check: func(t *testing.T, cfg Config) {
			if cfg.Model("opus") != "claude-opus-5-5" {
				t.Errorf("modelos: %v", cfg.Models)
			}
		}},
		{name: "json inválido", file: `{`, wantErr: "JSON válido"},
		{name: "limite zero", file: `{"limite_pesado": 0}`, wantErr: "limite_pesado"},
		{name: "pesquisa inválida", file: `{"pesquisa_web": "google"}`, wantErr: "pesquisa_web"},
		{name: "navegador ajustado", file: `{"navegador_abas": 1, "navegador_ocioso_minutos": 2, "navegador_carregamento_segundos": 60}`, check: func(t *testing.T, cfg Config) {
			if cfg.BrowserTabs != 1 || cfg.BrowserIdleMinutes != 2 || cfg.BrowserLoadSeconds != 60 {
				t.Errorf("navegador: %+v", cfg)
			}
		}},
		{name: "nenhuma aba", file: `{"navegador_abas": 0}`, wantErr: "navegador_abas"},
		{name: "ocioso zero", file: `{"navegador_ocioso_minutos": 0}`, wantErr: "navegador_ocioso_minutos"},
		{name: "carregamento negativo", file: `{"navegador_carregamento_segundos": -5}`, wantErr: "navegador_carregamento_segundos"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := Home{Dir: t.TempDir()}
			if tt.file != "" {
				if err := os.WriteFile(home.ConfigFile(), []byte(tt.file), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := home.Load()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("erro = %v, quero conter %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, cfg)
		})
	}
}

func TestHome_EnsureAndWriteDefault(t *testing.T) {
	t.Parallel()
	home := Home{Dir: filepath.Join(t.TempDir(), "mister")}
	if err := home.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{home.RolesDir(), home.SessionsDir(), home.MemoryDir()} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("faltou criar %s: %v", dir, err)
		}
	}
	created, err := home.WriteDefault()
	if err != nil || !created {
		t.Fatalf("WriteDefault = %v, %v", created, err)
	}
	if _, err := home.Load(); err != nil {
		t.Errorf("o config padrão gravado deveria carregar: %v", err)
	}
	if err := os.WriteFile(home.ConfigFile(), []byte(`{"max_threads": 9}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if created, err := home.WriteDefault(); err != nil || created {
		t.Errorf("não deveria sobrescrever um config existente: %v, %v", created, err)
	}
	if cfg, _ := home.Load(); cfg.MaxThreads != 9 {
		t.Errorf("o config do usuário foi alterado: %d", cfg.MaxThreads)
	}
}

func TestResolveHome(t *testing.T) {
	t.Setenv(HomeEnv, "/tmp/outro-lugar")
	home, err := ResolveHome()
	if err != nil || home.Dir != "/tmp/outro-lugar" {
		t.Errorf("com %s definido: %q, %v", HomeEnv, home.Dir, err)
	}
	t.Setenv(HomeEnv, "")
	home, err = ResolveHome()
	if err != nil || filepath.Base(home.Dir) != ".mister" {
		t.Errorf("padrão deveria ser ~/.mister: %q, %v", home.Dir, err)
	}
}
