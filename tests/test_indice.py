"""O índice: o picadinho por seção, a sincronização preguiçosa e a mira.

O embeddinggemma é DUBLADO (um "modelo" de brincadeira que conta palavras):
o que está sob teste é o MECANISMO — picar, re-embutir só o que mudou, score
da nota pela melhor seção — não a qualidade do modelo. A promessa da etapa:
dado um texto, ele aponta a nota certa.
"""
import os
import time
from pathlib import Path

import pytest

from mister import indice, memoria


# Um "embedding" de brincadeira: cada dimensão conta uma palavra-chave. Serve
# porque o cosseno de verdade roda em cima — só o modelo é de mentira.
_PALAVRAS = ("celular", "correio", "microfone")


def _vetor_de(texto: str) -> list[float]:
    texto = texto.lower()
    vetor = [float(texto.count(p)) for p in _PALAVRAS]
    return vetor if any(vetor) else [0.0, 0.0, 0.001]  # nunca o vetor nulo


@pytest.fixture
def embutidor_de_mentira(monkeypatch):
    """Troca o Ollama por contagem de palavras e conta os LOTES pedidos."""
    lotes: list[list[str]] = []

    def _embutir(textos):
        lotes.append(list(textos))
        return [_vetor_de(t) for t in textos]

    monkeypatch.setattr(indice, "embutir", _embutir)
    return lotes


# --- o picadinho --------------------------------------------------------------

def test_picar_separa_por_titulo_de_secao():
    pedacos = indice.picar("# Nota\n\nintro\n\n## Bateria\ndura o dia\n\n## ADB\ntem trava")
    assert len(pedacos) == 3
    assert pedacos[0].startswith("# Nota")
    assert "Bateria" in pedacos[1] and "ADB" in pedacos[2]


def test_texto_sem_titulo_e_um_pedaco_so():
    assert indice.picar("só um parágrafo corrido") == ["só um parágrafo corrido"]


def test_pedaco_em_branco_nao_vira_numero():
    assert indice.picar("") == []
    assert indice.picar("# A\n\n\n# B\ncorpo") == ["# A", "# B\ncorpo"]


# --- a sincronização preguiçosa ----------------------------------------------

def test_atualizar_embute_a_nota_nova_e_depois_sossega(embutidor_de_mentira):
    memoria.escrever("Celular Redmi", "specs do celular")
    indice.atualizar()
    assert len(embutidor_de_mentira) == 1  # embutiu a nota nova
    indice.atualizar()
    assert len(embutidor_de_mentira) == 1  # nada mudou: só stat, sem Ollama


def test_nota_mudada_e_re_embutida(embutidor_de_mentira):
    caminho = memoria.escrever("Celular Redmi", "specs")
    indice.atualizar()
    memoria.escrever("Celular Redmi", "a bateria dura o dia")
    os.utime(caminho, ns=(time.time_ns(), time.time_ns() + 1))  # mtime anda
    indice.atualizar()
    assert len(embutidor_de_mentira) == 2
    assert any("bateria" in t for t in embutidor_de_mentira[1])


def test_nota_apagada_sai_do_indice(embutidor_de_mentira):
    memoria.escrever("Celular Redmi", "specs do celular")
    memoria.escrever("ekodide", "o correio do PC")
    indice.atualizar()
    memoria.caminho_da("Celular Redmi").unlink()
    assert set(indice.atualizar()["notas"]) == {"ekodide"}


def test_indice_corrompido_se_reconstroi_sozinho(embutidor_de_mentira):
    memoria.escrever("Celular Redmi", "specs do celular")
    indice.atualizar()
    Path(os.environ["MISTER_INDICE"]).write_text("{lixo", encoding="utf-8")
    assert "celular-redmi" in indice.atualizar()["notas"]
    assert len(embutidor_de_mentira) == 2  # derivado: re-embutir é o conserto


# --- a mira (a promessa da etapa) --------------------------------------------

def test_dado_um_texto_o_indice_aponta_a_nota_certa(embutidor_de_mentira):
    memoria.escrever("Celular Redmi", "o celular tem 6 GB")
    memoria.escrever("ekodide", "o correio manda arquivo")
    memoria.escrever("Mic do Dell", "o microfone tem perfil fixo")
    nomes = [nome for nome, _ in indice.procurar("meu celular travou")]
    assert nomes[0] == "celular-redmi"


def test_o_score_da_nota_e_o_da_melhor_secao(embutidor_de_mentira):
    """O motivo do picadinho: nota grande com um assunto ESCONDIDO numa seção
    ainda é achada por ele — a média borraria, o max mira."""
    memoria.escrever(
        "Diario da maquina",
        "# Diario da maquina\n\n## Rede\nnada de mais\n\n## Sons\no microfone deu pau",
    )
    memoria.escrever("ekodide", "o correio manda arquivo")
    nomes = [nome for nome, _ in indice.procurar("problema no microfone")]
    assert nomes[0] == "diario-da-maquina"


def test_grafo_vazio_devolve_lista_vazia_sem_ollama(embutidor_de_mentira):
    assert indice.procurar("qualquer coisa") == []
    assert embutidor_de_mentira == []  # nem a consulta foi embutida


# --- o servidor fora do ar ----------------------------------------------------

def test_ollama_fora_do_ar_vira_indisponivel_com_receita():
    """Sem dublê: bate na porta morta do conftest e falha na hora — é o caso
    real da máquina (Ollama de usuário, sem serviço, ninguém sobe no boot)."""
    memoria.escrever("Celular Redmi", "specs")
    with pytest.raises(indice.IndiceIndisponivel, match="ollama serve"):
        indice.procurar("meu celular")
