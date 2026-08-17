"""A LISTA DE TAREFAS da conversa — o que o Mister está fazendo e o que já fez.

Existe pro painel da direita da TUI ter o que mostrar, mas quem ESCREVE é o
cérebro, pela tool `lista_de_tarefas` (`mister/tools/tarefas.py`) — este módulo
é só o lugar onde a lista mora, do mesmo jeito que o `mexidos.py` guarda os
arquivos gravados.

A lista é REESCRITA INTEIRA a cada atualização (`definir`), nunca remendada
item a item: o modelo manda a lista toda, do jeito que ela ficou. É mais
simples e não deixa item órfão quando ele muda de ideia no meio do caminho.

Alcance: a CONVERSA. O `/nova` e o retomar limpam. Em RAM, sem arquivo.
"""
from __future__ import annotations

# Os três estados de um item, na ordem natural. Estado desconhecido vira
# 'pendente' — o painel nunca fica com um rótulo que ele não sabe desenhar.
ESTADOS = ("pendente", "fazendo", "feito")
PADRAO = "pendente"

_itens: list[dict] = []  # [{"texto": str, "estado": str}]


def definir(itens: list[dict]) -> list[dict]:
    """Reescreve a lista inteira e devolve como ela ficou.

    Cada item é um dicionário com 'texto' e (opcional) 'estado'. Texto vazio é
    descartado calado; estado fora dos três vira 'pendente'."""
    novos: list[dict] = []
    for item in itens or []:
        texto = str((item or {}).get("texto") or "").strip()
        if not texto:
            continue
        estado = str((item or {}).get("estado") or PADRAO).strip().lower()
        novos.append({
            "texto": texto,
            "estado": estado if estado in ESTADOS else PADRAO,
        })
    _itens[:] = novos
    return listar()


def listar() -> list[dict]:
    """Uma CÓPIA da lista, na ordem em que o cérebro a escreveu."""
    return [dict(item) for item in _itens]


def limpar() -> None:
    """Zera a lista — o `/nova`, o retomar de conversa e o isolamento de teste."""
    _itens.clear()


def resumo() -> str:
    """'2/5 feitas' — a linha curta do painel (e o texto que a tool devolve pro
    cérebro saber que a lista entrou). Lista vazia devolve string vazia."""
    if not _itens:
        return ""
    feitas = sum(1 for item in _itens if item["estado"] == "feito")
    return f"{feitas}/{len(_itens)} feitas"
