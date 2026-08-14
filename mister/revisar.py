"""O REVISAR — o Mister arrumando as próprias notas, em segundo plano.

(O desenho chamava isto de "sonhar"; o nome de verdade é REVISAR, decisão do
dono em 14/08/2026.)

A especificação fechada (13-14/08/2026):

  - DISPARA com 10 minutos de ociosidade — dono sem digitar, janela aberta.
    Quem conta o tempo é a TUI (o relógio mora lá); aqui mora só o trabalho.
  - UMA passada por ociosidade: revisou, o gatilho só rearma depois da
    próxima mensagem do dono.
  - Na passada ele lê TODAS as notas e corrige/reescreve/apaga quantas quiser,
    com as MESMAS tools de nota da conversa (trocar_trecho, reescrever_nota,
    apagar_nota) — despachante, validação e trava de ler-antes iguais.
  - SEM cópia de segurança.
  - Desligável pelo interruptor `revisar` (quem checa é o gatilho, na TUI).

A CONFIRMAÇÃO AQUI DENTRO: `apagar_nota` pede aval do dono — mas em segundo
plano não há dono olhando. A decisão do dono (13/08/2026: na revisão "ele pode
apagar, também") vale como aval em pé: o Pendente que o despachante devolver é
re-despachado já aprovado, pelo MESMO caminho do carimbo que o laço da conversa
usa. É aprovação DE PROJETO, dada uma vez — não um furo: fora da revisão, a
pergunta continua acontecendo.

Roda pelo `envios.disparar` (o encanamento de segundo plano que já existia): a
fala de desfecho chega como recado num turno seguinte.
"""
from __future__ import annotations

from mister import leituras, memoria
from mister.agente import _jogada_lote, _resposta
from mister.brain import Decisao
from mister.confirmacao import Pendente
from mister.dispatcher import despachar

# As únicas tools que a revisão pode usar. O cérebro recebe a lista inteira de
# fichas (o motor é o mesmo da conversa), então o cinto é AQUI: chamada fora
# desta lista não executa — responde "fora do escopo" e a revisão segue.
TOOLS_DA_REVISAO = frozenset({"ler_nota", "trocar_trecho", "reescrever_nota", "apagar_nota"})

# Teto de lotes numa passada: a revisão é arrumação, não expedição. Estourou,
# para com o que fez e diz que parou — nunca gira a noite inteira.
MAX_LOTES = 40

_INSTRUCAO = (
    "[revisão de rotina — isto NÃO é o dono falando; é você, Mister, cuidando "
    "de você mesmo]\n"
    "O dono está há um tempo sem digitar e você tem a janela livre pra revisar "
    "SUAS notas de memória (o grafo). Abaixo vão TODAS elas, já lidas — pode "
    "corrigir direto.\n\n"
    "O que procurar, nota a nota:\n"
    "- fato REPETIDO (o mesmo dito duas vezes): deixe um só;\n"
    "- fato ENVELHECIDO convivendo com o novo (ex.: duas versões do mesmo "
    "número): fique com o atual;\n"
    "- nota SOLTA que tem a ver com outra: ligue com [[titulo-da-outra]];\n"
    "- nota MORTA (assunto que não existe mais): apague.\n\n"
    "Como corrigir: trocar_trecho é o padrão (velho exato -> novo); "
    "reescrever_nota quando a arrumação é a nota inteira; apagar_nota pra nota "
    "morta. SÓ essas ferramentas — nada de comando, arquivo ou celular aqui.\n"
    "Não pergunte nada: ninguém responde em segundo plano.\n"
    "Nota em ordem NÃO se mexe — pouca coisa errada, pouca mudança; nada "
    "errado, nenhuma. Quando terminar, responda em texto UMA frase curta "
    "resumindo o que fez (ou que estava tudo em ordem).\n\n"
)


def _notas_no_prompt() -> tuple[str, int]:
    """Todas as notas do grafo costuradas pro prompt, já MARCADAS como lidas
    (o conteúdo inteiro vai junto — é a mesma garantia que a trava pede da
    conversa). Devolve (texto, quantas)."""
    partes: list[str] = []
    for caminho in memoria.listar():
        try:
            corpo = caminho.read_text(encoding="utf-8").strip()
        except OSError:
            continue  # sumiu no meio: a revisão segue sem ela
        leituras.marcar(caminho)
        partes.append(f"--- nota: {caminho.stem} ---\n{corpo}")
    return "\n\n".join(partes), len(partes)


def _executar(decisao: Decisao) -> str:
    """Uma chamada da revisão, pelo despachante de sempre. `Pendente` (o
    apagar_nota pedindo aval) volta aprovado pelo caminho do carimbo — a
    aprovação de projeto explicada no topo."""
    if decisao.intencao not in TOOLS_DA_REVISAO:
        return (
            "[fora do escopo da revisão — aqui só ler_nota, trocar_trecho, "
            "reescrever_nota e apagar_nota]"
        )
    saida = despachar(decisao)
    if isinstance(saida, Pendente):
        saida = despachar(Decisao(
            saida.intencao, saida.params,
            aval_do_dono=True, carimbo=saida.carimbo,
        ))
    return saida.texto()


def rodar(cerebro) -> str:
    """UMA passada de revisão, do começo ao fim, e a fala de desfecho (é o
    formato que o `envios.disparar` espera). Erro de API estoura pro envios
    transformar em fala de quebra — nunca some calado."""
    notas, quantas = _notas_no_prompt()
    if not quantas:
        return "Fui revisar minhas notas e o grafo está vazio — nada pra fazer."

    historico: list[dict] = [{"role": "user", "content": _INSTRUCAO + notas}]
    for _ in range(MAX_LOTES):
        lote = cerebro.proximo_passo(historico)
        primeira = lote[0]
        if primeira.intencao in (None, "responder"):
            resumo = primeira.params.get("mensagem") or primeira.raciocinio or ""
            return f"Revisei minhas notas ({quantas}). {resumo}".strip()
        if primeira.intencao == "perguntar":
            # Sem dono pra responder: fecha o par e manda terminar.
            historico.append(_jogada_lote(lote))
            for decisao in lote:
                historico.append(_resposta(decisao, (
                    "[em segundo plano ninguém responde — termine a revisão "
                    "com o que dá e resuma em texto]"
                )))
            continue
        historico.append(_jogada_lote(lote))
        for decisao in lote[:4]:
            historico.append(_resposta(decisao, _executar(decisao)))
        for decisao in lote[4:]:
            historico.append(_resposta(decisao, "[não executei: passou do teto de 4]"))
    return (
        f"Comecei a revisar minhas notas ({quantas}) mas parei no teto de "
        f"{MAX_LOTES} passos — o que arrumei ficou; o resto fica pra próxima."
    )
