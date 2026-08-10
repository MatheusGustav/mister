"""O agente: costura MÚLTIPLOS passos num turno.

O cérebro age um passo por vez: chama uma tool, VÊ o resultado, e decide o
próximo — encadear, perguntar ao dono, ou concluir.

A SEGURANÇA não passa por aqui. Cada passo continua indo pelo `executar` (o
despachante), que valida os params contra o formulário e guarda a confirmação.
O agente só costura; ele nunca decide o que é seguro.

Cabrestos:
  - SEM teto de passos. Um agente que encadeia passos DIFERENTES roda até
    terminar; cancelar na hora é o ESC (a pele transforma em Ctrl+C). O que
    existe é um DETECTOR DE LOOP: se o cérebro repetir a MESMA ação (intenção +
    params) LOOP_JANELA vezes seguidas, é loop de verdade, não tarefa longa —
    aí ele para e EXPLICA pro dono o que tentou e onde travou, em vez de girar
    pra sempre.
  - 'perguntar' e 'responder' são intenções de CONTROLE, tratadas aqui — não são
    tools e não tocam o sistema.

O agente não conhece o correio: notícia de trabalho que roda em segundo plano
chega por `recados` (uma função que quem monta o laço injeta — ver
`__main__.py`). Assim o loop não precisa saber que o ekodide existe.
"""
from __future__ import annotations

import json
import uuid
from contextlib import nullcontext
from typing import Callable, ContextManager

from mister.brain import Cerebro, Decisao, ErroCerebro
from mister.confirmacao import Pendente
from mister.resultado import Resultado

# Marca da notícia que chega de fora do turno (envio que terminou em segundo
# plano), pro cérebro reconhecer o padrão no histórico.
MARCA_RECADO = "[recado do segundo plano]"

# Detector de loop: repetir a MESMA ação (intenção + params) essa quantidade de
# vezes SEGUIDAS é loop de verdade, não tarefa longa.
LOOP_JANELA = 3

# Tipos das funções de IO injetadas (facilita testar sem terminal de verdade).
Perguntar = Callable[[str], str]   # mostra um prompt, devolve a resposta do dono
Mostrar = Callable[[str], None]    # entrega uma mensagem ao dono
Executar = Callable[[Decisao], "Resultado | Pendente"]
# Fábrica de "estou ocupado" (ex.: um spinner). Envolve SÓ o trabalho — nunca o
# momento de perguntar ao dono, pra não competir com o input.
Pensando = Callable[[], ContextManager]
# Fábrica de animação por estado: atividade(tipo, detalhe) -> context manager.
# A pele escolhe o desenho; o agente só diz O QUE está acontecendo.
Atividade = Callable[..., ContextManager]
# Notícias que chegaram de fora do turno (ex.: envio em segundo plano que
# terminou). Nunca bloqueia: só colhe o que já está pronto.
Recados = Callable[[], list[str]]


def _ocupado(atividade: Atividade | None, tipo: str, detalhe: str = "") -> ContextManager:
    """Liga a animação do estado, se a pele oferecer uma. Senão, no-op."""
    return atividade(tipo, detalhe) if atividade else nullcontext()


def _confirmar(
    pendente: Pendente,
    executar: Executar,
    perguntar: Perguntar,
    atividade: Atividade | None = None,
) -> Resultado:
    """Pede o aval do DONO (nunca do modelo) e, se ele topar, repete a ação já
    marcada como confirmada."""
    escolha = perguntar(f"{pendente.pergunta} (s/n) ").strip().lower()
    if escolha in ("s", "sim", "y", "yes"):
        # aval_do_dono é o sinal: só esta linha (depois do "s") cria uma decisão
        # que o despachante aceita com 'confirmado' preenchido. O `carimbo`
        # (anti-drift) vai junto: o despachante recalcula e nega se a ação não
        # for EXATAMENTE a que o dono acabou de ver.
        with _ocupado(atividade, "executando", pendente.intencao):
            return executar(Decisao(
                pendente.intencao, pendente.params,
                aval_do_dono=True, carimbo=pendente.carimbo,
            ))
    # O "não" do DONO precisa chegar CLARO ao cérebro: um "ok, deixei pra lá"
    # soa como falha do sistema e o cérebro insiste (re-tenta e re-pergunta a
    # mesma coisa — hoje isso cairia no detector de loop).
    return Resultado(
        False,
        "O DONO recusou esta ação quando pedi a confirmação dele. NÃO tente de novo "
        "nem pergunte de novo — siga sem essa ação e, se ela fizer falta, só avise o "
        "que ficou sem fazer.",
    )


def conversar(
    cerebro: Cerebro,
    executar: Executar,
    texto: str,
    *,
    perguntar: Perguntar,
    mostrar: Mostrar,
    tracar: Mostrar | None = None,
    historico: list[dict] | None = None,
    pensando: Pensando | None = None,
    atividade: Atividade | None = None,
    recados: Recados | None = None,
) -> list[dict]:
    """Conduz UM turno do dono, do texto inicial até a resposta final, e DEVOLVE
    o histórico atualizado.

    Passar `historico` (de um turno anterior) dá MEMÓRIA: o cérebro enxerga a
    conversa toda, não só a última frase. Sem ele, começa do zero. Quem chama
    pode salvar o retorno em disco pra retomar depois (`mister --continue`).

    `tracar` (opcional) recebe as linhas de bastidor (o que o cérebro pensou e
    fez), separadas da resposta final, que vai em `mostrar`."""
    if historico is None:
        historico = []
    historico.append({"role": "user", "content": texto})

    def encerrar(mensagem: str) -> list[dict]:
        """Fala a mensagem final E a guarda no histórico (pra lembrar no próximo
        turno o que o Mister respondeu)."""
        historico.append({"role": "assistant", "content": mensagem})
        mostrar(mensagem)
        return historico

    repeticoes: list[tuple] = []  # últimas ações, pra achar loop de verdade

    while True:
        # RECADOS: trabalho em segundo plano (um envio do correio, por exemplo)
        # vive ENTRE turnos — dispara num, pode terminar noutro. O que TERMINOU
        # entra aqui como dado, sem bloquear ninguém, e o cérebro comenta na
        # próxima fala.
        for recado in (recados() if recados else []):
            historico.append({"role": "user", "content": (
                f"{MARCA_RECADO} um trabalho que rodava em segundo plano terminou "
                f"— avise o dono do desfecho na sua próxima fala: {recado}"
            )})
            if tracar:
                tracar(f"{MARCA_RECADO}: {recado}")

        try:
            with (pensando() if pensando else nullcontext()):
                decisao = cerebro.proximo_passo(historico)
        except ErroCerebro as e:
            # Falha de infra: avisa de verdade, não finge "não entendi". Não vira
            # "fala" do Mister no histórico — é tropeço transitório, não resposta.
            mostrar(f"⚠️ falha na API: {e}")
            return historico

        if tracar:
            tracar(f"[pensei: {decisao.raciocinio or '—'}]")
            tracar(f"[passo: intenção={decisao.intencao} params={decisao.params}]")

        # Detector de loop: 'responder'/None já encerram o turno sozinhos, não
        # precisam desse cabresto.
        if decisao.intencao not in (None, "responder"):
            chave = (
                decisao.intencao,
                json.dumps(decisao.params, sort_keys=True, ensure_ascii=False),
            )
            repeticoes.append(chave)
            repeticoes = repeticoes[-LOOP_JANELA:]
            if len(repeticoes) == LOOP_JANELA and len(set(repeticoes)) == 1:
                historico.append(_jogada(decisao))
                historico.append(_resposta(decisao, "[não executei: ação repetida]"))
                historico.append({"role": "user", "content": (
                    f"[loop detectado: você repetiu a MESMA ação {LOOP_JANELA}x seguidas "
                    "sem sair do lugar. PARE — não tente de novo. Em poucas frases, "
                    "explique ao dono o que você tentou fazer, por que travou, e peça um "
                    "direcionamento pra continuar. Responda em texto, sem chamar "
                    "ferramenta.]"
                )})
                try:
                    with (pensando() if pensando else nullcontext()):
                        explicacao = cerebro.proximo_passo(historico)
                except ErroCerebro:
                    return encerrar(
                        "Travei repetindo a mesma ação e nem consegui explicar o motivo. "
                        "Pode me dar uma direção pra continuar?"
                    )
                return encerrar(
                    explicacao.params.get("mensagem")
                    or explicacao.raciocinio
                    or "Travei repetindo a mesma ação — pode me dar uma direção pra continuar?"
                )

        # Resposta sem intenção (raro): é o cinto pra resposta vazia/malformada
        # da API. Entrega o raciocínio, que costuma explicar, em vez de uma frase
        # decorada.
        if decisao.intencao is None:
            return encerrar(
                decisao.raciocinio or "Me perdi nesse passo — me pede de outro jeito?"
            )

        # Controle 'responder': fim do turno, mensagem final do cérebro.
        if decisao.intencao == "responder":
            return encerrar(decisao.params.get("mensagem", "(sem resposta)"))

        # Controle 'perguntar': falta info -> pergunta e devolve ao cérebro.
        if decisao.intencao == "perguntar":
            pergunta = decisao.params.get("pergunta", "pode detalhar?")
            historico.append(_jogada(decisao))
            # A fala do dono é a RESPOSTA da chamada 'perguntar' — vai no par
            # nativo dela, não numa mensagem user solta. O `except` fecha o par
            # mesmo se o dono cancelar (ESC) no meio: jogada sem resposta deixa
            # o histórico inválido, e o PRÓXIMO turno é que morreria por isso.
            try:
                resposta_do_dono = perguntar(f"{pergunta} ")
            except BaseException:
                historico.append(_resposta(decisao, "[o dono cancelou antes de responder]"))
                raise
            historico.append(_resposta(decisao, resposta_do_dono))
            continue

        # Caso geral: é uma TOOL. Vai pelo despachante (validação + confirmação).
        with _ocupado(atividade, "executando", decisao.intencao):
            saida = executar(decisao)
        if isinstance(saida, Pendente):
            saida = _confirmar(saida, executar, perguntar, atividade)

        if tracar:
            tracar(f"[resultado: ok={saida.ok}] {saida.texto()}")

        # Devolve o resultado ao cérebro pra ele encadear ou concluir.
        historico.append(_jogada(decisao))
        historico.append(_resposta(decisao, saida.texto()))


def _jogada(decisao: Decisao) -> dict:
    """A jogada do cérebro como MENSAGEM NATIVA do histórico: a narração no
    content + a chamada de ferramenta com id. O protocolo exige o PAR — toda
    jogada destas precisa de uma `_resposta` logo depois. Decisão sem id (dublê
    de teste, repetição pós-confirmação) ganha um gerado, pro par bater."""
    if not decisao.id_chamada:
        decisao.id_chamada = f"chamada-{uuid.uuid4().hex[:8]}"
    return {
        "role": "assistant",
        "content": decisao.raciocinio or "",
        "tool_calls": [{
            "id": decisao.id_chamada,
            "type": "function",
            "function": {
                "name": decisao.intencao,
                "arguments": json.dumps(decisao.params, ensure_ascii=False),
            },
        }],
    }


def _resposta(decisao: Decisao, conteudo: str) -> dict:
    """A resposta nativa a uma jogada: o resultado (de tool, do sistema, ou a
    fala do dono numa pergunta) amarrado ao id da chamada."""
    return {"role": "tool", "tool_call_id": decisao.id_chamada, "content": conteudo}
