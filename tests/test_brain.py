"""O cérebro: o que ele faz com a RESPOSTA da API — sem chamar a API real.

Este é o teste que segura o FORMATO: a resposta nativa (tool-calling) vira uma
`Decisao` que o despachante consegue consumir. Se o provedor mudar o formato ou
alguém mexer na tradução, quebra aqui, não em produção.
"""
import io
import json

import pytest

from mister import brain
from mister.dispatcher import despachar

import mister.tools.basic  # noqa: F401  (cadastra as tools no registro)


# --- configuração (sem rede) --------------------------------------------------

def test_fabrica_devolve_cerebro_api(monkeypatch):
    monkeypatch.setenv("MISTER_API_KEY", "chave-de-teste")
    assert isinstance(brain.criar_cerebro(), brain.Cerebro)


def test_sem_chave_reclama_com_jeito(monkeypatch):
    monkeypatch.delenv("MISTER_API_KEY", raising=False)
    with pytest.raises(RuntimeError, match="MISTER_API_KEY"):
        brain.criar_cerebro()


def test_env_escolhe_o_modelo(monkeypatch):
    monkeypatch.setenv("MISTER_API_KEY", "chave-de-teste")
    monkeypatch.setenv("MISTER_API_MODELO", "algum/modelo-com-tools")
    assert brain.Cerebro().modelo == "algum/modelo-com-tools"


def test_campo_pensar_fala_o_dialeto_do_provedor(monkeypatch):
    monkeypatch.setattr(brain, "API_URL", "https://openrouter.ai/api/v1/chat/completions")
    assert brain._campo_pensar(True) == {"reasoning": {"enabled": True}}
    monkeypatch.setattr(brain, "API_URL", "https://api.exemplo.com/v1/chat/completions")
    assert brain._campo_pensar(True) == {"thinking": {"type": "enabled"}}


# --- tradução da resposta NATIVA (tool-calling) -------------------------------

class _CerebroDeMentira(brain._CerebroBase):
    """Motor falso: devolve a MENSAGEM crua que o teste mandar."""

    def __init__(self, mensagem):
        self._mensagem = mensagem
        self.pedidos = []

    def _chamar(self, mensagens, tools):
        self.pedidos.append({"mensagens": mensagens, "tools": tools})
        return self._mensagem


def test_chamada_nativa_vira_lote_de_uma_decisao():
    c = _CerebroDeMentira({
        "content": "vou ver a hora",
        "tool_calls": [{
            "id": "call_abc",
            "type": "function",
            "function": {"name": "que_horas_sao", "arguments": "{}"},
        }],
    })
    lote = c.proximo_passo([{"role": "user", "content": "que horas são?"}])
    assert len(lote) == 1
    d = lote[0]
    assert d.intencao == "que_horas_sao"
    assert d.params == {}
    assert d.raciocinio == "vou ver a hora"   # o content junto é a narração
    assert d.id_chamada == "call_abc"


def test_argumentos_viram_params():
    c = _CerebroDeMentira({
        "content": "",
        "tool_calls": [{
            "id": "x",
            "function": {"name": "enviar_para_celular", "arguments": '{"caminho": "~/a.pdf"}'},
        }],
    })
    assert c.proximo_passo([])[0].params == {"caminho": "~/a.pdf"}


def test_texto_puro_e_a_resposta_final():
    c = _CerebroDeMentira({"content": "oi! tudo certo por aqui."})
    lote = c.proximo_passo([{"role": "user", "content": "oi"}])
    assert len(lote) == 1
    assert lote[0].intencao == "responder"
    assert lote[0].params == {"mensagem": "oi! tudo certo por aqui."}


def test_resposta_vazia_vira_none():
    # Nem chamada nem texto (raro): o cinto de sempre — None, nunca explosão.
    c = _CerebroDeMentira({"content": ""})
    lote = c.proximo_passo([{"role": "user", "content": "oi"}])
    assert len(lote) == 1 and lote[0].intencao is None


def test_arguments_malformado_nao_explode():
    # JSON quebrado nos arguments vira params vazio — a validação do despachante
    # reprova e o laço corrige; nunca estoura exceção aqui.
    c = _CerebroDeMentira({
        "content": "",
        "tool_calls": [{"id": "x", "function": {"name": "que_horas_sao", "arguments": '{"a": '}}],
    })
    d = c.proximo_passo([])[0]
    assert d.intencao == "que_horas_sao" and d.params == {}


def test_arguments_que_nao_e_objeto_vira_params_vazio():
    c = _CerebroDeMentira({
        "content": "",
        "tool_calls": [{"id": "x", "function": {"name": "que_horas_sao", "arguments": "[1,2]"}}],
    })
    assert c.proximo_passo([])[0].params == {}


def test_lote_com_ate_4_chamadas_vira_uma_decisao_cada():
    # Ações independentes podem vir juntas na mesma resposta — até 4.
    c = _CerebroDeMentira({
        "content": "vou fazer as duas coisas",
        "tool_calls": [
            {"id": "a", "function": {"name": "que_horas_sao", "arguments": "{}"}},
            {"id": "b", "function": {"name": "outra", "arguments": '{"x": 1}'}},
        ],
    })
    lote = c.proximo_passo([])
    assert [d.intencao for d in lote] == ["que_horas_sao", "outra"]
    assert [d.id_chamada for d in lote] == ["a", "b"]
    assert lote[1].params == {"x": 1}
    # a narração é UMA só pra resposta inteira — repetida em cada decisão do lote
    assert all(d.raciocinio == "vou fazer as duas coisas" for d in lote)


def test_lote_com_mais_de_4_chamadas_devolve_todas():
    # Quem CORTA em 4 é o agente (ver test_agente.py); o cérebro só traduz.
    c = _CerebroDeMentira({
        "content": "",
        "tool_calls": [
            {"id": str(i), "function": {"name": "que_horas_sao", "arguments": "{}"}}
            for i in range(6)
        ],
    })
    assert len(c.proximo_passo([])) == 6


def test_o_cerebro_recebe_as_fichas_do_registro():
    c = _CerebroDeMentira({"content": "oi"})
    c.proximo_passo([{"role": "user", "content": "oi"}])
    nomes = [f["function"]["name"] for f in c.pedidos[0]["tools"]]
    assert "que_horas_sao" in nomes and "perguntar" in nomes


def test_a_decisao_do_cerebro_serve_de_entrada_pro_despachante():
    """O CONTRATO entre as duas peças: o despachante não importa o cérebro, então
    é este teste que garante que a `Decisao` tem a forma que ele espera."""
    c = _CerebroDeMentira({
        "content": "",
        "tool_calls": [{"id": "x", "function": {"name": "que_horas_sao", "arguments": "{}"}}],
    })
    (decisao,) = c.proximo_passo([{"role": "user", "content": "horas"}])
    saida = despachar(decisao)
    assert saida.ok


# --- o pedido que sai (sem rede) ---------------------------------------------

class _Resposta(io.BytesIO):
    def __enter__(self):
        return self

    def __exit__(self, *a):
        return False


def _api_de_mentira(monkeypatch, mensagem: dict) -> list[dict]:
    """Intercepta o urlopen e guarda o PEDIDO que teria ido pra rede."""
    enviados: list[dict] = []

    def _urlopen(req, timeout=0):
        enviados.append(json.loads(req.data.decode("utf-8")))
        return _Resposta(json.dumps({"choices": [{"message": mensagem}]}).encode())

    monkeypatch.setattr(brain.urllib.request, "urlopen", _urlopen)
    return enviados


def test_o_pedido_leva_instrucao_tools_e_lote_ligado(monkeypatch):
    monkeypatch.setenv("MISTER_API_KEY", "chave-de-teste")
    enviados = _api_de_mentira(monkeypatch, {"content": "oi"})
    brain.Cerebro().proximo_passo([{"role": "user", "content": "oi"}])
    pedido = enviados[0]
    assert pedido["messages"][0]["role"] == "system"
    assert pedido["parallel_tool_calls"] is True  # até 4 chamadas por resposta
    assert pedido["temperature"] == 0.0      # roteamento repetível
    assert pedido["tools"]                    # tool-calling nativo, sempre


def test_http_de_chave_falha_na_hora_sem_insistir(monkeypatch):
    monkeypatch.setenv("MISTER_API_KEY", "chave-errada")
    tentativas = []

    def _urlopen(req, timeout=0):
        tentativas.append(1)
        raise brain.urllib.error.HTTPError(req.full_url, 401, "não autorizado", {}, None)

    monkeypatch.setattr(brain.urllib.request, "urlopen", _urlopen)
    with pytest.raises(brain.ErroCerebro, match="MISTER_API_KEY"):
        brain.Cerebro().proximo_passo([{"role": "user", "content": "oi"}])
    assert len(tentativas) == 1  # 401 não passa sozinho: não re-tenta


def test_limite_e_re_tentado(monkeypatch):
    monkeypatch.setenv("MISTER_API_KEY", "chave-de-teste")
    monkeypatch.setattr(brain, "ESPERA_RETRY_S", 0)  # teste não espera de verdade
    monkeypatch.setattr(brain.time, "sleep", lambda _: None)
    tentativas = []

    def _urlopen(req, timeout=0):
        tentativas.append(1)
        if len(tentativas) < 3:
            raise brain.urllib.error.HTTPError(req.full_url, 429, "calma", {}, None)
        return _Resposta(json.dumps({"choices": [{"message": {"content": "oi"}}]}).encode())

    monkeypatch.setattr(brain.urllib.request, "urlopen", _urlopen)
    d = brain.Cerebro().proximo_passo([{"role": "user", "content": "oi"}])[0]
    assert d.intencao == "responder" and len(tentativas) == 3


def test_rede_caida_vira_erro_cerebro_com_jeito(monkeypatch):
    monkeypatch.setenv("MISTER_API_KEY", "chave-de-teste")
    monkeypatch.setattr(brain.time, "sleep", lambda _: None)

    def _urlopen(req, timeout=0):
        raise brain.urllib.error.URLError("sem rota até o host")

    monkeypatch.setattr(brain.urllib.request, "urlopen", _urlopen)
    with pytest.raises(brain.ErroCerebro, match="rede"):
        brain.Cerebro().proximo_passo([])
