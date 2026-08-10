"""Isolamento da suite: nenhum teste pode ler/escrever o ~/.mister REAL do dono.

TUDO que os módulos gravam aponta pra uma pasta descartável por teste — quem
quiser conteúdo, planta o seu. (Os módulos leem o env a cada chamada de
propósito, justamente pra isto funcionar — se criar caminho novo, cadastre a
variável aqui TAMBÉM.)

Lição herdada do projeto anterior: módulos que liam o env na IMPORTAÇÃO ficavam
de fora desta lista e meses de pytest despejaram fixture no arquivo REAL do
dono. Isolar aqui é a rede de proteção; ler o env por chamada é a regra que
segura ela.

No MVP só existem dois caminhos graváveis (a conversa e o acervo de conversas):
o correio guarda o dele em ~/.config/ekodide/, que é do EKODIDE, não daqui — e
nenhum teste chega perto disso (o ekodide é dublado, ver test_correio.py).
"""
import pytest


@pytest.fixture(autouse=True)
def mister_isolado(monkeypatch, tmp_path):
    monkeypatch.setenv("MISTER_CONVERSA", str(tmp_path / "conversa.json"))
    monkeypatch.setenv("MISTER_CONVERSAS", str(tmp_path / "conversas"))
