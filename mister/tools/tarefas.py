"""A tool da LISTA DE TAREFAS — o cérebro anotando o plano dele na tela.

É a única tool que não faz nada no mundo: só escreve no `mister/tarefas.py`,
que o painel da direita da TUI lê. Serve pro dono ver o plano de um pedido de
vários passos andando, em vez de esperar calado até o fim.

A lista vai INTEIRA a cada chamada (o formulário pede `itens`, não "adicione
um item"): o modelo reescreve o plano do jeito que ele ficou. Sem remendo, sem
item órfão quando ele muda de rumo no meio.
"""
from __future__ import annotations

from typing import Literal

from pydantic import BaseModel

from mister import tarefas
from mister.registry import tool
from mister.resultado import Resultado


class ItemDeTarefa(BaseModel):
    texto: str
    estado: Literal["pendente", "fazendo", "feito"] = "pendente"


class ListaDeTarefasParams(BaseModel):
    itens: list[ItemDeTarefa]


@tool(
    "lista_de_tarefas",
    ListaDeTarefasParams,
    "ANOTA na tela o plano de um pedido de VÁRIOS PASSOS, pro dono acompanhar "
    "o que já andou. Use SÓ quando a tarefa tiver 3 ou mais passos de verdade: "
    "pergunta simples, bate-papo e tarefa de um passo só NÃO levam lista — "
    "anotar à toa só polui a tela. Mande a lista INTEIRA toda vez (ela "
    "substitui a anterior), marcando o estado de cada item: 'fazendo' no que "
    "você está executando AGORA (um só por vez), 'feito' no que terminou, "
    "'pendente' no resto. Chame de novo a cada passo que muda de estado.",
)
def lista_de_tarefas(params: ListaDeTarefasParams) -> Resultado:
    itens = tarefas.definir([item.model_dump() for item in params.itens])
    if not itens:
        tarefas.limpar()
        return Resultado(True, "Lista de tarefas limpa.")
    return Resultado(True, f"Lista anotada ({tarefas.resumo()}).")
