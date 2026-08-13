"""O GRAFO — a memória de longo prazo do Mister, em arquivos que qualquer um lê.

A ideia em uma frase: o Mister guarda tudo, mas nunca lê tudo. Aqui mora o
GUARDAR; o ler-só-o-que-tem-a-ver vem com o índice e a leitura automática
(etapas seguintes).

O formato é aberto de propósito: notas `.md` numa pasta (`~/.mister/memoria/`),
ligadas por `[[link]]` — os links são o que faz disso um GRAFO e não uma pilha.
Nota pode ser grande; não é obrigatório picar em notas pequenas. Qualquer
programa de notas (Logseq, por exemplo) mostra a pasta com os links clicáveis —
mas isso é só pro olho humano: o Mister não sabe que ele existe, e trocar de
programa (ou não usar nenhum) não muda uma linha daqui.

Quem escreve aqui é o MISTER, sozinho — é a outra metade da divisão da memória
(regra é do dono, no MISTER.md; fato é do Mister, no grafo). Um `[[link]]` pra
nota que ainda não existe não é erro: marca coisa que vale escrever depois.

Nada vence prazo: nota só some se o dono mandar apagar ou se o Mister apagar.
"""
from __future__ import annotations

import os
import re
import unicodedata
from pathlib import Path

PASTA_PADRAO = str(Path.home() / ".mister" / "memoria")


def _pasta() -> Path:
    """Lê MISTER_MEMORIA A CADA chamada (não na importação) — é o que deixa o
    conftest apontar os testes pra longe do grafo REAL do dono."""
    return Path(os.environ.get("MISTER_MEMORIA", PASTA_PADRAO))


def slug(titulo: str) -> str:
    """O nome de arquivo de uma nota: sem acento, minúsculo, hífen no lugar do
    resto ('Celular Redmi' -> 'celular-redmi'). É UMA função de propósito: quem
    escreve a nota e quem resolve um [[link]] passam pelo MESMO funil — 'Celular
    Redmi' e '[[celular-redmi]]' apontam pro mesmo arquivo."""
    puro = unicodedata.normalize("NFKD", titulo).encode("ascii", "ignore").decode("ascii")
    return "-".join(re.findall(r"[a-z0-9]+", puro.lower()))


def caminho_da(titulo: str) -> Path:
    """Onde a nota desse título mora (exista ela ou não)."""
    return _pasta() / f"{slug(titulo)}.md"


def escrever(titulo: str, conteudo: str) -> Path:
    """Grava uma nota no grafo e devolve o caminho dela.

    Nota NOVA nasce com o título como cabeçalho. Nota que JÁ existe ganha o
    conteúdo no FIM, sem apagar nada — juntar, reescrever e podar é trabalho do
    "sonhar" (fica pra depois da TUI), não da caneta. Os `[[links]]` vão no
    conteúdo, do jeito que vieram.

    Levanta OSError se o disco recusar: anotar é a ação pedida, não best-effort
    — falha tem que aparecer (a thread do disparo a transforma em fala)."""
    caminho = caminho_da(titulo)
    caminho.parent.mkdir(parents=True, exist_ok=True)
    corpo = conteudo.strip()
    if caminho.exists():
        atual = caminho.read_text(encoding="utf-8").rstrip()
        caminho.write_text(f"{atual}\n\n{corpo}\n", encoding="utf-8")
    else:
        caminho.write_text(f"# {titulo.strip()}\n\n{corpo}\n", encoding="utf-8")
    return caminho


def listar() -> list[Path]:
    """As notas do grafo (pasta ainda sem nota = lista vazia, nunca erro)."""
    try:
        return sorted(_pasta().glob("*.md"))
    except OSError:
        return []
