"""O agente: costura MÚLTIPLOS passos num turno.

O cérebro devolve um LOTE de até 4 chamadas por resposta (ações
INDEPENDENTES podem vir juntas — ver `prompts.montar_instrucao`); quem decide
quantas do lote de fato rodam é este módulo: cada chamada passa pelo
despachante, NA ORDEM, e se uma precisar do dono (confirmação ou 'perguntar')
o lote PARA nela — as anteriores já rodaram, ela espera, as seguintes não
rodam (mas todas ainda respondem no histórico, ver `_jogada_lote`).

A SEGURANÇA não passa por aqui. Cada chamada continua indo pelo `executar` (o
despachante), que valida os params contra o formulário e guarda a confirmação.
O agente só costura; ele nunca decide o que é seguro.

Cabrestos:
  - SEM teto de VOLTAS ao cérebro (cada volta roda um LOTE de até 4 chamadas).
    Um agente que encadeia lotes DIFERENTES roda até terminar; cancelar na
    hora é o ESC (a pele transforma em Ctrl+C). O que existe é um DETECTOR DE
    LOOP: se o cérebro repetir o MESMO LOTE (a lista ordenada de
    intenção+params de cada chamada) LOOP_JANELA vezes seguidas, é loop de
    verdade, não tarefa longa — aí ele para e EXPLICA pro dono o que tentou e
    onde travou, em vez de girar pra sempre.
  - 'perguntar' e 'responder' são intenções de CONTROLE, tratadas aqui — não são
    tools e não tocam o sistema. 'responder' só existe sozinho (lote de 1 — o
    cérebro nunca mistura resposta final com chamada de ferramenta).

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

# Detector de loop: repetir o MESMO LOTE (intenção + params de cada chamada
# elegível, na ordem) essa quantidade de vezes SEGUIDAS é loop de verdade, não
# tarefa longa.
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
                lote = cerebro.proximo_passo(historico)
        except ErroCerebro as e:
            # Falha de infra: avisa de verdade, não finge "não entendi". Não vira
            # "fala" do Mister no histórico — é tropeço transitório, não resposta.
            mostrar(f"⚠️ falha na API: {e}")
            return historico

        if tracar:
            tracar(f"[pensei: {lote[0].raciocinio or '—'}]")
            for decisao in lote:
                tracar(f"[passo: intenção={decisao.intencao} params={decisao.params}]")

        primeira = lote[0]

        # Resposta sem intenção (raro): é o cinto pra resposta vazia/malformada
        # da API. Entrega o raciocínio, que costuma explicar, em vez de uma frase
        # decorada. (Lote de 1 sempre — ver brain.proximo_passo.)
        if primeira.intencao is None:
            return encerrar(
                primeira.raciocinio or "Me perdi nesse passo — me pede de outro jeito?"
            )

        # Controle 'responder': fim do turno, mensagem final do cérebro. (Lote
        # de 1 sempre — o cérebro nunca mistura resposta final com chamada.)
        if primeira.intencao == "responder":
            return encerrar(primeira.params.get("mensagem", "(sem resposta)"))

        # TETO DO LOTE: só as 4 primeiras chamadas rodam; o resto ainda precisa
        # de resposta no histórico (ver o loop abaixo), só não executa.
        elegiveis, cortadas = lote[:4], lote[4:]

        # DETECTOR DE LOOP: a chave agora é o LOTE inteiro (a lista ordenada de
        # intenção+params de cada chamada elegível) — lote idêntico repetido
        # LOOP_JANELA vezes seguidas é loop de verdade, não tarefa longa.
        chave = tuple(
            (d.intencao, json.dumps(d.params, sort_keys=True, ensure_ascii=False))
            for d in elegiveis
        )
        repeticoes.append(chave)
        repeticoes = repeticoes[-LOOP_JANELA:]
        if len(repeticoes) == LOOP_JANELA and len(set(repeticoes)) == 1:
            historico.append(_jogada_lote(lote))
            for d in elegiveis:
                historico.append(_resposta(d, "[não executei: lote repetido]"))
            for d in cortadas:
                historico.append(_resposta(d, _MSG_CORTADA))
            historico.append({"role": "user", "content": (
                f"[loop detectado: você repetiu o MESMO LOTE {LOOP_JANELA}x seguidas "
                "sem sair do lugar. PARE — não tente de novo. Em poucas frases, "
                "explique ao dono o que você tentou fazer, por que travou, e peça um "
                "direcionamento pra continuar. Responda em texto, sem chamar "
                "ferramenta.]"
            )})
            try:
                with (pensando() if pensando else nullcontext()):
                    explicacao = cerebro.proximo_passo(historico)[0]
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

        # RODA O LOTE, na ordem: chamada que precisa do dono (confirmação ou
        # 'perguntar') PARA o lote ali — as anteriores já rodaram, as seguintes
        # não. Tudo isto vai numa ÚNICA jogada nativa no fim (ver _jogada_lote);
        # o try/finally garante que o que já rodou fica registrado mesmo se o
        # dono cancelar (ESC) no meio de uma pergunta/confirmação.
        respostas: list[tuple[Decisao, str]] = []
        try:
            parou = False
            for decisao in elegiveis:
                if parou:
                    respostas.append((decisao, _MSG_PAROU))
                    continue

                if decisao.intencao == "perguntar":
                    pergunta = decisao.params.get("pergunta", "pode detalhar?")
                    # O dono pode cancelar (ESC) bem no meio: registra o
                    # cancelamento nesta chamada antes de deixar a exceção
                    # subir, pra jogada+resposta fechar mesmo assim.
                    try:
                        resposta_do_dono = perguntar(f"{pergunta} ")
                    except BaseException:
                        respostas.append((decisao, "[o dono cancelou antes de responder]"))
                        raise
                    respostas.append((decisao, resposta_do_dono))
                    parou = True  # espera o dono: o resto do lote não roda ainda
                    continue

                # Caso geral: é uma TOOL. Vai pelo despachante (validação + confirmação).
                with _ocupado(atividade, "executando", decisao.intencao):
                    saida = executar(decisao)
                if isinstance(saida, Pendente):
                    try:
                        saida = _confirmar(saida, executar, perguntar, atividade)
                    except BaseException:
                        respostas.append((decisao, "[o dono cancelou antes de responder]"))
                        raise
                    parou = True  # a confirmação já rodou/recusou: para aqui

                if tracar:
                    tracar(f"[resultado: ok={saida.ok}] {saida.texto()}")
                respostas.append((decisao, saida.texto()))

            for decisao in cortadas:
                respostas.append((decisao, _MSG_CORTADA))
        finally:
            # Só registra o que de fato ganhou uma resposta — decisão cancelada
            # ANTES de responder (ESC bem no meio de perguntar/confirmar) fica
            # de fora: nunca dizemos ao histórico que algo rodou sem completar.
            if respostas:
                historico.append(_jogada_lote([d for d, _ in respostas]))
                for d, texto in respostas:
                    historico.append(_resposta(d, texto))


# Textos padrão de "não executei" — a chamada sem resposta invalida o
# histórico nativo (todo tool_call exige seu par), então mesmo quem não rodou
# precisa de uma linha aqui.
_MSG_PAROU = "[não executei: o lote parou numa chamada anterior que precisava do dono]"
_MSG_CORTADA = "[não executei: vieram mais de 4 chamadas nesta resposta, só as 4 primeiras contam]"


def _jogada_lote(decisoes: list[Decisao]) -> dict:
    """O LOTE inteiro como UMA mensagem NATIVA do histórico: a narração no
    content (uma só, igual em todas as decisões do lote) + TODAS as chamadas
    juntas no campo `tool_calls` — é o formato que o protocolo exige quando o
    modelo manda mais de uma chamada na mesma resposta. Cada uma tem que
    ganhar sua `_resposta` logo depois (ver o chamador). Decisão sem id (dublê
    de teste, repetição pós-confirmação) ganha um gerado, pro par bater."""
    for decisao in decisoes:
        if not decisao.id_chamada:
            decisao.id_chamada = f"chamada-{uuid.uuid4().hex[:8]}"
    return {
        "role": "assistant",
        "content": (decisoes[0].raciocinio or "") if decisoes else "",
        "tool_calls": [{
            "id": decisao.id_chamada,
            "type": "function",
            "function": {
                "name": decisao.intencao,
                "arguments": json.dumps(decisao.params, ensure_ascii=False),
            },
        } for decisao in decisoes],
    }


def _resposta(decisao: Decisao, conteudo: str) -> dict:
    """A resposta nativa a uma jogada: o resultado (de tool, do sistema, ou a
    fala do dono numa pergunta) amarrado ao id da chamada."""
    return {"role": "tool", "tool_call_id": decisao.id_chamada, "content": conteudo}
