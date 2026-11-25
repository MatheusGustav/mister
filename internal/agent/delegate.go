package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/MatheusGustav/mister/internal/role"
	"github.com/MatheusGustav/mister/internal/tool"
)

const (
	waitDefault = 10 * time.Minute
	waitMax     = 30 * time.Minute
)

// delegationTools are the tools only the host of the session can use.
func (rt *Runtime) delegationTools() []tool.Tool {
	return []tool.Tool{rt.delegateTool(), rt.waitTool(), rt.continueTool(), rt.listTool()}
}

// onlyHost refuses the delegation tools to any agent that is not the host,
// whatever its role file says.
func onlyHost(env *tool.Env) (tool.Result, bool) {
	if env.AgentID != HostID {
		return tool.Fail("só o host da sessão delega e acompanha threads; um thread não cria outro."), false
	}
	return tool.Result{}, true
}

func (rt *Runtime) delegateTool() tool.Tool {
	type input struct {
		Nome    string `json:"nome"`
		Tarefa  string `json:"tarefa"`
		Modelo  string `json:"modelo"`
		Esforco string `json:"esforco"`
		Papel   string `json:"papel"`
	}
	return tool.Tool{
		Name: "delegar",
		Description: "Cria um thread: um agente novo, numa git worktree própria (pasta irmã do projeto, branch wt/<nome>, saindo do commit atual), " +
			"com uma tarefa bem definida. Devolve na hora; o thread trabalha sozinho e o relatório chega por aguardar_threads. " +
			"A 'tarefa' é o único contexto que o thread recebe: escreva o pedido, o que já foi conferido no código e as decisões já tomadas.",
		Schema: tool.Object([]string{"nome", "tarefa"}, map[string]any{
			"nome":    tool.Str("nome curto do pedido, em minúsculas com hífens (vira o nome da worktree e da branch)"),
			"tarefa":  tool.Str("o recado completo para o thread"),
			"modelo":  tool.Str("modelo do thread: sonnet, opus ou fable (opcional, padrão: o do papel)"),
			"esforco": tool.Str("esforço do thread: low, medium, high, xhigh ou max (opcional, padrão: o do papel)"),
			"papel":   tool.Str("papel do agente (opcional, padrão: thread)"),
		}),
		Run: func(ctx context.Context, env *tool.Env, raw json.RawMessage) tool.Result {
			if res, ok := onlyHost(env); !ok {
				return res
			}
			in, err := tool.Decode[input](raw)
			if err != nil {
				return tool.Fail("%v", err)
			}
			if !threadNameRe.MatchString(in.Nome) || in.Nome == HostID {
				return tool.Fail("nome %q inválido: use minúsculas, números e hífens (ex.: ajuste-login), e não use %q.", in.Nome, HostID)
			}
			if strings.TrimSpace(in.Tarefa) == "" {
				return tool.Fail("'tarefa' veio vazia. O thread só sabe o que estiver escrito nela.")
			}
			if in.Papel == "" {
				in.Papel = role.Thread
			}
			r, ok := rt.opts.Roles[in.Papel]
			if !ok {
				return tool.Fail("o papel %q não existe. Papéis: %s", in.Papel, strings.Join(roleNames(rt.opts.Roles), ", "))
			}
			if !role.ValidEffort(in.Esforco) {
				return tool.Fail("esforço %q inválido. Use low, medium, high, xhigh ou max.", in.Esforco)
			}
			if in.Modelo == "" {
				in.Modelo = r.Model
			}
			if in.Esforco == "" {
				in.Esforco = r.Effort
			}

			rt.mu.Lock()
			_, exists := rt.threads[in.Nome]
			running := rt.runningLocked()
			rt.mu.Unlock()
			if exists {
				return tool.Fail("já existe um thread %q nesta sessão. Para mandar uma correção a ele, use continuar_thread; para outro assunto, escolha outro nome.", in.Nome)
			}
			if running >= rt.opts.Config.MaxThreads {
				return tool.Fail("já há %d threads rodando, que é o limite desta máquina. Use aguardar_threads e delegue de novo quando um terminar.", running)
			}

			path := worktreePath(rt.opts.Project, in.Nome)
			branch := "wt/" + in.Nome
			if _, err := os.Stat(path); err == nil {
				return tool.Fail("a pasta %s já existe. Escolha outro nome ou remova a worktree antiga (git worktree remove).", path)
			}
			if _, err := git(ctx, rt.opts.Project, "rev-parse", "--verify", "HEAD"); err != nil {
				return tool.Fail("delegar precisa que o projeto seja um repositório git com pelo menos um commit, para criar a worktree: %v", err)
			}
			if _, err := git(ctx, rt.opts.Project, "worktree", "add", "-q", "-b", branch, path, "HEAD"); err != nil {
				return tool.Fail("não foi possível criar a worktree: %v", err)
			}

			meta := AgentMeta{ID: in.Nome, Role: r.Name, Model: rt.opts.Config.Model(in.Modelo), Effort: in.Esforco,
				Root: path, Branch: branch, Task: strings.TrimSpace(in.Tarefa), Status: StatusRunning}
			preamble := threadPreamble(meta, rt.opts.Project, rt.opts.Now())
			a, err := rt.newAgent(meta, r, preamble, nil)
			if err != nil {
				return tool.Fail("a worktree foi criada em %s, mas o thread não subiu: %v", path, err)
			}
			t := &thread{agent: a, meta: meta}
			rt.mu.Lock()
			rt.threads[in.Nome] = t
			rt.order = append(rt.order, in.Nome)
			rt.mu.Unlock()
			// The host reviews the diff of the worktree, so it may read it.
			env.Workspace.AllowRead(path)

			rt.start(ctx, t, preamble)
			if err := rt.saveMeta(); err != nil {
				return tool.Fail("o thread %s está rodando, mas a sessão não foi gravada: %v", in.Nome, err)
			}
			return tool.OK("thread %s lançado (papel %s, modelo %s, esforço %s) em %s, branch %s. Use aguardar_threads para receber o relatório.",
				in.Nome, r.Name, meta.Model, orDash(meta.Effort), path, branch)
		},
	}
}

func (rt *Runtime) waitTool() tool.Tool {
	type input struct {
		Timeout int `json:"timeout_segundos"`
	}
	return tool.Tool{
		Name: "aguardar_threads",
		Description: "Espera até algum thread terminar e devolve o relatório de cada thread terminado que ainda não foi lido. " +
			"Se o tempo acabar antes, diz quem ainda está rodando: chame de novo para continuar esperando.",
		Schema: tool.Object(nil, map[string]any{
			"timeout_segundos": tool.Int(fmt.Sprintf("quanto esperar, em segundos (opcional, padrão %d, máximo %d)",
				int(waitDefault.Seconds()), int(waitMax.Seconds()))),
		}),
		Run: func(ctx context.Context, env *tool.Env, raw json.RawMessage) tool.Result {
			if res, ok := onlyHost(env); !ok {
				return res
			}
			in, err := tool.Decode[input](raw)
			if err != nil {
				return tool.Fail("%v", err)
			}
			if in.Timeout < 0 {
				return tool.Fail("'timeout_segundos' não pode ser negativo.")
			}
			wait := waitDefault
			if in.Timeout > 0 {
				wait = min(time.Duration(in.Timeout)*time.Second, waitMax)
			}
			timer := time.NewTimer(wait)
			defer timer.Stop()

			for {
				reports, running := rt.collect()
				if len(reports) > 0 {
					if err := rt.saveMeta(); err != nil {
						return tool.Fail("%s\n[a sessão não foi gravada: %v]", strings.Join(reports, "\n\n"), err)
					}
					out := strings.Join(reports, "\n\n")
					if len(running) > 0 {
						out += "\n\nAinda rodando: " + strings.Join(running, ", ")
					}
					return tool.OK("%s", out)
				}
				if len(running) == 0 {
					return tool.OK("nenhum thread rodando e nenhum relatório por ler.")
				}
				select {
				case <-rt.events:
				case <-timer.C:
					return tool.OK("passaram %s e nenhum thread terminou. Ainda rodando: %s. Chame aguardar_threads de novo para continuar esperando.",
						wait, strings.Join(running, ", "))
				case <-ctx.Done():
					return tool.Fail("a espera foi cancelada. Ainda rodando: %s.", strings.Join(running, ", "))
				}
			}
		},
	}
}

// collect hands over the reports not yet delivered and the names of the
// threads still running.
func (rt *Runtime) collect() (reports, running []string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, id := range rt.order {
		t := rt.threads[id]
		switch {
		case t.meta.Status == StatusRunning:
			running = append(running, id)
		case !t.meta.Reported:
			t.meta.Reported = true
			reports = append(reports, fmt.Sprintf("=== thread %s (%s) — pasta %s, branch %s ===\n%s",
				id, t.meta.Status, t.meta.Root, t.meta.Branch, t.meta.Report))
		}
	}
	return reports, running
}

func (rt *Runtime) continueTool() tool.Tool {
	type input struct {
		Nome     string `json:"nome"`
		Mensagem string `json:"mensagem"`
	}
	return tool.Tool{
		Name: "continuar_thread",
		Description: "Manda uma mensagem a um thread que já terminou a rodada (uma correção da revisão, uma decisão que faltava). " +
			"Ele continua na mesma worktree, com a conversa que já tinha. Devolve na hora; o relatório novo chega por aguardar_threads.",
		Schema: tool.Object([]string{"nome", "mensagem"}, map[string]any{
			"nome":     tool.Str("nome do thread"),
			"mensagem": tool.Str("o que ele precisa fazer ou saber agora"),
		}),
		Run: func(ctx context.Context, env *tool.Env, raw json.RawMessage) tool.Result {
			if res, ok := onlyHost(env); !ok {
				return res
			}
			in, err := tool.Decode[input](raw)
			if err != nil {
				return tool.Fail("%v", err)
			}
			if strings.TrimSpace(in.Mensagem) == "" {
				return tool.Fail("'mensagem' veio vazia.")
			}
			rt.mu.Lock()
			t, ok := rt.threads[in.Nome]
			var status string
			if ok {
				status = t.meta.Status
			}
			running := rt.runningLocked()
			names := append([]string(nil), rt.order...)
			rt.mu.Unlock()
			switch {
			case !ok:
				return tool.Fail("não existe thread %q nesta sessão. Threads: %s", in.Nome, strings.Join(names, ", "))
			case status == StatusRunning:
				return tool.Fail("o thread %s ainda está rodando. Espere ele terminar com aguardar_threads.", in.Nome)
			case t.agent == nil:
				return tool.Fail("a worktree do thread %s não existe mais; ele não pode continuar. Delegue de novo com outro nome.", in.Nome)
			case running >= rt.opts.Config.MaxThreads:
				return tool.Fail("já há %d threads rodando, que é o limite desta máquina. Espere um terminar.", running)
			}
			rt.start(ctx, t, in.Mensagem)
			if err := rt.saveMeta(); err != nil {
				return tool.Fail("o thread %s voltou a rodar, mas a sessão não foi gravada: %v", in.Nome, err)
			}
			return tool.OK("mensagem entregue; o thread %s voltou a rodar. Use aguardar_threads para receber o relatório.", in.Nome)
		},
	}
}

func (rt *Runtime) listTool() tool.Tool {
	type input struct{}
	return tool.Tool{
		Name:        "listar_threads",
		Description: "Mostra o host e cada thread: estado, modelo, pasta, memória usada por agente, tokens, e a fila dos comandos pesados.",
		Schema:      tool.Object(nil, map[string]any{}),
		Parallel:    true,
		Run: func(_ context.Context, env *tool.Env, raw json.RawMessage) tool.Result {
			if res, ok := onlyHost(env); !ok {
				return res
			}
			if _, err := tool.Decode[input](raw); err != nil {
				return tool.Fail("%v", err)
			}
			return tool.OK("%s", rt.Report())
		},
	}
}

func roleNames(roles map[string]role.Role) []string {
	names := make([]string, 0, len(roles))
	for name := range roles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
