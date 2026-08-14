"""Testes da TUI — o esqueleto, sem subir tela.

O que se prova aqui é o que quebraria CALADO: o contrato da pele (o agente
chama os mesmos métodos da pele simples — método faltando só apareceria com a
TUI aberta) e o CSS (um $nome esquecido no dicionário CORES viraria cor
inválida em tempo de execução). Subir o app de verdade é teste de olho, não de
suite."""
from __future__ import annotations

import re

from mister import interface, tui


def test_cores_cobrem_todos_os_nomes_do_css():
    """Todo $nome usado no desenho existe no dicionário CORES (e o contrário:
    cor sobrando no dicionário é lixo que confunde quem for mexer)."""
    usados = set(re.findall(r"\$([a-z_]+)", tui._CSS))
    assert usados == set(tui.CORES)


def test_css_pronto_nao_deixa_nome_sem_cor():
    assert "$" not in tui._css()


def test_pele_tui_tem_o_mesmo_contrato_da_pele_simples():
    """O agente/__main__ não sabem em qual pele estão falando — a TUI precisa
    responder a TUDO que a pele simples responde."""
    contrato = {
        nome
        for nome in dir(interface.Pele)
        if not nome.startswith("_")
    }
    faltando = contrato - set(dir(tui.PeleTui))
    assert not faltando, f"a PeleTui não tem: {sorted(faltando)}"


def test_blocos_da_conversa_filtra_o_que_nao_e_conversa():
    """Só fala do dono e fala do Mister viram bloco — mensagem de sistema
    (user com '['), jogada de ferramenta e resposta de tool ficam de fora."""
    historico = [
        {"role": "user", "content": "oi"},
        {"role": "user", "content": "[recado do segundo plano] terminou"},
        {"role": "assistant", "content": "vou olhar", "tool_calls": [{"id": "x"}]},
        {"role": "tool", "tool_call_id": "x", "content": "resultado"},
        {"role": "assistant", "content": "olá!"},
    ]
    assert tui.blocos_da_conversa(historico) == [("dono", "oi"), ("mister", "olá!")]


def test_exportar_grava_md_legivel(tmp_path):
    historico = [
        {"role": "user", "content": "oi"},
        {"role": "assistant", "content": "olá!"},
    ]
    caminho = tui.exportar(historico, pasta=str(tmp_path))
    corpo = caminho.read_text(encoding="utf-8")
    assert caminho.suffix == ".md"
    assert "**Dono:** oi" in corpo
    assert "**Mister:** olá!" in corpo


def test_interpretar_barra():
    assert tui.interpretar_barra("oi, tudo bem?") is None
    assert tui.interpretar_barra("!ls") is None
    assert tui.interpretar_barra("/nova") == "nova"
    assert tui.interpretar_barra("/naoexiste") == "naoexiste"  # quem avisa é a tela
    assert tui.interpretar_barra("/") == ""


def test_sem_ajuda_nos_comandos():
    """Decisão do dono (13/08/2026): /ajuda não existe — a paleta cumpre o
    papel. O teste segura a decisão contra regressão."""
    assert "ajuda" not in tui.COMANDOS


def test_importar_tui_nao_exige_textual(monkeypatch):
    """O import do módulo não pode puxar o textual (extra opcional): quem
    importa `mister.tui` sem o extra só quebra — com receita — no `main`."""
    import importlib
    import sys

    monkeypatch.setitem(sys.modules, "textual", None)
    importlib.reload(tui)  # se o topo do módulo importasse textual, estourava
