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

from mister.confirmacao import (
    CAMPOS_INTERNOS,
    Pendente,
    PrecisaConfirmar,
    carimbar,
    pergunta_de_confirmacao,
)
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

    # VALIDA: tenta encaixar os parâmetros no formulário da tool. Duas coisas
    # saem ANTES, e as duas só AQUI — depois da conferência do carimbo, que
    # segue calculada sobre os params inteiros, do jeito que o `Pendente` os
    # guardou (tirar antes faria o carimbo deixar de bater):
    #
    #   - os CAMPOS INTERNOS: não moram no formulário, e com o `extra="forbid"`
    #     da base (`registry.Formulario`) um 'confirmado' sobrando faria toda
    #     ação confirmada morrer com "parâmetros inválidos" DEPOIS do sim do dono;
    #   - os NULOS: no strict TODO campo é obrigatório e "não preenchi" chega
    #     como null (ver prompts._schema_params). Descartar é o que faz o
    #     DEFAULT do formulário valer — sem isto, 'pasta': null viraria erro de
    #     tipo. Vale porque nenhum formulário aceita None de propósito hoje; se
    #     um dia aceitar (campo Optional de verdade), esta linha precisa saber
    #     diferenciar "não preenchi" de "preenchi com nada".
    limpos = {
        nome: valor
        for nome, valor in params.items()
        if nome not in CAMPOS_INTERNOS and valor is not None
    }
    try:
        formulario_preenchido = spec.formulario(**limpos)
    except ValidationError as erro:
        return Resultado(False, f"Parâmetros inválidos para '{decisao.intencao}':\n{erro}")

    # A CONFIRMAÇÃO É DO DESPACHANTE: olha intenção+params ANTES de executar —
    # critério é irreversibilidade, não escrita (ver confirmacao.pergunta_de_
    # confirmacao). Só entra em jogo sem aval do dono ainda; com aval, o carimbo
    # acima já garantiu que é a MESMA ação que ele aprovou.
    try:
        if not decisao.aval_do_dono:
            pergunta = pergunta_de_confirmacao(decisao.intencao, params)
            if pergunta:
                raise PrecisaConfirmar(pergunta)
        # EXECUTA: só chega aqui se o formulário bateu e não pediu aval.
        return spec.handler(formulario_preenchido)
    except PrecisaConfirmar as pedido:
        # Devolve a ação "engatilhada", já marcada como confirmada, para o loop
        # repetir se o usuário topar. Os params do Pendente saem dos LIMPOS
        # (pós-tranca), não dos crus do modelo — o que o dono aprova é
        # exatamente o que vai rodar.
        return Pendente(
            pergunta=pedido.pergunta,
            intencao=decisao.intencao,
            params={**params, "confirmado": True},
        )
