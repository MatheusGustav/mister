"""O MISTER.md — as regras fixas do dono, lidas INTEIRAS em toda mensagem.

Esta é a metade SEM busca da memória de longo prazo (a outra metade é o grafo,
que vem nas etapas seguintes). A divisão tem motivo: regra que só aparece
quando o assunto bate já foi quebrada antes de ser lembrada — "nunca publica
X" guardada num canto buscável só apareceria numa conversa sobre X, mas o
pedido vai ser outro, e aí é tarde. Fato pode esperar o assunto puxar; regra
não pode. Por isso o arquivo vai INTEIRO no prompt, sem busca nenhuma.

O arquivo paga pedágio de contexto em TODA mensagem — ele é pequeno DE
PROPÓSITO. Quem escreve é a tool `guardar_regra`, direto (escrever regra é
reversível — não pede confirmação, ver `confirmacao.pergunta_de_confirmacao`).
Fato solto, spec de aparelho, macete — isso NÃO é regra, vai pro grafo.

Best-effort como toda memória: arquivo sumido/ilegível = sem regras, nunca um
Mister que não sobe.
"""
from __future__ import annotations

import os
from pathlib import Path

CAMINHO_PADRAO = str(Path.home() / ".mister" / "MISTER.md")

# O título do arquivo quando ele nasce. Uma linha só: o arquivo inteiro entra
# no prompt a cada mensagem — cabeçalho gordo seria pedágio pago pra sempre.
_TITULO = "# Regras do dono\n"


def _caminho() -> str:
    """Lê MISTER_REGRAS A CADA chamada (não na importação) — é o que deixa o
    conftest apontar os testes pra longe do arquivo REAL do dono."""
    return os.environ.get("MISTER_REGRAS", CAMINHO_PADRAO)


def ler() -> str:
    """O arquivo INTEIRO, do jeito que está — ou "" se não existe/não abre.
    Quem costura no prompt é o `prompts.montar_instrucao`, a cada mensagem."""
    try:
        return Path(_caminho()).read_text(encoding="utf-8").strip()
    except OSError:
        return ""


def adicionar(regra: str) -> str:
    """Grava UMA regra nova no fim do arquivo (criando-o na primeira) e devolve
    o texto gravado. Regra é uma LINHA: quebra interna vira espaço.

    Levanta OSError se o disco recusar: gravar regra não é best-effort, é a
    ação pedida — falha tem que aparecer, não sumir calada."""
    linha = " ".join(regra.split())
    caminho = Path(_caminho())
    caminho.parent.mkdir(parents=True, exist_ok=True)
    atual = ler()
    corpo = f"{atual}\n- {linha}\n" if atual else f"{_TITULO}\n- {linha}\n"
    caminho.write_text(corpo, encoding="utf-8")
    return linha
