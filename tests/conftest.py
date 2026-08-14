"""Isolamento da suite: nenhum teste pode ler/escrever o ~/.mister REAL do dono.

TUDO que os módulos gravam aponta pra uma pasta descartável por teste — quem
quiser conteúdo, planta o seu. (Os módulos leem o env a cada chamada de
propósito, justamente pra isto funcionar — se criar caminho novo, cadastre a
variável aqui TAMBÉM.)

Lição herdada do projeto anterior: módulos que liam o env na IMPORTAÇÃO ficavam
de fora desta lista e meses de pytest despejaram fixture no arquivo REAL do
dono. Isolar aqui é a rede de proteção; ler o env por chamada é a regra que
segura ela.

Os caminhos graváveis: a conversa, o acervo de conversas, o MISTER.md (as
regras do dono), o grafo da memória (memoria/) e o índice dela. O correio
guarda o dele em ~/.config/ekodide/, que é do
EKODIDE, não daqui — e nenhum teste chega perto disso (o ekodide é dublado,
ver test_correio.py).

A trava de ler-antes (`leituras.py`) mora em RAM, fora do env — cada teste
zera o registro pra um não vazar leitura pro outro.
"""
import pytest

from mister import leituras


@pytest.fixture(autouse=True)
def mister_isolado(monkeypatch, tmp_path):
    monkeypatch.setenv("MISTER_CONVERSA", str(tmp_path / "conversa.json"))
    monkeypatch.setenv("MISTER_CONVERSAS", str(tmp_path / "conversas"))
    monkeypatch.setenv("MISTER_REGRAS", str(tmp_path / "MISTER.md"))
    monkeypatch.setenv("MISTER_MEMORIA", str(tmp_path / "memoria"))
    monkeypatch.setenv("MISTER_INDICE", str(tmp_path / "indice.json"))
    # Porta morta de propósito: teste que esquecer de dublar o embutir acha
    # "conexão recusada" na hora, nunca o Ollama REAL da máquina.
    monkeypatch.setenv("MISTER_OLLAMA", "http://127.0.0.1:9")
    leituras.esquecer_tudo()
