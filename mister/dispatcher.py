"""O despachante: recebe a decisão do cérebro, VALIDA os parâmetros contra o
formulário da tool e, só se passar, EXECUTA.

É aqui que mora a confiabilidade do Mister: nada roda sem o formulário bater.
Mesmo com um LLM do outro lado, é esta camada burra e previsível que impede um
parâmetro errado de virar uma ação errada.

Ele NÃO importa o cérebro: recebe qualquer objeto com a forma de uma decisão
(`intencao`, `params`, `aval_do_dono`, `carimbo`) — quem constrói é o
`brain.Decisao`, mas o despachante não precisa saber disso pra funcionar (e o
teste dele roda sem cérebro nenhum).
"""
from __future__ import annotations

from typing import TYPE_CHECKING

from pydantic import ValidationError

from mister.confirmacao import CAMPOS_INTERNOS, Pendente, PrecisaConfirmar, carimbar
from mister.registry import REGISTRO
from mister.resultado import Resultado

if TYPE_CHECKING:  # só pro type checker — em tempo de execução é duck typing
    from mister.brain import Decisao


def despachar(decisao: "Decisao") -> Resultado | Pendente:
    if decisao.intencao is None:
        return Resultado(False, "Não entendi o comando.")

    spec = REGISTRO.get(decisao.intencao)
    if spec is None:
        return Resultado(False, f"Intenção desconhecida: '{decisao.intencao}'.")

    # TRANCA ANTI-FURO: campo INTERNO (ex.: 'confirmado', ver
    # confirmacao.CAMPOS_INTERNOS) fica escondido do cérebro no prompt, mas um
    # modelo enganado/injetado pode preenchê-lo mesmo assim e pular a
    # confirmação. Aqui esses campos são DESCARTADOS, determinístico: só a
    # decisão CARIMBADA pelo loop após o dono topar (aval_do_dono) os mantém.
    # Prompt esconde; despachante GARANTE.
    params = dict(decisao.params)
    if not decisao.aval_do_dono:
        for nome in CAMPOS_INTERNOS:
            params.pop(nome, None)
    else:
        # CARIMBO ANTI-DRIFT: aval do dono só vale pra ação EXATA que ele viu.
        # Recalcula a impressão digital (intenção + params + cwd + arquivos-
        # operando) e nega se não bater — params editados, arquivo trocado no
        # meio do caminho ou aval sem Pendente de origem caem todos aqui.
        if not decisao.carimbo or carimbar(decisao.intencao, params) != decisao.carimbo:
            return Resultado(
                False,
                "A ação mudou entre a sua aprovação e a execução (ou veio sem o "
                "carimbo da confirmação) — cancelei por segurança. Peça de novo.",
            )

    # VALIDA: tenta encaixar os parâmetros no formulário da tool.
    try:
        formulario_preenchido = spec.formulario(**params)
    except ValidationError as erro:
        return Resultado(False, f"Parâmetros inválidos para '{decisao.intencao}':\n{erro}")

    # EXECUTA: só chega aqui se o formulário bateu.
    try:
        return spec.handler(formulario_preenchido)
    except PrecisaConfirmar as pedido:
        # A tool não vai agir sem aval. Devolve a ação "engatilhada", já marcada
        # como confirmada, para o loop repetir se o usuário topar.
        # Os params do Pendente saem dos LIMPOS (pós-tranca), não dos crus do
        # modelo — o que o dono aprova é exatamente o que vai rodar.
        return Pendente(
            pergunta=pedido.pergunta,
            intencao=decisao.intencao,
            params={**params, "confirmado": True},
        )
