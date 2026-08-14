"""A trava de ler-antes: o registro puro, sem tool nem despachante no meio.

O que está sob teste: marcar registra, foi_lido confere — e a marca não bate
mais se o arquivo mudou por fora depois da leitura (mtime+tamanho).
"""
from mister import leituras


def test_nao_lido_e_false(tmp_path):
    alvo = tmp_path / "nota.md"
    alvo.write_text("x", encoding="utf-8")
    assert leituras.foi_lido(alvo) is False


def test_marcar_e_depois_foi_lido_e_true(tmp_path):
    alvo = tmp_path / "nota.md"
    alvo.write_text("x", encoding="utf-8")
    leituras.marcar(alvo)
    assert leituras.foi_lido(alvo) is True


def test_arquivo_mudado_depois_da_leitura_invalida_a_marca(tmp_path):
    alvo = tmp_path / "nota.md"
    alvo.write_text("x", encoding="utf-8")
    leituras.marcar(alvo)
    alvo.write_text("outro conteúdo, tamanho diferente", encoding="utf-8")
    assert leituras.foi_lido(alvo) is False


def test_arquivo_sumido_nunca_conta_como_lido(tmp_path):
    alvo = tmp_path / "nota.md"
    alvo.write_text("x", encoding="utf-8")
    leituras.marcar(alvo)
    alvo.unlink()
    assert leituras.foi_lido(alvo) is False


def test_marcar_arquivo_inexistente_nao_estoura(tmp_path):
    alvo = tmp_path / "nunca-existiu.md"
    leituras.marcar(alvo)  # não explode
    assert leituras.foi_lido(alvo) is False


def test_esquecer_tudo_zera_o_registro(tmp_path):
    alvo = tmp_path / "nota.md"
    alvo.write_text("x", encoding="utf-8")
    leituras.marcar(alvo)
    leituras.esquecer_tudo()
    assert leituras.foi_lido(alvo) is False
