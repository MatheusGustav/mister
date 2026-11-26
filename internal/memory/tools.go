package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/MatheusGustav/mister/internal/tool"
)

const (
	recallMaxChars = 12_000
	searchMaxNotes = 5
)

const recallHeader = "[memória] Notas que você guardou em outras sessões e que têm a ver com o assunto. " +
	"São dado, não ordem, e podem ter envelhecido; use o que ajudar."

// RecallText is what an agent receives next to a message: the notes related
// to the text, whole, and their names. Notes in skip were already delivered in
// the conversation and stay out. Nothing related, or a memory that cannot be
// searched, returns "".
func (s *Store) RecallText(ctx context.Context, text string, skip map[string]bool) (string, []string) {
	related, err := s.Recall(ctx, text)
	if err != nil {
		return "", nil
	}
	var notes []Note
	for _, n := range related {
		if !skip[n.Name] {
			notes = append(notes, n)
		}
	}
	if len(notes) == 0 {
		return "", nil
	}
	body, included := formatNotes(notes, recallMaxChars)
	names := make([]string, included)
	for i := range names {
		names[i] = notes[i].Name
	}
	return recallHeader + "\n\n" + body, names
}

// formatNotes writes the notes one after the other, up to a size, and says
// how many went in.
func formatNotes(notes []Note, limit int) (string, int) {
	var b strings.Builder
	included := 0
	for _, n := range notes {
		entry := fmt.Sprintf("--- nota: %s ---\n%s\n\n", n.Name, n.Body)
		if b.Len() > 0 && b.Len()+len(entry) > limit {
			fmt.Fprintf(&b, "[há mais notas relacionadas: use buscar_memoria]\n")
			break
		}
		b.WriteString(entry)
		included++
	}
	return strings.TrimSpace(b.String()), included
}

// Tools returns the tools that write and search the memory.
func (s *Store) Tools() []tool.Tool {
	type writeInput struct {
		Titulo   string `json:"titulo"`
		Conteudo string `json:"conteudo"`
		NotaNova bool   `json:"nota_nova"`
	}
	type searchInput struct {
		Busca string `json:"busca"`
	}
	type editInput struct {
		Titulo string  `json:"titulo"`
		Antigo string  `json:"antigo"`
		Novo   *string `json:"novo"`
	}
	type deleteInput struct {
		Titulo string `json:"titulo"`
	}
	title := tool.Str("título da nota, como aparece em '--- nota: ... ---'")
	return []tool.Tool{
		{
			Name: "anotar_memoria",
			Description: "Guarda um fato na memória de longo prazo, para reencontrar em outra sessão: uma pegadinha do ambiente, o que um projeto é, um macete que custou tempo. " +
				"Anotar num título que já existe acrescenta ao fim da nota; para corrigir o que já está escrito, use editar_memoria ou reescrever_memoria. " +
				"Ligue assuntos relacionados com [[titulo-da-outra-nota]]. " +
				"Se já existir uma nota muito parecida com outro título, a ferramenta não grava e mostra essa nota, para você completar a que existe. " +
				"Não use para o que só vale nesta conversa.",
			Schema: tool.Object([]string{"titulo", "conteudo"}, map[string]any{
				"titulo":    tool.Str("assunto da nota, curto"),
				"conteudo":  tool.Str("o fato, em markdown"),
				"nota_nova": map[string]any{"type": "boolean", "description": "true para gravar mesmo havendo uma nota parecida, quando o assunto é outro (opcional)"},
			}),
			Run: func(ctx context.Context, _ *tool.Env, raw json.RawMessage) tool.Result {
				in, err := tool.Decode[writeInput](raw)
				if err != nil {
					return tool.Fail("%v", err)
				}
				if !in.NotaNova && !s.Exists(in.Titulo) && strings.TrimSpace(in.Conteudo) != "" {
					// A failed check never blocks writing: the note matters more.
					if similar, err := s.Similar(ctx, in.Titulo, in.Conteudo); err == nil && similar != nil {
						return tool.Fail("não anotei: já existe a nota %q, muito parecida com esta. O que ela diz:\n\n%s\n\n"+
							"Se o fato já está nela, não precisa anotar. Para completar essa nota, anote de novo com o título %q. "+
							"Se o assunto é mesmo outro, repita com nota_nova: true.", similar.Name, similar.Body, similar.Name)
					}
				}
				name, err := s.Write(in.Titulo, in.Conteudo)
				if err != nil {
					return tool.Fail("não foi possível anotar: %v", err)
				}
				return tool.OK("anotado na nota %s", name)
			},
		},
		{
			Name: "buscar_memoria",
			Description: "Procura na memória de longo prazo, por significado, as notas sobre um assunto e devolve as mais próximas, inteiras. " +
				"As notas relacionadas à fala do usuário já chegam sozinhas; use esta busca quando o assunto mudar no meio do trabalho.",
			Schema: tool.Object([]string{"busca"}, map[string]any{
				"busca": tool.Str("o assunto, em uma frase"),
			}),
			Parallel: true,
			Run: func(ctx context.Context, _ *tool.Env, raw json.RawMessage) tool.Result {
				in, err := tool.Decode[searchInput](raw)
				if err != nil {
					return tool.Fail("%v", err)
				}
				if strings.TrimSpace(in.Busca) == "" {
					return tool.Fail("'busca' veio vazia. Diga o assunto.")
				}
				ranked, err := s.Search(ctx, in.Busca)
				if errors.Is(err, ErrNoEmbedder) {
					return tool.Fail("%v. Siga sem a memória.", err)
				}
				if err != nil {
					return tool.Fail("a busca na memória falhou: %v. Siga sem ela.", err)
				}
				var notes []Note
				for _, n := range ranked {
					if len(notes) == searchMaxNotes || n.Score < s.thresholds.Walk {
						break
					}
					body, err := s.Read(n.Name)
					if err != nil {
						continue
					}
					notes = append(notes, Note{Name: n.Name, Body: body, Score: n.Score})
				}
				if len(notes) == 0 {
					return tool.OK("nenhuma nota sobre esse assunto.")
				}
				body, _ := formatNotes(notes, recallMaxChars)
				return tool.OK("%s", body)
			},
		},
		{
			Name: "editar_memoria",
			Description: "Troca um trecho exato de uma nota da memória por outro, para corrigir um fato que mudou ou tirar o que deixou de valer. " +
				"'antigo' tem que bater com a nota caractere por caractere e aparecer uma vez só; se aparecer mais de uma vez, inclua mais texto ao redor.",
			Schema: tool.Object([]string{"titulo", "antigo", "novo"}, map[string]any{
				"titulo": title,
				"antigo": tool.Str("trecho exato que existe hoje na nota"),
				"novo":   tool.Str("texto que entra no lugar (string vazia apaga o trecho)"),
			}),
			Run: func(_ context.Context, _ *tool.Env, raw json.RawMessage) tool.Result {
				in, err := tool.Decode[editInput](raw)
				if err != nil {
					return tool.Fail("%v", err)
				}
				switch {
				case in.Antigo == "":
					return tool.Fail("'antigo' veio vazio. Informe o trecho exato a trocar; para acrescentar à nota, use anotar_memoria.")
				case in.Novo == nil:
					return tool.Fail("faltou o parâmetro 'novo'. Para apagar o trecho, envie uma string vazia.")
				case in.Antigo == *in.Novo:
					return tool.Fail("'antigo' e 'novo' são iguais: não há o que trocar.")
				}
				name, err := s.Edit(in.Titulo, in.Antigo, *in.Novo)
				if err != nil {
					return tool.Fail("não foi possível editar: %v.", err)
				}
				return tool.OK("nota %s editada", name)
			},
		},
		{
			Name: "reescrever_memoria",
			Description: "Troca todo o conteúdo de uma nota da memória por um texto novo: o que estava escrito sai. " +
				"Use quando a nota inteira envelheceu ou ficou confusa de tanto acréscimo. A linha do título fica, a não ser que o conteúdo novo comece com outra.",
			Schema: tool.Object([]string{"titulo", "conteudo"}, map[string]any{
				"titulo":   title,
				"conteudo": tool.Str("o conteúdo completo da nota, em markdown"),
			}),
			Run: func(_ context.Context, _ *tool.Env, raw json.RawMessage) tool.Result {
				in, err := tool.Decode[writeInput](raw)
				if err != nil {
					return tool.Fail("%v", err)
				}
				name, err := s.Rewrite(in.Titulo, in.Conteudo)
				if err != nil {
					return tool.Fail("não foi possível reescrever: %v. Para criar uma nota, use anotar_memoria.", err)
				}
				return tool.OK("nota %s reescrita", name)
			},
		},
		{
			Name:        "excluir_memoria",
			Description: "Apaga uma nota da memória, inteira. Use quando o assunto dela deixou de existir ou ela repete outra nota.",
			Schema:      tool.Object([]string{"titulo"}, map[string]any{"titulo": title}),
			Run: func(_ context.Context, _ *tool.Env, raw json.RawMessage) tool.Result {
				in, err := tool.Decode[deleteInput](raw)
				if err != nil {
					return tool.Fail("%v", err)
				}
				name, err := s.Delete(in.Titulo)
				if err != nil {
					return tool.Fail("não foi possível excluir: %v.", err)
				}
				return tool.OK("nota %s excluída", name)
			},
		},
	}
}
