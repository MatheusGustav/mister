"""Testes do USO DO CONTEXTO — o desenho da barra e o teto por modelo.

Nada aqui toca em rede: o `teto()` só lê o cache, e o `atualizar()` (o único
que sai pra internet) é testado com o urlopen dublado."""
from __future__ import annotations

import json

from mister import contexto


# --- o desenho (função pura) --------------------------------------------------

def test_barra_desenha_as_duas_linhas():
    linhas = contexto.barra(24_100, 64_000, largura=18)
    assert linhas[0].endswith("38%")
    assert linhas[0].count(contexto.CHEIO) + linhas[0].count(contexto.VAZIO) == 18
    assert linhas[1] == "24.1k / 64k tokens"


def test_barra_sem_medida_ainda():
    """Antes da primeira resposta não existe prompt_tokens — a barra não pode
    mentir 0%; ela diz que ainda não mediu."""
    assert len(contexto.barra(0, 64_000)) == 1
    assert "sem medida" in contexto.barra(0, 64_000)[0]


def test_barra_estourada_nao_estoura_o_desenho():
    """Passar do teto trava a barra em cheia, mas a conta segue honesta."""
    linhas = contexto.barra(80_000, 64_000, largura=10)
    assert linhas[0].count(contexto.CHEIO) == 10
    assert contexto.VAZIO not in linhas[0]
    assert "125%" in linhas[0]


def test_curto():
    assert contexto.curto(850) == "850"
    assert contexto.curto(24_100) == "24.1k"
    assert contexto.curto(64_000) == "64k"
    assert contexto.curto(128_000) == "128k"


def test_nivel_vira_cor_de_aviso():
    assert contexto.nivel(10, 100) == "normal"
    assert contexto.nivel(85, 100) == "alto"
    assert contexto.nivel(99, 100) == "critico"
    assert contexto.nivel(10, 0) == "normal"  # teto zoado não vira 'critico'


# --- o teto (cache em arquivo, sem rede) --------------------------------------

def test_teto_sem_cache_e_o_de_reserva_e_avisa():
    total, estimado = contexto.teto("openai/gpt-5.6-luna")
    assert (total, estimado) == (contexto.TETO_PADRAO, True)


def test_teto_do_cache_nao_e_estimado(tmp_path, monkeypatch):
    arquivo = tmp_path / "contexto_modelos.json"
    arquivo.write_text(json.dumps({"x/y": 200_000}), encoding="utf-8")
    monkeypatch.setenv("MISTER_CONTEXTO_MODELOS", str(arquivo))
    assert contexto.teto("x/y") == (200_000, False)
    assert contexto.teto("outro/modelo") == (contexto.TETO_PADRAO, True)


def test_cache_quebrado_nao_derruba(tmp_path, monkeypatch):
    arquivo = tmp_path / "contexto_modelos.json"
    arquivo.write_text("{isto não é json", encoding="utf-8")
    monkeypatch.setenv("MISTER_CONTEXTO_MODELOS", str(arquivo))
    assert contexto.teto("x/y") == (contexto.TETO_PADRAO, True)


def test_atualizar_grava_o_cache(monkeypatch, tmp_path):
    """A resposta da OpenRouter vira {modelo: teto} no arquivo — e entradas
    sem context_length útil ficam de fora."""
    import contextlib
    import io

    corpo = json.dumps({"data": [
        {"id": "a/b", "context_length": 128_000},
        {"id": "c/d", "context_length": None},
        {"id": "e/f"},
    ]}).encode("utf-8")

    @contextlib.contextmanager
    def _falso(req, timeout=0):
        yield io.BytesIO(corpo)

    monkeypatch.setattr(contexto.urllib.request, "urlopen", _falso)
    assert contexto.atualizar("a/b") is True
    assert json.loads((tmp_path / "contexto_modelos.json").read_text()) == {"a/b": 128_000}
    assert contexto.teto("a/b") == (128_000, False)


def test_atualizar_sem_rede_volta_falso_e_nao_quebra(monkeypatch):
    import urllib.error

    def _explode(req, timeout=0):
        raise urllib.error.URLError("sem rede")

    monkeypatch.setattr(contexto.urllib.request, "urlopen", _explode)
    assert contexto.atualizar("a/b") is False
    assert contexto.teto("a/b") == (contexto.TETO_PADRAO, True)


def test_atualizar_nem_sai_de_casa_se_ja_tem_cache(tmp_path, monkeypatch):
    arquivo = tmp_path / "contexto_modelos.json"
    arquivo.write_text(json.dumps({"a/b": 999}), encoding="utf-8")
    monkeypatch.setenv("MISTER_CONTEXTO_MODELOS", str(arquivo))

    def _nao_deveria(req, timeout=0):
        raise AssertionError("foi à rede com o teto já em cache")

    monkeypatch.setattr(contexto.urllib.request, "urlopen", _nao_deveria)
    assert contexto.atualizar("a/b") is True
