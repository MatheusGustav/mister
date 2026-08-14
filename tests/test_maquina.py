"""As tools de máquina: rodar comando, ler/escrever/procurar arquivo local.

Nenhum teste sobe processo perigoso de verdade nem espera timeout de verdade
(o subprocess é real só pra comando trivial/rápido; o timeout é dublado). O que
está sob teste: a saída do comando chega estruturada, o critério de
confirmação do item 3 se aplica certo a rodar_comando, e a trava de ler-antes
do item 5 se aplica certo a escrever_arquivo.
"""
from dataclasses import dataclass, field

import pytest

from mister import leituras
from mister.confirmacao import Pendente
from mister.dispatcher import despachar
from mister.tools import maquina

import mister.tools.maquina  # noqa: F401  (cadastra as tools no registro)


@dataclass
class _Decisao:
    intencao: str | None
    params: dict = field(default_factory=dict)
    aval_do_dono: bool = False
    carimbo: str = ""


# --- rodar_comando -------------------------------------------------------------

def test_roda_um_comando_simples_e_devolve_a_saida():
    saida = despachar(_Decisao("rodar_comando", {"comando": "echo oi"}))
    assert saida.ok and "oi" in saida.mensagem


def test_comando_vazio_e_recusado():
    saida = despachar(_Decisao("rodar_comando", {"comando": "   "}))
    assert not saida.ok


def test_comando_que_falha_devolve_ok_false_com_a_saida():
    saida = despachar(_Decisao("rodar_comando", {"comando": "exit 3"}))
    assert not saida.ok


def test_saida_grande_e_cortada(monkeypatch):
    monkeypatch.setattr(maquina, "SAIDA_COMANDO_MAX", 10)
    saida = despachar(_Decisao("rodar_comando", {"comando": "echo 0123456789999999"}))
    assert saida.ok and len(saida.mensagem) <= 10
    assert "cortada" in saida.sugestao


def test_comando_que_estoura_o_tempo_e_cortado(monkeypatch):
    import subprocess

    def _sempre_estoura(*a, **k):
        raise subprocess.TimeoutExpired(cmd="sleep 999", timeout=maquina.TIMEOUT_COMANDO_S)

    monkeypatch.setattr(subprocess, "run", _sempre_estoura)
    saida = despachar(_Decisao("rodar_comando", {"comando": "sleep 999"}))
    assert not saida.ok
    assert "passou de" in saida.mensagem


# --- rodar_comando: a confirmação do item 3 -----------------------------------

def test_comando_inofensivo_roda_direto():
    saida = despachar(_Decisao("rodar_comando", {"comando": "ls"}))
    assert not isinstance(saida, Pendente)


def test_comando_destrutivo_pede_confirmacao(tmp_path):
    saida = despachar(_Decisao("rodar_comando", {"comando": f"rm -rf {tmp_path}/x"}))
    assert isinstance(saida, Pendente)


def test_comando_destrutivo_confirmado_roda(tmp_path):
    alvo = tmp_path / "lixo.txt"
    alvo.write_text("x", encoding="utf-8")
    pendente = despachar(_Decisao("rodar_comando", {"comando": f"rm {alvo}"}))
    saida = despachar(_Decisao(
        pendente.intencao, pendente.params, aval_do_dono=True, carimbo=pendente.carimbo,
    ))
    assert saida.ok
    assert not alvo.exists()


# --- ler_arquivo -----------------------------------------------------------

def test_le_o_arquivo_inteiro_e_marca_como_lido(tmp_path):
    alvo = tmp_path / "notas.txt"
    alvo.write_text("linha 1\nlinha 2\n" * 5000, encoding="utf-8")  # sem limite de linhas
    saida = despachar(_Decisao("ler_arquivo", {"caminho": str(alvo)}))
    assert saida.ok
    assert saida.mensagem.count("linha 1") == 5000
    assert leituras.foi_lido(alvo)


def test_ler_arquivo_inexistente_e_recusado(tmp_path):
    saida = despachar(_Decisao("ler_arquivo", {"caminho": str(tmp_path / "nao-existe.txt")}))
    assert not saida.ok


def test_ler_arquivo_binario_e_recusado(tmp_path):
    alvo = tmp_path / "bin.dat"
    alvo.write_bytes(b"\xff\xfe\x00\x01binario")
    saida = despachar(_Decisao("ler_arquivo", {"caminho": str(alvo)}))
    assert not saida.ok


# --- escrever_arquivo: a trava de ler-antes -----------------------------------

def test_escreve_arquivo_novo_sem_precisar_ler(tmp_path):
    alvo = tmp_path / "novo.txt"
    saida = despachar(_Decisao("escrever_arquivo", {"caminho": str(alvo), "conteudo": "oi"}))
    assert saida.ok
    assert alvo.read_text(encoding="utf-8") == "oi"


def test_cria_pastas_no_meio_do_caminho(tmp_path):
    alvo = tmp_path / "a" / "b" / "c.txt"
    saida = despachar(_Decisao("escrever_arquivo", {"caminho": str(alvo), "conteudo": "x"}))
    assert saida.ok and alvo.exists()


def test_sobrescrever_sem_ler_antes_e_recusado(tmp_path):
    alvo = tmp_path / "existe.txt"
    alvo.write_text("original", encoding="utf-8")
    saida = despachar(_Decisao("escrever_arquivo", {"caminho": str(alvo), "conteudo": "novo"}))
    assert not saida.ok
    assert "ler_arquivo" in saida.sugestao
    assert alvo.read_text(encoding="utf-8") == "original"


def test_ler_e_depois_sobrescrever_funciona(tmp_path):
    alvo = tmp_path / "existe.txt"
    alvo.write_text("original", encoding="utf-8")
    despachar(_Decisao("ler_arquivo", {"caminho": str(alvo)}))
    saida = despachar(_Decisao("escrever_arquivo", {"caminho": str(alvo), "conteudo": "novo"}))
    assert saida.ok
    assert alvo.read_text(encoding="utf-8") == "novo"


def test_escrever_arquivo_nao_pede_confirmacao(tmp_path):
    alvo = tmp_path / "existe.txt"
    alvo.write_text("original", encoding="utf-8")
    despachar(_Decisao("ler_arquivo", {"caminho": str(alvo)}))
    saida = despachar(_Decisao("escrever_arquivo", {"caminho": str(alvo), "conteudo": "novo"}))
    assert not isinstance(saida, Pendente)


# --- procurar_arquivo ---------------------------------------------------------

def test_procura_por_nome(tmp_path):
    (tmp_path / "celular-redmi.md").write_text("x", encoding="utf-8")
    (tmp_path / "outra-nota.md").write_text("x", encoding="utf-8")
    saida = despachar(_Decisao("procurar_arquivo", {"nome": "redmi", "pasta": str(tmp_path)}))
    assert saida.ok and "celular-redmi.md" in saida.mensagem
    assert "outra-nota.md" not in saida.mensagem


def test_procura_por_conteudo(tmp_path):
    (tmp_path / "a.txt").write_text("a chave da API é 123", encoding="utf-8")
    (tmp_path / "b.txt").write_text("nada a ver", encoding="utf-8")
    saida = despachar(_Decisao("procurar_arquivo", {"conteudo": "chave da API", "pasta": str(tmp_path)}))
    assert saida.ok and "a.txt" in saida.mensagem and "b.txt" not in saida.mensagem


def test_sem_nome_e_sem_conteudo_e_recusado(tmp_path):
    saida = despachar(_Decisao("procurar_arquivo", {"pasta": str(tmp_path)}))
    assert not saida.ok


def test_pasta_inexistente_e_recusada():
    saida = despachar(_Decisao("procurar_arquivo", {"nome": "x", "pasta": "/nao/existe/de/jeito/nenhum"}))
    assert not saida.ok


def test_nada_achado_nao_e_erro(tmp_path):
    saida = despachar(_Decisao("procurar_arquivo", {"nome": "fantasma", "pasta": str(tmp_path)}))
    assert saida.ok and "Não achei" in saida.mensagem


def test_ignora_pastas_pesadas(tmp_path):
    pesada = tmp_path / "node_modules"
    pesada.mkdir()
    (pesada / "achavel.txt").write_text("x", encoding="utf-8")
    saida = despachar(_Decisao("procurar_arquivo", {"nome": "achavel", "pasta": str(tmp_path)}))
    assert saida.ok and "Não achei" in saida.mensagem


def test_para_no_teto_de_resultados(tmp_path, monkeypatch):
    monkeypatch.setattr(maquina, "RESULTADOS_MAX", 2)
    for i in range(5):
        (tmp_path / f"achado-{i}.txt").write_text("x", encoding="utf-8")
    saida = despachar(_Decisao("procurar_arquivo", {"nome": "achado", "pasta": str(tmp_path)}))
    assert saida.ok
    assert len(saida.mensagem.splitlines()) == 2
    assert "refine" in saida.sugestao
