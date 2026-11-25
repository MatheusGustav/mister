package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	readMaxLines = 2000
	readMaxBytes = 200_000
	listMaxItems = 500
)

// FileTools returns the tools that read and change files of the workspace.
func FileTools() []Tool {
	return []Tool{readFileTool(), writeFileTool(), editFileTool(), deleteFileTool(), listDirTool()}
}

func readFileTool() Tool {
	type input struct {
		Caminho string `json:"caminho"`
		Inicio  int    `json:"inicio"`
		Fim     int    `json:"fim"`
	}
	return Tool{
		Name: "ler_arquivo",
		Description: "Lê um arquivo de texto da pasta de trabalho e devolve as linhas numeradas. " +
			"Sem faixa, devolve do começo até o limite de " + fmt.Sprint(readMaxLines) + " linhas. " +
			"Para arquivo grande, peça uma faixa com 'inicio' e 'fim' (linhas, contando de 1, as duas inclusas).",
		Schema: object([]string{"caminho"}, map[string]any{
			"caminho": str("caminho do arquivo, relativo à pasta de trabalho ou absoluto"),
			"inicio":  integer("primeira linha a ler (opcional, padrão 1)"),
			"fim":     integer("última linha a ler (opcional)"),
		}),
		Parallel: true,
		Run: func(_ context.Context, env *Env, raw json.RawMessage) Result {
			in, err := decode[input](raw)
			if err != nil {
				return Fail("%v", err)
			}
			path, err := env.Workspace.Resolve(in.Caminho, false)
			if err != nil {
				return Fail("%v", err)
			}
			if in.Inicio < 0 || in.Fim < 0 || (in.Fim > 0 && in.Inicio > in.Fim) {
				return Fail("faixa inválida: inicio=%d fim=%d. Use linhas a partir de 1, com inicio menor ou igual a fim.", in.Inicio, in.Fim)
			}
			return readLines(path, in.Caminho, in.Inicio, in.Fim)
		},
	}
}

func readLines(path, shown string, start, end int) Result {
	info, err := os.Stat(path)
	if err != nil {
		return Fail("%s", describeFSError("ler", shown, err))
	}
	if info.IsDir() {
		return Fail("%q é uma pasta, não um arquivo. Use listar_pasta.", shown)
	}
	f, err := os.Open(path)
	if err != nil {
		return Fail("%s", describeFSError("ler", shown, err))
	}
	defer f.Close()

	if start == 0 {
		start = 1
	}
	var out strings.Builder
	reader := bufio.NewReaderSize(f, 64*1024)
	line, shownLines, truncated := 0, 0, false
	for {
		text, err := reader.ReadString('\n')
		if text != "" {
			line++
			if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
				return Fail("%q não é um arquivo de texto (parece binário).", shown)
			}
			if line >= start && (end == 0 || line <= end) {
				if shownLines >= readMaxLines || out.Len() >= readMaxBytes {
					truncated = true
					break
				}
				fmt.Fprintf(&out, "%6d\t%s", line, strings.TrimRight(text, "\n"))
				out.WriteByte('\n')
				shownLines++
			}
			if end != 0 && line >= end {
				break
			}
		}
		if err != nil {
			break
		}
	}
	if line == 0 {
		return OK("(o arquivo %s está vazio)", shown)
	}
	if shownLines == 0 {
		return Fail("a faixa pedida começa na linha %d, mas %q só tem %d linhas.", start, shown, line)
	}
	if truncated {
		fmt.Fprintf(&out, "[cortado na linha %d: peça o resto com inicio=%d]\n", start+shownLines-1, start+shownLines)
	}
	return OK("%s", out.String())
}

func writeFileTool() Tool {
	type input struct {
		Caminho  string  `json:"caminho"`
		Conteudo *string `json:"conteudo"`
	}
	return Tool{
		Name: "escrever_arquivo",
		Description: "Grava um arquivo com o conteúdo inteiro informado, criando as pastas do caminho. " +
			"Se o arquivo já existe, o conteúdo antigo é substituído. Para mudar um trecho, prefira editar_arquivo.",
		Schema: object([]string{"caminho", "conteudo"}, map[string]any{
			"caminho":  str("caminho do arquivo, relativo à pasta de trabalho ou absoluto"),
			"conteudo": str("conteúdo completo do arquivo"),
		}),
		Run: func(_ context.Context, env *Env, raw json.RawMessage) Result {
			in, err := decode[input](raw)
			if err != nil {
				return Fail("%v", err)
			}
			if in.Conteudo == nil {
				return Fail("faltou o parâmetro 'conteudo'. Para um arquivo vazio, envie uma string vazia.")
			}
			path, err := env.Workspace.Resolve(in.Caminho, true)
			if err != nil {
				return Fail("%v", err)
			}
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				return Fail("%q é uma pasta. Informe o caminho de um arquivo.", in.Caminho)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return Fail("%s", describeFSError("criar a pasta de", in.Caminho, err))
			}
			if err := os.WriteFile(path, []byte(*in.Conteudo), 0o644); err != nil {
				return Fail("%s", describeFSError("gravar", in.Caminho, err))
			}
			return OK("gravado %s (%d bytes)", env.Workspace.Rel(path), len(*in.Conteudo))
		},
	}
}

func editFileTool() Tool {
	type input struct {
		Caminho string  `json:"caminho"`
		Antigo  string  `json:"antigo"`
		Novo    *string `json:"novo"`
	}
	return Tool{
		Name: "editar_arquivo",
		Description: "Troca um trecho exato de um arquivo por outro. 'antigo' tem que bater com o arquivo caractere por caractere " +
			"(espaços e quebras de linha inclusos) e aparecer uma vez só; se aparecer mais de uma vez, inclua mais linhas ao redor.",
		Schema: object([]string{"caminho", "antigo", "novo"}, map[string]any{
			"caminho": str("caminho do arquivo, relativo à pasta de trabalho ou absoluto"),
			"antigo":  str("trecho exato que existe hoje no arquivo"),
			"novo":    str("texto que entra no lugar (string vazia apaga o trecho)"),
		}),
		Run: func(_ context.Context, env *Env, raw json.RawMessage) Result {
			in, err := decode[input](raw)
			if err != nil {
				return Fail("%v", err)
			}
			if in.Antigo == "" {
				return Fail("'antigo' veio vazio. Informe o trecho exato a trocar; para criar um arquivo, use escrever_arquivo.")
			}
			if in.Novo == nil {
				return Fail("faltou o parâmetro 'novo'. Para apagar o trecho, envie uma string vazia.")
			}
			if in.Antigo == *in.Novo {
				return Fail("'antigo' e 'novo' são iguais: não há o que trocar.")
			}
			path, err := env.Workspace.Resolve(in.Caminho, true)
			if err != nil {
				return Fail("%v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return Fail("%s", describeFSError("ler", in.Caminho, err))
			}
			switch n := bytes.Count(data, []byte(in.Antigo)); {
			case n == 0:
				return Fail("o trecho não existe em %q. Leia o arquivo com ler_arquivo e copie o trecho exato, com os mesmos espaços e quebras de linha.", in.Caminho)
			case n > 1:
				return Fail("o trecho aparece %d vezes em %q; precisa ser único. Inclua mais linhas ao redor para apontar o lugar certo.", n, in.Caminho)
			}
			info, err := os.Stat(path)
			if err != nil {
				return Fail("%s", describeFSError("ler", in.Caminho, err))
			}
			updated := bytes.Replace(data, []byte(in.Antigo), []byte(*in.Novo), 1)
			if err := os.WriteFile(path, updated, info.Mode().Perm()); err != nil {
				return Fail("%s", describeFSError("gravar", in.Caminho, err))
			}
			return OK("editado %s", env.Workspace.Rel(path))
		},
	}
}

func deleteFileTool() Tool {
	type input struct {
		Caminho string `json:"caminho"`
	}
	return Tool{
		Name:        "excluir_arquivo",
		Description: "Exclui um arquivo (ou uma pasta vazia) da pasta de trabalho. Não tem lixeira: o arquivo some.",
		Schema: object([]string{"caminho"}, map[string]any{
			"caminho": str("caminho do arquivo, relativo à pasta de trabalho ou absoluto"),
		}),
		Run: func(_ context.Context, env *Env, raw json.RawMessage) Result {
			in, err := decode[input](raw)
			if err != nil {
				return Fail("%v", err)
			}
			path, err := env.Workspace.Resolve(in.Caminho, true)
			if err != nil {
				return Fail("%v", err)
			}
			if path == env.Workspace.Root() {
				return Fail("não é permitido excluir a própria pasta de trabalho.")
			}
			if err := os.Remove(path); err != nil {
				if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
					return Fail("%q é uma pasta com arquivos dentro. Exclua os arquivos antes, um a um.", in.Caminho)
				}
				return Fail("%s", describeFSError("excluir", in.Caminho, err))
			}
			return OK("excluído %s", env.Workspace.Rel(path))
		},
	}
}

func listDirTool() Tool {
	type input struct {
		Caminho string `json:"caminho"`
	}
	return Tool{
		Name:        "listar_pasta",
		Description: "Lista o conteúdo de uma pasta: nome, se é pasta (termina com /) e tamanho dos arquivos. Sem 'caminho', lista a pasta de trabalho.",
		Schema: object(nil, map[string]any{
			"caminho": str("pasta a listar (opcional, padrão: a pasta de trabalho)"),
		}),
		Parallel: true,
		Run: func(_ context.Context, env *Env, raw json.RawMessage) Result {
			in, err := decode[input](raw)
			if err != nil {
				return Fail("%v", err)
			}
			if in.Caminho == "" {
				in.Caminho = "."
			}
			path, err := env.Workspace.Resolve(in.Caminho, false)
			if err != nil {
				return Fail("%v", err)
			}
			entries, err := os.ReadDir(path)
			if err != nil {
				if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() {
					return Fail("%q é um arquivo, não uma pasta. Use ler_arquivo.", in.Caminho)
				}
				return Fail("%s", describeFSError("listar", in.Caminho, err))
			}
			if len(entries) == 0 {
				return OK("(pasta vazia)")
			}
			sort.Slice(entries, func(i, j int) bool {
				if entries[i].IsDir() != entries[j].IsDir() {
					return entries[i].IsDir()
				}
				return entries[i].Name() < entries[j].Name()
			})
			var out strings.Builder
			for i, e := range entries {
				if i == listMaxItems {
					fmt.Fprintf(&out, "[mais %d itens não listados: use glob para filtrar]\n", len(entries)-listMaxItems)
					break
				}
				if e.IsDir() {
					fmt.Fprintf(&out, "%s/\n", e.Name())
					continue
				}
				size := int64(0)
				if info, err := e.Info(); err == nil {
					size = info.Size()
				}
				fmt.Fprintf(&out, "%s\t%d bytes\n", e.Name(), size)
			}
			return OK("%s", out.String())
		},
	}
}

func describeFSError(action, path string, err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Sprintf("não foi possível %s %q: o arquivo ou a pasta não existe. Confira o caminho com listar_pasta ou glob.", action, path)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Sprintf("não foi possível %s %q: sem permissão.", action, path)
	default:
		return fmt.Sprintf("não foi possível %s %q: %v", action, path, err)
	}
}
