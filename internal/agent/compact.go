package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MatheusGustav/mister/internal/provider"
)

const compactPrompt = "[sistema] O contexto desta conversa ficou grande e vai ser trocado por um resumo. " +
	"Escreva agora esse resumo, só em texto, sem chamar ferramenta. Ele precisa bastar para continuar o trabalho sem o histórico: " +
	"o pedido com as palavras de quem pediu, as decisões tomadas e a base de cada uma, os arquivos lidos e alterados (com caminho) e o que mudou em cada um, " +
	"os comandos rodados e o resultado, o estado atual, o que falta fazer e qualquer pendência com o usuário."

const compactHeader = "[contexto compactado] A conversa anterior foi trocada pelo resumo abaixo, escrito por você."

func (a *Agent) needsCompaction() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.set.CompactTokens > 0 && len(a.history) > 1 && a.stats.ContextTokens >= int64(a.set.CompactTokens)
}

// compact replaces the history by a summary the model writes. The request
// reuses the whole cached conversation and only adds the instruction at the
// end. The preamble and a pending user message stay word for word. With
// resume, the agent is in the middle of a task and is told to carry on.
func (a *Agent) compact(ctx context.Context, resume bool) error {
	history := a.snapshot()
	req := a.request(append(history, provider.UserText(compactPrompt)))
	req.OnText = nil
	resp, err := a.provider.Complete(ctx, req)
	if err != nil {
		return fmt.Errorf("pedindo o resumo: %w", err)
	}
	a.recordUsage(resp.Usage)
	summary := strings.TrimSpace(resp.Message.Text())
	if summary == "" {
		return errors.New("o modelo não devolveu um resumo")
	}

	var text strings.Builder
	if a.set.Preamble != "" {
		text.WriteString(a.set.Preamble)
		text.WriteString("\n\n")
	}
	text.WriteString(compactHeader)
	text.WriteString("\n\n")
	text.WriteString(summary)

	blocks := []provider.Block{{Type: provider.BlockText, Text: text.String()}}
	if pending := pendingUserText(history); len(pending) > 0 {
		blocks = append(blocks, pending...)
	} else if resume {
		blocks = append(blocks, provider.Block{Type: provider.BlockText, Text: "Continue o trabalho de onde parou."})
	}
	compacted := []provider.Message{{Role: provider.RoleUser, Blocks: blocks}}

	// The notes left with the history: they may be delivered again.
	a.delivered = nil
	a.mu.Lock()
	a.history = compacted
	a.stats.Compactions++
	a.stats.ContextTokens = 0
	a.refreshSizeLocked()
	a.mu.Unlock()

	if a.journal != nil {
		if err := a.journal.Reset(compacted); err != nil {
			return fmt.Errorf("gravando o histórico compactado: %w", err)
		}
	}
	a.notice("contexto compactado")
	return nil
}

// pendingUserText returns the blocks of the last message when it is a user
// message made only of text: a question still waiting for its answer. Tool
// results cannot be kept, because the calls they answer leave with the history.
func pendingUserText(history []provider.Message) []provider.Block {
	if len(history) == 0 {
		return nil
	}
	last := history[len(history)-1]
	if last.Role != provider.RoleUser {
		return nil
	}
	for _, b := range last.Blocks {
		if b.Type != provider.BlockText {
			return nil
		}
	}
	return last.Blocks
}
