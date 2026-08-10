"""A conversa em disco: salvar, retomar e nunca travar o Mister por causa dela.

O conftest aponta MISTER_CONVERSA/MISTER_CONVERSAS pra uma pasta descartável —
nenhum teste chega perto do ~/.mister de verdade.
"""
import json
import os
from pathlib import Path

from mister import conversa


def _limpar_estado():
    """A sessão é estado de módulo (o loop do terminal a liga uma vez). Cada
    teste começa do zero pra não herdar o arquivo do anterior."""
    conversa._sessao = None
    conversa._arquivar = False


def test_ida_e_volta(monkeypatch):
    _limpar_estado()
    hist = [{"role": "user", "content": "oi"}, {"role": "assistant", "content": "olá"}]
    conversa.salvar(hist)
    assert conversa.carregar() == hist


def test_sem_arquivo_comeca_do_zero():
    _limpar_estado()
    assert conversa.carregar() == []


def test_arquivo_corrompido_nao_trava():
    _limpar_estado()
    Path(os.environ["MISTER_CONVERSA"]).write_text("{isso não é json", encoding="utf-8")
    assert conversa.carregar() == []


def test_json_que_nao_e_lista_nao_trava():
    _limpar_estado()
    Path(os.environ["MISTER_CONVERSA"]).write_text('{"role": "user"}', encoding="utf-8")
    assert conversa.carregar() == []


def test_salva_so_a_janela():
    _limpar_estado()
    hist = [{"role": "user", "content": str(i)} for i in range(conversa.MAX_MENSAGENS + 10)]
    conversa.salvar(hist)
    assert len(conversa.carregar()) == conversa.MAX_MENSAGENS


def test_esquecer_apaga():
    _limpar_estado()
    conversa.salvar([{"role": "user", "content": "oi"}])
    conversa.esquecer()
    assert conversa.carregar() == []
    conversa.esquecer()  # de novo: não pode explodir


# --- o corte não pode quebrar o par nativo -----------------------------------

def test_corte_nao_deixa_resposta_de_ferramenta_orfa():
    """Janela começando com {role: tool} é pedido que a API recusa (a chamada
    que ela responde ficou pra trás). O corte empurra pra frente."""
    _limpar_estado()
    hist = [{"role": "user", "content": f"m{i}"} for i in range(conversa.MAX_MENSAGENS)]
    hist.append({"role": "tool", "tool_call_id": "x", "content": "resultado órfão"})
    cortado = conversa.compactar(hist)
    assert cortado[0].get("role") != "tool"


def test_compactar_nao_mexe_em_conversa_curta():
    curta = [{"role": "user", "content": "oi"}]
    assert conversa.compactar(curta) is curta


def test_compactar_corta_conversa_longa():
    longa = [{"role": "user", "content": str(i)} for i in range(conversa.MAX_MENSAGENS * 2)]
    assert len(conversa.compactar(longa)) == conversa.MAX_MENSAGENS


# --- o acervo de sessões (mister -r) -----------------------------------------

def test_sessao_nova_vira_arquivo_no_acervo():
    _limpar_estado()
    conversa.iniciar_sessao()
    conversa.salvar([{"role": "user", "content": "primeira fala"}])
    sessoes = conversa.listar_sessoes()
    assert len(sessoes) == 1
    assert sessoes[0]["resumo"] == "primeira fala"
    assert sessoes[0]["n"] == 1


def test_sessao_retomada_grava_no_mesmo_arquivo():
    _limpar_estado()
    conversa.iniciar_sessao()
    conversa.salvar([{"role": "user", "content": "oi"}])
    caminho = conversa.listar_sessoes()[0]["caminho"]

    _limpar_estado()
    conversa.iniciar_sessao(caminho)  # `mister --continue`
    conversa.salvar([{"role": "user", "content": "oi"}, {"role": "assistant", "content": "olá"}])
    assert len(conversa.listar_sessoes()) == 1  # não duplicou no acervo
    assert conversa.carregar_sessao(caminho)[-1]["content"] == "olá"


def test_sem_arquivar_o_acervo_fica_vazio():
    """Teste (e qualquer uso de biblioteca) não enche o acervo do dono: só o
    loop do terminal liga o arquivamento."""
    _limpar_estado()
    conversa.salvar([{"role": "user", "content": "oi"}])
    assert conversa.listar_sessoes() == []


def test_acervo_inexistente_devolve_lista_vazia():
    _limpar_estado()
    assert conversa.listar_sessoes() == []


def test_sessao_corrompida_e_pulada_sem_derrubar_o_menu():
    _limpar_estado()
    conversa.iniciar_sessao()
    conversa.salvar([{"role": "user", "content": "boa"}])
    pasta = Path(os.environ["MISTER_CONVERSAS"])
    (pasta / "quebrada.json").write_text("{", encoding="utf-8")
    (pasta / "vazia.json").write_text("[]", encoding="utf-8")
    sessoes = conversa.listar_sessoes()
    assert len(sessoes) == 1 and sessoes[0]["resumo"] == "boa"


def test_resumo_longo_e_aparado():
    _limpar_estado()
    conversa.iniciar_sessao()
    conversa.salvar([{"role": "user", "content": "x" * 200}])
    assert len(conversa.listar_sessoes()[0]["resumo"]) == conversa.RESUMO_TETO


def test_carregar_sessao_inexistente_nao_explode():
    assert conversa.carregar_sessao("/nao/existe.json") == []


def test_o_que_e_gravado_e_json_utf8_legivel():
    _limpar_estado()
    conversa.salvar([{"role": "user", "content": "ação com acentuação"}])
    cru = Path(os.environ["MISTER_CONVERSA"]).read_text(encoding="utf-8")
    assert "ação" in cru  # ensure_ascii=False: dá pra ler o arquivo na mão
    assert json.loads(cru)[0]["content"] == "ação com acentuação"
