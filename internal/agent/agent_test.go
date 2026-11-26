package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MatheusGustav/mister/internal/provider"
	"github.com/MatheusGustav/mister/internal/tool"
)

func TestAgent_Run_AnswersWithoutTools(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Settings{System: "instruções"}, func(int, provider.Request) (*provider.Response, error) {
		return say("pronto"), nil
	})
	got, err := h.agent.Run(context.Background(), "oi")
	if err != nil {
		t.Fatal(err)
	}
	if got != "pronto" {
		t.Errorf("resposta = %q, quero pronto", got)
	}
	req := h.provider.request(0)
	if req.System != "instruções" || len(req.Messages) != 1 || req.Messages[0].Text() != "oi" {
		t.Errorf("pedido inesperado: system=%q mensagens=%+v", req.System, req.Messages)
	}
	if len(h.journal.appends) != 2 {
		t.Errorf("o diário recebeu %d mensagens, quero 2 (a do usuário e a resposta)", len(h.journal.appends))
	}
}

func TestAgent_Run_ExecutesToolAndFeedsResultBack(t *testing.T) {
	t.Parallel()
	echo := simpleTool("eco", false, func(context.Context) tool.Result { return tool.OK("resultado do eco") })
	h := newHarness(t, Settings{}, func(n int, req provider.Request) (*provider.Response, error) {
		if n == 0 {
			return use(toolUse{id: "t1", name: "eco", input: map[string]any{}}), nil
		}
		return say("vi: " + lastMessage(req)), nil
	}, echo)

	got, err := h.agent.Run(context.Background(), "use a ferramenta")
	if err != nil {
		t.Fatal(err)
	}
	if got != "vi: resultado do eco" {
		t.Errorf("resposta = %q", got)
	}
	second := h.provider.request(1)
	if len(second.Messages) != 3 {
		t.Fatalf("o segundo pedido tem %d mensagens, quero 3", len(second.Messages))
	}
	result := second.Messages[2].Blocks[0]
	if result.Type != provider.BlockToolResult || result.ToolUseID != "t1" || result.IsError {
		t.Errorf("resultado da ferramenta inesperado: %+v", result)
	}
	if stats := h.agent.Stats(); stats.ToolCalls != 1 || stats.Requests != 2 || stats.Messages != 4 {
		t.Errorf("medidas inesperadas: %+v", stats)
	}
}

func TestAgent_Run_ToolImagesGoBackToTheModel(t *testing.T) {
	t.Parallel()
	picture := []byte(strings.Repeat("p", 2048))
	shot := simpleTool("print", false, func(context.Context) tool.Result {
		res := tool.OK("um print")
		res.Images = []provider.Image{{Label: "desktop", MediaType: "image/png", Data: picture}}
		return res
	})
	h := newHarness(t, Settings{}, func(n int, _ provider.Request) (*provider.Response, error) {
		if n == 0 {
			return use(toolUse{id: "t1", name: "print", input: map[string]any{}}), nil
		}
		return say("vi"), nil
	}, shot)

	if _, err := h.agent.Run(context.Background(), "tire um print"); err != nil {
		t.Fatal(err)
	}
	result := h.provider.request(1).Messages[2].Blocks[0]
	if result.Text != "um print" || len(result.Images) != 1 || result.Images[0].Label != "desktop" ||
		result.Images[0].MediaType != "image/png" || string(result.Images[0].Data) != string(picture) {
		t.Errorf("a imagem não chegou ao modelo junto do resultado: %+v", result)
	}
	if saved := h.journal.appends[2].Blocks[0]; len(saved.Images) != 1 {
		t.Errorf("a imagem não foi para o histórico gravado: %+v", saved)
	}
	if stats := h.agent.Stats(); stats.HistoryBytes < len(picture) {
		t.Errorf("o histórico mede %d bytes, mas guarda uma imagem de %d", stats.HistoryBytes, len(picture))
	}
}

func TestAgent_Run_ToolErrorsGoBackToTheModel(t *testing.T) {
	t.Parallel()
	failing := simpleTool("falha", false, func(context.Context) tool.Result { return tool.Fail("deu errado, tente X") })
	h := newHarness(t, Settings{}, func(n int, req provider.Request) (*provider.Response, error) {
		if n == 0 {
			return use(toolUse{id: "a", name: "falha", input: map[string]any{}}, toolUse{id: "b", name: "fantasma", input: map[string]any{}}), nil
		}
		return say("ok"), nil
	}, failing)

	if _, err := h.agent.Run(context.Background(), "vai"); err != nil {
		t.Fatal(err)
	}
	blocks := h.provider.request(1).Messages[2].Blocks
	if len(blocks) != 2 {
		t.Fatalf("quero os dois resultados na mesma mensagem, veio %d", len(blocks))
	}
	if !blocks[0].IsError || blocks[0].Text != "deu errado, tente X" {
		t.Errorf("erro da ferramenta não chegou inteiro: %+v", blocks[0])
	}
	if !blocks[1].IsError || !strings.Contains(blocks[1].Text, "não existe") {
		t.Errorf("ferramenta desconhecida deveria virar erro claro: %+v", blocks[1])
	}
}

func TestAgent_Run_ReadOnlyToolsRunInParallel(t *testing.T) {
	t.Parallel()
	var inFlight, peak, serialSawOthers atomic.Int32
	bothStarted := make(chan struct{})
	reader := func(context.Context) tool.Result {
		if now := inFlight.Add(1); now == 2 {
			peak.Store(2)
			close(bothStarted)
		}
		select {
		case <-bothStarted:
		case <-time.After(5 * time.Second):
		}
		inFlight.Add(-1)
		return tool.OK("lido")
	}
	writer := func(context.Context) tool.Result {
		serialSawOthers.Store(inFlight.Load())
		return tool.OK("gravado")
	}
	h := newHarness(t, Settings{}, func(n int, _ provider.Request) (*provider.Response, error) {
		if n == 0 {
			return use(
				toolUse{id: "1", name: "ler_a", input: map[string]any{}},
				toolUse{id: "2", name: "ler_b", input: map[string]any{}},
				toolUse{id: "3", name: "gravar", input: map[string]any{}},
			), nil
		}
		return say("fim"), nil
	}, simpleTool("ler_a", true, reader), simpleTool("ler_b", true, reader), simpleTool("gravar", false, writer))

	if _, err := h.agent.Run(context.Background(), "vai"); err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 2 {
		t.Error("as duas leituras não rodaram ao mesmo tempo")
	}
	if serialSawOthers.Load() != 0 {
		t.Error("a ferramenta que altera rodou junto com as leituras")
	}
	blocks := h.provider.request(1).Messages[2].Blocks
	var order []string
	for _, b := range blocks {
		order = append(order, b.ToolUseID+"="+b.Text)
	}
	if got := strings.Join(order, " "); got != "1=lido 2=lido 3=gravado" {
		t.Errorf("resultados fora da ordem das chamadas: %s", got)
	}
}

func TestAgent_Run_StopReasons(t *testing.T) {
	t.Parallel()
	var ran atomic.Int32
	counter := simpleTool("conta", false, func(context.Context) tool.Result {
		ran.Add(1)
		return tool.OK("rodou")
	})
	cut := func(resp *provider.Response) *provider.Response {
		resp.Stop = provider.StopMaxTokens
		return resp
	}

	t.Run("chamada cortada pelo limite de tokens não roda", func(t *testing.T) {
		h := newHarness(t, Settings{}, func(n int, _ provider.Request) (*provider.Response, error) {
			if n == 0 {
				return cut(use(toolUse{id: "x", name: "conta", input: map[string]any{}})), nil
			}
			return say("refeito"), nil
		}, counter)
		if _, err := h.agent.Run(context.Background(), "vai"); err != nil {
			t.Fatal(err)
		}
		if ran.Load() != 0 {
			t.Error("a ferramenta rodou com a entrada cortada")
		}
		block := h.provider.request(1).Messages[2].Blocks[0]
		if !block.IsError || !strings.Contains(block.Text, "limite de tokens") {
			t.Errorf("faltou o erro explicando o corte: %+v", block)
		}
	})

	t.Run("texto cortado pede para continuar", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, Settings{}, func(n int, _ provider.Request) (*provider.Response, error) {
			if n == 0 {
				return cut(say("primeira metade")), nil
			}
			return say("segunda metade"), nil
		})
		got, err := h.agent.Run(context.Background(), "escreva")
		if err != nil {
			t.Fatal(err)
		}
		if got != "segunda metade" {
			t.Errorf("resposta = %q", got)
		}
		if !strings.Contains(lastMessage(h.provider.request(1)), "Continue exatamente de onde parou") {
			t.Error("faltou o pedido para continuar")
		}
	})

	t.Run("texto cortado sem fim devolve o que tem", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, Settings{}, func(int, provider.Request) (*provider.Response, error) {
			return cut(say("pedaço")), nil
		})
		got, err := h.agent.Run(context.Background(), "escreva")
		if err != nil || got != "pedaço" {
			t.Errorf("resposta = %q, erro = %v", got, err)
		}
		if n := h.provider.count(); n != maxCutContinues+1 {
			t.Errorf("fez %d pedidos, quero %d", n, maxCutContinues+1)
		}
	})

	t.Run("recusa vira erro e não entra no histórico", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, Settings{}, func(int, provider.Request) (*provider.Response, error) {
			return &provider.Response{Stop: provider.StopRefusal, StopDetail: "cyber"}, nil
		})
		_, err := h.agent.Run(context.Background(), "algo")
		var refusal *RefusalError
		if !errors.As(err, &refusal) || refusal.Detail != "cyber" {
			t.Fatalf("erro = %v, quero RefusalError com o motivo", err)
		}
		if got := h.agent.Stats().Messages; got != 1 {
			t.Errorf("o histórico ficou com %d mensagens, quero só a do usuário", got)
		}
	})

	t.Run("pausa do provedor reenvia a conversa", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, Settings{}, func(n int, _ provider.Request) (*provider.Response, error) {
			if n == 0 {
				resp := say("pesquisando")
				resp.Stop = provider.StopPause
				return resp, nil
			}
			return say("achei"), nil
		})
		got, err := h.agent.Run(context.Background(), "pesquise")
		if err != nil || got != "achei" {
			t.Fatalf("resposta = %q, erro = %v", got, err)
		}
		second := h.provider.request(1).Messages
		if len(second) != 2 || second[1].Role != provider.RoleAssistant {
			t.Errorf("a retomada deveria mandar a conversa sem mensagem nova: %+v", second)
		}
	})

	t.Run("pausa sem fim vira erro", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, Settings{}, func(int, provider.Request) (*provider.Response, error) {
			return &provider.Response{Stop: provider.StopPause}, nil
		})
		if _, err := h.agent.Run(context.Background(), "pesquise"); err == nil || !strings.Contains(err.Error(), "pausou") {
			t.Errorf("erro = %v", err)
		}
	})
}

func TestAgent_Run_ShowsTheProvidersWebSearch(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Settings{}, func(_ int, req provider.Request) (*provider.Response, error) {
		req.OnWebSearch(json.RawMessage(`{"query":"go 1.27"}`))
		return say("achei"), nil
	})
	var calls []string
	h.agent.hooks.OnToolCall = func(id string, call tool.Call) {
		calls = append(calls, id+" "+call.Name+" "+string(call.Input))
	}
	if _, err := h.agent.Run(context.Background(), "pesquise"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != `teste pesquisar_web {"query":"go 1.27"}` {
		t.Errorf("chamadas mostradas = %q", calls)
	}
}

func TestAgent_Run_StopsAtMaxIterations(t *testing.T) {
	t.Parallel()
	echo := simpleTool("eco", false, func(context.Context) tool.Result { return tool.OK("de novo") })
	h := newHarness(t, Settings{MaxIterations: 3}, func(n int, _ provider.Request) (*provider.Response, error) {
		return use(toolUse{id: "x", name: "eco", input: map[string]any{}}), nil
	}, echo)
	_, err := h.agent.Run(context.Background(), "vai")
	if !errors.Is(err, ErrMaxIterations) {
		t.Fatalf("erro = %v, quero ErrMaxIterations", err)
	}
	if n := h.provider.count(); n != 3 {
		t.Errorf("fez %d pedidos, quero 3", n)
	}
}

func TestAgent_Run_ProviderErrorIsWrapped(t *testing.T) {
	t.Parallel()
	boom := errors.New("rede caiu")
	h := newHarness(t, Settings{ID: "meu-agente"}, func(int, provider.Request) (*provider.Response, error) {
		return nil, boom
	})
	_, err := h.agent.Run(context.Background(), "oi")
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "meu-agente") {
		t.Errorf("erro = %v, quero o erro original com o nome do agente", err)
	}
}

func TestAgent_Run_CancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	blocker := simpleTool("espera", false, func(ctx context.Context) tool.Result {
		cancel()
		<-ctx.Done()
		return tool.Fail("cancelado")
	})
	h := newHarness(t, Settings{}, func(n int, _ provider.Request) (*provider.Response, error) {
		return use(toolUse{id: "x", name: "espera", input: map[string]any{}}), nil
	}, blocker)

	_, err := h.agent.Run(ctx, "vai")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("erro = %v, quero context.Canceled", err)
	}
	// The call got its result before the run stopped: the history stays valid.
	last := h.journal.appends[len(h.journal.appends)-1]
	if last.Role != provider.RoleUser || last.Blocks[0].Type != provider.BlockToolResult {
		t.Errorf("a última mensagem gravada deveria ser o resultado da ferramenta: %+v", last)
	}
	if note := last.Blocks[len(last.Blocks)-1]; note.Type != provider.BlockText || note.Text != interruptedTools {
		t.Errorf("o agente deveria ficar sabendo da interrupção: %+v", note)
	}
}

func TestAgent_Run_CancelledWhileAnswering(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	h := newHarness(t, Settings{}, func(n int, _ provider.Request) (*provider.Response, error) {
		if n == 0 {
			cancel()
			return nil, ctx.Err()
		}
		return say("sigo daqui"), nil
	})

	if _, err := h.agent.Run(ctx, "explique"); !errors.Is(err, context.Canceled) {
		t.Fatalf("erro = %v, quero context.Canceled", err)
	}
	if _, err := h.agent.Run(context.Background(), "outra coisa"); err != nil {
		t.Fatal(err)
	}
	sent := h.provider.request(1).Messages
	if len(sent) != 3 || sent[0].Text() != "explique" || sent[1].Text() != interruptedAnswer || sent[2].Text() != "outra coisa" {
		t.Errorf("a conversa seguinte deveria levar o aviso da interrupção entre as duas mensagens: %+v", sent)
	}
}

func TestAgent_Run_KeepsThePrefixFixed(t *testing.T) {
	t.Parallel()
	echo := simpleTool("eco", false, func(context.Context) tool.Result { return tool.OK("ok") })
	other := simpleTool("alfa", true, func(context.Context) tool.Result { return tool.OK("ok") })
	h := newHarness(t, Settings{System: "fixo", Model: "m", Effort: "high"}, func(n int, _ provider.Request) (*provider.Response, error) {
		if n%2 == 0 {
			return use(toolUse{id: "x", name: "eco", input: map[string]any{}}), nil
		}
		return say("ok"), nil
	}, echo, other)
	for _, input := range []string{"um", "dois"} {
		if _, err := h.agent.Run(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	first := h.provider.request(0)
	if first.Tools[0].Name != "alfa" || first.Tools[1].Name != "eco" {
		t.Errorf("as ferramentas deveriam vir ordenadas por nome: %v", first.Tools)
	}
	for i := 1; i < h.provider.count(); i++ {
		req := h.provider.request(i)
		if req.System != first.System || req.Model != first.Model || req.Effort != first.Effort || len(req.Tools) != len(first.Tools) {
			t.Errorf("o pedido %d mudou o começo fixo", i)
		}
		for j := range req.Tools {
			if req.Tools[j].Name != first.Tools[j].Name {
				t.Errorf("o pedido %d mudou a ordem das ferramentas", i)
			}
		}
		// The conversation only grows: every earlier message is still there.
		prev := h.provider.request(i - 1).Messages
		for j := range prev {
			if req.Messages[j].Text() != prev[j].Text() {
				t.Errorf("o pedido %d alterou a mensagem %d do histórico", i, j)
			}
		}
	}
}

func TestAgent_Run_AttachesNoticesAndRecall(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Settings{}, func(int, provider.Request) (*provider.Response, error) { return say("ok"), nil })
	pending := []string{"o thread x terminou"}
	h.agent.notices = func() []string {
		out := pending
		pending = nil
		return out
	}
	h.agent.recall = func(_ context.Context, text string, _ map[string]bool) (string, []string) {
		return "[memória] nota sobre " + text, []string{"nota"}
	}

	if _, err := h.agent.Run(context.Background(), "redmi"); err != nil {
		t.Fatal(err)
	}
	blocks := h.provider.request(0).Messages[0].Blocks
	var texts []string
	for _, b := range blocks {
		texts = append(texts, b.Text)
	}
	want := "redmi|[memória] nota sobre redmi|[aviso] o thread x terminou"
	if got := strings.Join(texts, "|"); got != want {
		t.Errorf("blocos da mensagem = %q, quero %q", got, want)
	}
}

func TestAgent_RunWith_SearchesMemoryWithTheInputAlone(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Settings{}, func(int, provider.Request) (*provider.Response, error) { return say("ok"), nil })
	var searched string
	h.agent.recall = func(_ context.Context, text string, _ map[string]bool) (string, []string) {
		searched = text
		return "[memória] nota", []string{"nota"}
	}
	if _, err := h.agent.RunWith(context.Background(), "Projeto: /x\nData: 24/11/2026", "em qual porta roda o banco?"); err != nil {
		t.Fatal(err)
	}
	if searched != "em qual porta roda o banco?" {
		t.Errorf("a memória foi buscada com %q; o cabeçalho não pode entrar na busca", searched)
	}
	msg := h.provider.request(0).Messages[0]
	if got := msg.Text(); got != "Projeto: /x\nData: 24/11/2026\n\nem qual porta roda o banco?[memória] nota" {
		t.Errorf("mensagem enviada = %q", got)
	}
	if len(msg.Blocks) != 3 {
		t.Errorf("quero 3 blocos (cabeçalho, pergunta, memória), veio %d", len(msg.Blocks))
	}
}

// fakeMemory holds notes by keyword: a search brings the notes whose keyword
// is in the text, except the ones already delivered.
type fakeMemory struct {
	notes    map[string]string
	searches []string
}

func (m *fakeMemory) recall(_ context.Context, text string, skip map[string]bool) (string, []string) {
	m.searches = append(m.searches, text)
	var names []string
	for keyword := range m.notes {
		if strings.Contains(text, keyword) && !skip[keyword] {
			names = append(names, keyword)
		}
	}
	if len(names) == 0 {
		return "", nil
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("[memória]")
	for _, name := range names {
		b.WriteString(" " + m.notes[name])
	}
	return b.String(), names
}

func TestAgent_Run_SearchesMemoryAfterEachToolRound(t *testing.T) {
	t.Parallel()
	failing := simpleTool("rodar_comando", false, func(context.Context) tool.Result {
		return tool.Fail("a porta 8080 do docker já está em uso")
	})
	reading := simpleTool("ler_arquivo", true, func(context.Context) tool.Result {
		return tool.OK("conteúdo que fala de docker mas não é problema")
	})
	mem := &fakeMemory{notes: map[string]string{"docker": "nota das portas do docker", "login": "nota do login"}}
	h := newHarness(t, Settings{}, func(n int, _ provider.Request) (*provider.Response, error) {
		switch n {
		case 0:
			return use(toolUse{id: "1", name: "ler_arquivo", input: map[string]any{"caminho": "a.txt"}}), nil
		case 1, 2:
			return use(toolUse{id: "c", name: "rodar_comando", input: map[string]any{"comando": "subir o ambiente"}}), nil
		default:
			return say("fim"), nil
		}
	}, failing, reading)
	h.agent.recall = mem.recall

	if _, err := h.agent.Run(context.Background(), "ajusta o login"); err != nil {
		t.Fatal(err)
	}
	texts := func(i int) string {
		var parts []string
		msgs := h.provider.request(i).Messages
		for _, b := range msgs[len(msgs)-1].Blocks {
			parts = append(parts, b.Text)
		}
		return strings.Join(parts, " | ")
	}
	if got := texts(0); !strings.Contains(got, "nota do login") {
		t.Errorf("a fala deveria trazer a nota do login: %s", got)
	}
	if got := texts(1); strings.Contains(got, "[memória]") {
		t.Errorf("a saída de uma leitura que deu certo não entra na busca: %s", got)
	}
	if got := texts(2); !strings.Contains(got, "a porta 8080 do docker já está em uso | [memória] nota das portas do docker") {
		t.Errorf("o erro do comando deveria trazer a nota do docker, depois do resultado: %s", got)
	}
	if got := texts(3); strings.Contains(got, "[memória]") {
		t.Errorf("a mesma nota não deveria ser entregue duas vezes na conversa: %s", got)
	}
	if want := `ler_arquivo {"caminho":"a.txt"}`; !strings.Contains(mem.searches[1], want) {
		t.Errorf("a busca da rodada deveria levar a chamada %q: %q", want, mem.searches[1])
	}
	if !strings.Contains(mem.searches[2], "rodar_comando") || !strings.Contains(mem.searches[2], "já está em uso") {
		t.Errorf("a busca da rodada deveria levar o comando e o erro: %q", mem.searches[2])
	}
}

func TestAgent_Compaction_AllowsNotesAgain(t *testing.T) {
	t.Parallel()
	mem := &fakeMemory{notes: map[string]string{"login": "nota do login"}}
	h := newHarness(t, Settings{CompactTokens: 500}, func(n int, req provider.Request) (*provider.Response, error) {
		switch {
		case n == 0:
			resp := say("primeira")
			resp.Usage = provider.Usage{InputTokens: 900}
			return resp, nil
		case strings.Contains(lastMessage(req), "vai ser trocado por um resumo"):
			return say("RESUMO"), nil
		default:
			return say("ok"), nil
		}
	})
	h.agent.recall = mem.recall
	for _, input := range []string{"ajusta o login", "o login de novo", "e o login outra vez"} {
		if _, err := h.agent.Run(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	// First message: delivered. Second: compaction drops the history, so the
	// note comes again. Third: already there.
	var delivered []bool
	for i := range h.provider.count() {
		req := h.provider.request(i)
		if strings.Contains(lastMessage(req), "vai ser trocado por um resumo") {
			continue
		}
		last := req.Messages[len(req.Messages)-1]
		delivered = append(delivered, strings.Contains(last.Blocks[len(last.Blocks)-1].Text, "nota do login"))
	}
	if len(delivered) != 3 || !delivered[0] || !delivered[1] || delivered[2] {
		t.Errorf("entregas da nota por mensagem = %v, quero [true true false]", delivered)
	}
}

func TestAgent_Restore_ClosesDanglingToolCalls(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Settings{}, func(int, provider.Request) (*provider.Response, error) { return say("retomei"), nil })
	saved := []provider.Message{
		provider.UserText("faça"),
		use(toolUse{id: "perdida", name: "eco", input: map[string]any{}}).Message,
	}
	if err := h.agent.Restore(saved); err != nil {
		t.Fatal(err)
	}
	if _, err := h.agent.Run(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}
	msgs := h.provider.request(0).Messages
	if len(msgs) != 4 {
		t.Fatalf("quero 4 mensagens (pedido, chamada, resultado de interrupção, continue), veio %d", len(msgs))
	}
	closing := msgs[2].Blocks[0]
	if closing.Type != provider.BlockToolResult || closing.ToolUseID != "perdida" || !closing.IsError {
		t.Errorf("a chamada pendurada não foi fechada: %+v", closing)
	}
}

func TestAgent_Compaction(t *testing.T) {
	t.Parallel()
	echo := simpleTool("eco", false, func(context.Context) tool.Result { return tool.OK("saída grande") })
	big := provider.Usage{InputTokens: 900}
	isSummaryRequest := func(req provider.Request) bool {
		return strings.Contains(lastMessage(req), "vai ser trocado por um resumo")
	}

	t.Run("no meio da tarefa troca o histórico pelo resumo", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, Settings{Preamble: "Pasta: /x\n\ntarefa original", CompactTokens: 500}, func(n int, req provider.Request) (*provider.Response, error) {
			switch {
			case n == 0:
				resp := use(toolUse{id: "1", name: "eco", input: map[string]any{}})
				resp.Usage = big
				return resp, nil
			case isSummaryRequest(req):
				return say("RESUMO: fiz A, falta B"), nil
			default:
				return say("terminei B"), nil
			}
		}, echo)

		got, err := h.agent.Run(context.Background(), "Pasta: /x\n\ntarefa original")
		if err != nil || got != "terminei B" {
			t.Fatalf("resposta = %q, erro = %v", got, err)
		}
		summaryReq := h.provider.request(1)
		if len(summaryReq.Messages) != 4 || summaryReq.System != h.provider.request(0).System {
			t.Errorf("o pedido de resumo deveria reusar a conversa inteira e só acrescentar a instrução: %d mensagens", len(summaryReq.Messages))
		}
		after := h.provider.request(2).Messages
		if len(after) != 1 {
			t.Fatalf("depois de compactar quero 1 mensagem, veio %d", len(after))
		}
		text := after[0].Text()
		for _, want := range []string{"tarefa original", "RESUMO: fiz A, falta B", "Continue o trabalho de onde parou."} {
			if !strings.Contains(text, want) {
				t.Errorf("o histórico compactado não contém %q:\n%s", want, text)
			}
		}
		if len(h.journal.resets) != 1 {
			t.Errorf("o diário deveria registrar 1 troca de histórico, registrou %d", len(h.journal.resets))
		}
		if stats := h.agent.Stats(); stats.Compactions != 1 {
			t.Errorf("Compactions = %d, quero 1", stats.Compactions)
		}
	})

	t.Run("pergunta nova do usuário fica palavra por palavra", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, Settings{CompactTokens: 500}, func(n int, req provider.Request) (*provider.Response, error) {
			switch {
			case n == 0:
				resp := say("primeira resposta")
				resp.Usage = big
				return resp, nil
			case isSummaryRequest(req):
				return say("RESUMO"), nil
			default:
				return say("segunda resposta"), nil
			}
		})
		if _, err := h.agent.Run(context.Background(), "primeira pergunta"); err != nil {
			t.Fatal(err)
		}
		if _, err := h.agent.Run(context.Background(), "qual a cor exata do botão?"); err != nil {
			t.Fatal(err)
		}
		after := h.provider.request(2).Messages
		if len(after) != 2 {
			t.Fatalf("quero o resumo e a pergunta nova depois de compactar, veio %d mensagens", len(after))
		}
		if summary := after[0].Text(); !strings.Contains(summary, "RESUMO") || strings.Contains(summary, "Continue o trabalho") {
			t.Errorf("o resumo de uma conversa parada não manda continuar trabalho nenhum: %q", summary)
		}
		if last := after[1].Text(); last != "qual a cor exata do botão?" {
			t.Errorf("a pergunta nova virou %q", last)
		}
		for _, msg := range h.provider.request(1).Messages {
			if strings.Contains(msg.Text(), "qual a cor exata") {
				t.Error("o resumo deveria ser pedido antes de a pergunta nova entrar no histórico")
			}
		}
	})

	t.Run("resumo vazio mantém o histórico", func(t *testing.T) {
		t.Parallel()
		var notices []string
		h := newHarness(t, Settings{CompactTokens: 500}, func(n int, req provider.Request) (*provider.Response, error) {
			switch {
			case n == 0:
				resp := use(toolUse{id: "1", name: "eco", input: map[string]any{}})
				resp.Usage = big
				return resp, nil
			case isSummaryRequest(req):
				return say("  "), nil
			default:
				return say("segui"), nil
			}
		}, echo)
		h.agent.hooks.OnNotice = func(_, text string) { notices = append(notices, text) }
		if _, err := h.agent.Run(context.Background(), "vai"); err != nil {
			t.Fatal(err)
		}
		if got := len(h.provider.request(2).Messages); got != 3 {
			t.Errorf("sem resumo o histórico deveria seguir inteiro (3 mensagens), veio %d", got)
		}
		if len(notices) != 1 || !strings.Contains(notices[0], "não consegui compactar") {
			t.Errorf("avisos = %v", notices)
		}
	})

	t.Run("abaixo do limite não compacta", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, Settings{CompactTokens: 5000}, func(n int, _ provider.Request) (*provider.Response, error) {
			if n == 0 {
				resp := use(toolUse{id: "1", name: "eco", input: map[string]any{}})
				resp.Usage = big
				return resp, nil
			}
			return say("fim"), nil
		}, echo)
		if _, err := h.agent.Run(context.Background(), "vai"); err != nil {
			t.Fatal(err)
		}
		if h.provider.count() != 2 || len(h.journal.resets) != 0 {
			t.Errorf("não deveria compactar: %d pedidos, %d trocas", h.provider.count(), len(h.journal.resets))
		}
	})
}

func TestAgent_Stats_MeasuresMemory(t *testing.T) {
	t.Parallel()
	payload := strings.Repeat("x", 4096)
	big := simpleTool("grande", false, func(context.Context) tool.Result { return tool.OK("%s", payload) })
	h := newHarness(t, Settings{}, func(n int, _ provider.Request) (*provider.Response, error) {
		if n == 0 {
			resp := use(toolUse{id: "1", name: "grande", input: map[string]any{}})
			resp.Usage = provider.Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 100, CacheWriteTokens: 50}
			return resp, nil
		}
		return say("fim"), nil
	}, big)
	if _, err := h.agent.Run(context.Background(), "vai"); err != nil {
		t.Fatal(err)
	}
	h.agent.recordChildMem(2048)
	h.agent.recordChildMem(512)
	stats := h.agent.Stats()
	if stats.HistoryBytes < len(payload) || stats.PeakHistoryBytes < stats.HistoryBytes {
		t.Errorf("o histórico mede %d bytes (pico %d), mas guarda um resultado de %d", stats.HistoryBytes, stats.PeakHistoryBytes, len(payload))
	}
	if stats.PeakChildKB != 2048 {
		t.Errorf("PeakChildKB = %d, quero o maior (2048)", stats.PeakChildKB)
	}
	if stats.InputTokens != 10 || stats.OutputTokens != 5 || stats.CacheReadTokens != 100 || stats.CacheWriteTokens != 50 {
		t.Errorf("soma de tokens inesperada: %+v", stats)
	}
}
