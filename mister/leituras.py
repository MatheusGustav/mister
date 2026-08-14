"""A TRAVA DE LER-ANTES — o registro do que já foi lido nesta sessão.

`trocar_trecho`, `reescrever_nota`, `apagar_nota` (e `escrever_arquivo`) recusam
mexer em algo que o cérebro nunca leu — mesma ideia do Edit/Read do Claude
Code: ele tem que ter visto o conteúdo antes de mandar sobrescrever ou apagar.
As tools são funções puras e não enxergam a conversa, então este módulo é a
peça COMPARTILHADA entre quem MARCA (`ler_nota`, `ler_arquivo`, a leitura
automática) e quem CONSULTA (as tools que mexem no que já existe).

Alcance: a SESSÃO DO PROCESSO — em RAM, sem arquivo. Reabriu o Mister, tem que
ler de novo. Junto com o caminho guarda mtime+tamanho do momento da leitura:
se o arquivo mudou por fora depois (o dono editou na mão, outro processo
mexeu), a marca não bate mais e a trava recusa — manda ler de novo antes de
confiar no que está sobrescrevendo.
"""
from __future__ import annotations

from pathlib import Path

_lidos: dict[str, str] = {}  # caminho absoluto (str) -> marca (mtime_ns:tamanho)


def _marca(caminho: Path) -> str | None:
    try:
        stat = caminho.stat()
    except OSError:
        return None
    return f"{stat.st_mtime_ns}:{stat.st_size}"


def marcar(caminho: Path) -> None:
    """Registra que `caminho` foi lido AGORA — guarda a marca do momento. Path
    sumido/sem permissão simplesmente não marca (nada pra travar depois)."""
    marca = _marca(caminho)
    if marca is not None:
        _lidos[str(Path(caminho).resolve())] = marca


def foi_lido(caminho: Path) -> bool:
    """`caminho` foi lido nesta sessão E continua do jeito que estava? Arquivo
    sumido, nunca lido, ou mudado por fora desde a leitura = False."""
    marca_atual = _marca(Path(caminho))
    if marca_atual is None:
        return False
    return _lidos.get(str(Path(caminho).resolve())) == marca_atual


def instantaneo() -> dict[str, str]:
    """Uma CÓPIA do registro, par com `restaurar`: o revisar tira uma antes da
    passada e devolve depois — as leituras da revisão não valem como leitura
    da conversa."""
    return dict(_lidos)


def restaurar(copia: dict[str, str]) -> None:
    """Volta o registro pro estado de uma cópia do `instantaneo`. Marca feita
    por outro caminho DEPOIS da cópia se perde — falha fechada: quem perdeu a
    marca só precisa ler de novo."""
    _lidos.clear()
    _lidos.update(copia)


def esquecer_tudo() -> None:
    """Zera o registro — só pra isolamento de teste (ver conftest.py)."""
    _lidos.clear()
