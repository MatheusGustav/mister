"""OS INTERRUPTORES — os botões de ligar/desligar do dono.

Estavam prometidos desde o desenho da memória ("o interruptor é botão, e botão
mora na TUI"): agora a TUI existe, eles existem. Quatro botões:

  - `anotar`   — o Mister pode gravar nota nova no grafo (anotar_memoria)?
  - `revisar`  — a revisão em segundo plano (10 min de ociosidade) roda?
  - `celular`  — as 5 tools do ekodide (enviar/olhar/puxar...) atendem?
  - `internet` — as 2 tools da Exa (pesquisar_web/abrir_pagina) atendem?

TUDO NASCE LIGADO — desligar é exceção, não padrão (instalado = disponível, a
regra de sempre). O estado persiste em `~/.mister/interruptores.json` pra
sobreviver ao fechar; o arquivo só guarda o que foi DESLIGADO alguma vez —
botão que nunca foi tocado nem aparece nele.

Quem CONSULTA é a própria peça desligável (a tool do anotar, o porteiro do
correio, o gatilho do revisar na TUI) — não a tela. Assim o interruptor vale
em QUALQUER pele, e a TUI é só o dedo que aperta.
"""
from __future__ import annotations

import json
import os
from pathlib import Path

ARQUIVO_PADRAO = str(Path.home() / ".mister" / "interruptores.json")

# nome -> o que o botão liga/desliga (o texto que a TUI mostra).
NOMES = {
    "anotar": "o Mister anotar memória nova por conta própria",
    "revisar": "a revisão das notas em segundo plano (10 min parado)",
    "celular": "as ferramentas do celular (ekodide)",
    "internet": "as ferramentas de internet (pesquisar e abrir página)",
}


def _arquivo() -> Path:
    """Lê MISTER_INTERRUPTORES A CADA chamada (não na importação) — o mesmo
    truque do resto do ~/.mister: o conftest aponta os testes pra longe."""
    return Path(os.environ.get("MISTER_INTERRUPTORES", ARQUIVO_PADRAO))


# Rede pro disco falhar: um toque que não conseguiu persistir vale MESMO ASSIM
# nesta sessão (fica aqui, por cima do arquivo) — melhor que negar o toque.
# Gravou de novo com sucesso, a sobreposição esvazia.
_so_na_sessao: dict = {}


def _estado() -> dict:
    try:
        lido = json.loads(_arquivo().read_text(encoding="utf-8"))
        no_disco = lido if isinstance(lido, dict) else {}
    except (OSError, ValueError):
        no_disco = {}  # sem arquivo (ou quebrado) = ninguém desligou nada
    return {**no_disco, **_so_na_sessao}


def ligado(nome: str) -> bool:
    """O botão `nome` está ligado? Botão desconhecido/nunca tocado = ligado."""
    return bool(_estado().get(nome, True))


def alternar(nome: str) -> bool:
    """Aperta o botão: liga o desligado, desliga o ligado. Devolve o estado
    NOVO, que vale já — persistindo no disco ou (se ele recusar) só na sessão."""
    estado = _estado()
    estado[nome] = not ligado(nome)
    try:
        arquivo = _arquivo()
        arquivo.parent.mkdir(parents=True, exist_ok=True)
        arquivo.write_text(json.dumps(estado, ensure_ascii=False), encoding="utf-8")
        _so_na_sessao.clear()
    except OSError:
        _so_na_sessao[nome] = estado[nome]
    return estado[nome]
