"""OS ARQUIVOS MEXIDOS — o que o Mister GRAVOU nesta conversa.

Módulo irmão do `leituras.py`, mesma ideia e mesmo formato: as tools são
funções puras e não enxergam a conversa, então quem MARCA (o
`escrever_arquivo`) e quem CONSULTA (o painel da direita da TUI) se encontram
aqui. Só o alvo muda — lá é o que foi LIDO, aqui é o que foi ESCRITO.

Alcance: a CONVERSA, não o processo. O `/nova` e o retomar LIMPAM a lista — o
painel mostra "o que eu mexi nesta conversa", não histórico eterno. Em RAM,
sem arquivo: fechou o Mister, some junto.

Sem repetir: arquivo gravado três vezes aparece UMA vez, no lugar da gravação
mais recente. O `listar` devolve do mais NOVO pro mais antigo, que é a ordem
que o painel desenha.
"""
from __future__ import annotations

import os
from pathlib import Path

_mexidos: list[str] = []  # caminhos absolutos (str), do mais antigo pro mais novo


def _absoluto(caminho: Path | str) -> str:
    """O caminho já expandido e absoluto. Sem `resolve()` estrito: arquivo que
    sumiu logo depois de gravado ainda tem que aparecer na lista."""
    return str(Path(os.path.expanduser(str(caminho))).absolute())


def marcar(caminho: Path | str) -> None:
    """Registra que `caminho` foi GRAVADO agora. Já estava na lista? Sobe pro
    fim (a marca vale pela gravação mais recente, sem duplicar a linha)."""
    alvo = _absoluto(caminho)
    if alvo in _mexidos:
        _mexidos.remove(alvo)
    _mexidos.append(alvo)


def listar() -> list[str]:
    """Os arquivos mexidos nesta conversa, do mais NOVO pro mais antigo."""
    return list(reversed(_mexidos))


def limpar() -> None:
    """Zera a lista — o `/nova`, o retomar de conversa e o isolamento de teste."""
    _mexidos.clear()


def encurtar(caminho: str, largura: int = 0) -> str:
    """O caminho como o painel mostra: a pasta do dono vira '~'. Fora dela,
    volta inteiro (encurtar demais esconderia de qual disco veio).

    Com `largura`, ainda corta PELA FRENTE até caber ('…/mister/tui.py'): o
    fim é o nome do arquivo, que é o que o dono está procurando na lista —
    deixar o textual quebrar a linha sozinho partia o nome no meio
    ('.../tu' + 'i.py'). Sem largura (o padrão), não corta nada."""
    casa = str(Path.home())
    if caminho == casa:
        texto = "~"
    elif caminho.startswith(casa + os.sep):
        texto = "~" + caminho[len(casa):]
    else:
        texto = caminho

    if largura <= 0 or len(texto) <= largura:
        return texto

    partes = texto.split(os.sep)
    # Vai comendo pasta da esquerda enquanto não couber. O último pedaço (o
    # nome do arquivo) nunca sai.
    for i in range(1, len(partes)):
        tentativa = "…" + os.sep + os.sep.join(partes[i:])
        if len(tentativa) <= largura:
            return tentativa
    # Nem o nome do arquivo sozinho cabe: corta a cabeça dele também.
    return "…" + partes[-1][-(largura - 1):] if largura > 1 else partes[-1][-1:]
