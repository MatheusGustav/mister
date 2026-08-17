"""A internet do Mister: o transporte da Exa e as duas tools que o usam.

Nenhum teste aqui toca a rede — o `exa.chamar` é dublado. O que se prova é o
que quebra na vida real: o embrulho SSE virar texto, a falha virar RECUSA (não
exceção), o botão do dono ser obedecido e os tetos cortarem.
"""
from __future__ import annotations

import pytest

from mister import exa, interruptores
from mister.registry import REGISTRO
from mister.tools.internet import (
    URLS_MAX,
    AbrirPaginaParams,
    PesquisarWebParams,
    abrir_pagina,
    pesquisar_web,
)


def _sse(payload: str) -> str:
    """O corpo como a Exa manda: um quadro SSE, não JSON puro."""
    return f"event: message\ndata: {payload}\n\n"


# --- o transporte: tirar o embrulho -------------------------------------------

def test_decodificar_tira_o_prefixo_data_do_sse():
    achado = exa._decodificar(_sse('{"result":{"content":[{"type":"text","text":"oi"}]}}'))
    assert achado["result"]["content"][0]["text"] == "oi"


def test_decodificar_aceita_json_puro_tambem():
    assert exa._decodificar('{"result":{"ok":1}}')["result"] == {"ok": 1}


def test_decodificar_pula_quadro_que_nao_e_a_resposta():
    corpo = "event: ping\ndata: {\"jsonrpc\":\"2.0\"}\n" + _sse('{"result":{"content":[]}}')
    assert "result" in exa._decodificar(corpo)


def test_decodificar_de_lixo_vira_erro_explicado():
    with pytest.raises(exa.ErroExa):
        exa._decodificar("<html>página de erro do proxy</html>")


def test_texto_junta_os_blocos_de_texto():
    resposta = {"result": {"content": [
        {"type": "text", "text": "primeira"},
        {"type": "text", "text": "segunda"},
    ]}}
    assert exa._texto_do_resultado(resposta) == "primeira\nsegunda"


def test_erro_jsonrpc_vira_erroexa():
    with pytest.raises(exa.ErroExa, match="recusou"):
        exa._texto_do_resultado({"error": {"message": "ferramenta desconhecida"}})


def test_iserror_vira_erroexa_mesmo_com_http_200():
    resposta = {"result": {"isError": True,
                           "content": [{"type": "text", "text": "URL inválida"}]}}
    with pytest.raises(exa.ErroExa, match="URL inválida"):
        exa._texto_do_resultado(resposta)


def test_o_pedido_leva_user_agent_proprio(monkeypatch):
    """A Exa devolve 403 pro 'Python-urllib/3.x' padrão — quem tirar este
    cabeçalho quebra a internet inteira do Mister e o teste avisa antes."""
    import urllib.request

    class _Resp:
        def read(self):
            return _sse('{"result":{"content":[{"type":"text","text":"ok"}]}}').encode()

        def __enter__(self):
            return self

        def __exit__(self, *_):
            return False

    vistos = {}

    def _fingir(req, timeout=None):
        vistos.update(req.headers)
        return _Resp()

    monkeypatch.setattr(urllib.request, "urlopen", _fingir)
    assert exa.chamar("web_search_exa", {"query": "x"}) == "ok"
    # urllib normaliza o nome do cabeçalho pra 'User-agent'.
    assert "urllib" not in vistos.get("User-agent", "Python-urllib/3")


# --- as tools: cadastro e formulário ------------------------------------------

def test_as_duas_tools_estao_no_registro():
    assert "pesquisar_web" in REGISTRO and "abrir_pagina" in REGISTRO


def test_formulario_recusa_campo_inventado():
    from pydantic import ValidationError

    with pytest.raises(ValidationError):
        PesquisarWebParams(busca="x", inventado=1)


# --- pesquisar_web -------------------------------------------------------------

def test_pesquisa_devolve_o_texto_da_exa(monkeypatch):
    monkeypatch.setattr(exa, "chamar", lambda f, a: f"achados de {a['query']}")
    saida = pesquisar_web(PesquisarWebParams(busca="  preço do  dólar "))
    assert saida.ok and saida.mensagem == "achados de preço do dólar"


def test_pesquisa_corta_a_quantidade_no_teto(monkeypatch):
    vistos = {}
    monkeypatch.setattr(exa, "chamar", lambda f, a: vistos.update(a) or "ok")
    pesquisar_web(PesquisarWebParams(busca="x", quantidade=999))
    assert vistos["numResults"] == 10


def test_pesquisa_vazia_recusa_sem_ir_na_rede(monkeypatch):
    def _nao_chama(*_):
        raise AssertionError("não podia ter ido na rede")

    monkeypatch.setattr(exa, "chamar", _nao_chama)
    assert pesquisar_web(PesquisarWebParams(busca="   ")).ok is False


def test_rede_fora_vira_recusa_e_nao_excecao(monkeypatch):
    def _cai(*_):
        raise exa.ErroExa("não cheguei na Exa — confira a rede (timeout).")

    monkeypatch.setattr(exa, "chamar", _cai)
    saida = pesquisar_web(PesquisarWebParams(busca="x"))
    assert saida.ok is False and "rede" in saida.mensagem


# --- abrir_pagina --------------------------------------------------------------

def test_abrir_manda_todas_as_urls_numa_chamada_so(monkeypatch):
    vistos = {}
    monkeypatch.setattr(exa, "chamar", lambda f, a: vistos.update(a) or "página")
    saida = abrir_pagina(AbrirPaginaParams(urls=["https://a.com", "https://b.com"]))
    assert saida.ok and vistos["urls"] == ["https://a.com", "https://b.com"]


def test_abrir_recusa_endereco_que_nao_e_http(monkeypatch):
    def _nao_chama(*_):
        raise AssertionError("não podia ter ido na rede")

    monkeypatch.setattr(exa, "chamar", _nao_chama)
    saida = abrir_pagina(AbrirPaginaParams(urls=["file:///etc/passwd"]))
    assert saida.ok is False and "http" in saida.mensagem


def test_abrir_corta_no_teto_de_urls_e_avisa_quais_ficaram_de_fora(monkeypatch):
    vistos = {}
    monkeypatch.setattr(exa, "chamar", lambda f, a: vistos.update(a) or "página")
    urls = [f"https://s{n}.com" for n in range(URLS_MAX + 2)]
    saida = abrir_pagina(AbrirPaginaParams(urls=urls))
    assert len(vistos["urls"]) == URLS_MAX
    assert f"https://s{URLS_MAX}.com" in saida.sugestao


def test_abrir_sem_url_nenhuma_recusa():
    assert abrir_pagina(AbrirPaginaParams(urls=["  "])).ok is False


# --- o botão do dono -----------------------------------------------------------

def test_internet_desligada_recusa_as_duas_sem_ir_na_rede(monkeypatch):
    def _nao_chama(*_):
        raise AssertionError("não podia ter ido na rede")

    monkeypatch.setattr(exa, "chamar", _nao_chama)
    interruptores.alternar("internet")
    for saida in (
        pesquisar_web(PesquisarWebParams(busca="x")),
        abrir_pagina(AbrirPaginaParams(urls=["https://a.com"])),
    ):
        assert saida.ok is False and "DESLIGADA" in saida.mensagem
