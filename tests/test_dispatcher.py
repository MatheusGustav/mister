"""O despachante: VALIDA antes de executar, e guarda o portão da confirmação.

Estes testes não sobem cérebro nenhum — o despachante recebe a FORMA de uma
decisão, não o `brain.Decisao` (por isso o dublê abaixo). Que o cérebro de
verdade produz essa mesma forma é o que o test_brain segura.
"""
from dataclasses import dataclass, field

import pytest

from mister import dispatcher
from mister.confirmacao import Pendente, carimbar
from mister.dispatcher import despachar
from mister.registry import REGISTRO, Formulario, tool
from mister.resultado import Resultado

import mister.tools.basic  # noqa: F401  (cadastra as tools no registro)


@dataclass
class _Decisao:
    """A FORMA que o despachante consome (o mesmo contrato do brain.Decisao)."""

    intencao: str | None
    params: dict = field(default_factory=dict)
    aval_do_dono: bool = False
    carimbo: str = ""


class _MexeNoDiscoParams(Formulario):
    alvo: str
    pasta: str = "raiz"  # campo com DEFAULT: o cérebro pode mandar null nele
    # Sem 'confirmado' aqui de propósito: CAMPO INTERNO não mora no formulário
    # (ver confirmacao.CAMPOS_INTERNOS) — o despachante o descarta antes de
    # validar, e com o `extra="forbid"` da base ele seria erro se sobrasse.


@pytest.fixture(autouse=True)
def tool_que_confirma(monkeypatch):
    """Uma tool de mentira que só age com aval — o cenário do puxar_do_celular,
    sem rede nenhuma no meio.

    Quem decide que ela pede aval é o DESPACHANTE, não a tool (o desenho de
    hoje: `pergunta_de_confirmacao` olha intenção+params e a tool só executa) —
    por isso o critério entra por monkeypatch em vez de um `raise` no handler."""
    antes = dict(REGISTRO)

    @tool("mexer_no_disco", _MexeNoDiscoParams, "tool de teste que pede aval")
    def _handler(params: _MexeNoDiscoParams) -> Resultado:
        return Resultado(True, f"mexi em {params.alvo} ({params.pasta})")

    def _criterio(intencao: str, params: dict) -> str | None:
        if intencao == "mexer_no_disco":
            return f"Posso mexer em {params.get('alvo')}?"
        return None

    monkeypatch.setattr(dispatcher, "pergunta_de_confirmacao", _criterio)
    yield
    REGISTRO.clear()
    REGISTRO.update(antes)


# --- validar -> executar -----------------------------------------------------

def test_despacha_a_tool_do_encanamento():
    out = despachar(_Decisao("que_horas_sao", {}))
    assert out.ok and "são" in out.mensagem.lower()


def test_intencao_desconhecida_recusa_sem_explodir():
    out = despachar(_Decisao("voar_ate_marte", {}))
    assert out.ok is False and "desconhecida" in out.mensagem.lower()


def test_intencao_none_recusa():
    out = despachar(_Decisao(None, {}))
    assert out.ok is False


def test_validacao_barra_params_faltando():
    # sem 'alvo' o formulário não bate — e o que não bate NÃO executa.
    out = despachar(_Decisao("mexer_no_disco", {}))
    assert out.ok is False and "inválid" in out.mensagem.lower()


def test_param_inventado_pelo_modelo_nao_vira_acao():
    # Nome trocado: falta o obrigatório E sobra um inventado — não executa.
    out = despachar(_Decisao("mexer_no_disco", {"pasta": "~/x"}))
    assert out.ok is False


def test_campo_inventado_junto_do_certo_e_recusado():
    """O `extra="forbid"` da base: antes, um campo que o modelo inventou passava
    IGNORADO EM SILÊNCIO e a ação rodava assim mesmo. Agora vira recusa, e a
    explicação volta pro cérebro em vez de o chute sumir sem rastro."""
    out = despachar(_Decisao("mexer_no_disco", {"alvo": "~/x", "inventado": 123}))
    assert out.ok is False and "inválid" in out.mensagem.lower()


def test_campo_nulo_e_descartado_pro_default_valer():
    """No strict TODO campo é obrigatório e "não preenchi" chega como null (ver
    prompts._schema_params). Sem descartar, 'pasta': None quebraria o `str` do
    formulário; descartando, o default entra."""
    out = despachar(_Decisao("mexer_no_disco", {"alvo": "~/x", "pasta": None},
                             aval_do_dono=True,
                             carimbo=carimbar("mexer_no_disco",
                                              {"alvo": "~/x", "pasta": None})))
    assert out.ok and "(raiz)" in out.mensagem


# --- a tranca anti-furo (campo interno) --------------------------------------

def test_tool_que_pede_aval_devolve_pendente():
    out = despachar(_Decisao("mexer_no_disco", {"alvo": "~/x"}))
    assert isinstance(out, Pendente)
    assert out.intencao == "mexer_no_disco"
    assert out.params["confirmado"] is True   # já engatilhada pro loop repetir
    assert out.carimbo


def test_modelo_nao_fura_a_confirmacao_preenchendo_campo_interno():
    """O furo clássico: o modelo manda confirmado=True sozinho. O despachante
    DESCARTA campos com default quando não há aval do dono — vira Pendente."""
    out = despachar(_Decisao("mexer_no_disco", {"alvo": "~/x", "confirmado": True}))
    assert isinstance(out, Pendente)


# --- o carimbo anti-drift ----------------------------------------------------

def test_aval_com_carimbo_certo_executa():
    pendente = despachar(_Decisao("mexer_no_disco", {"alvo": "~/x"}))
    out = despachar(_Decisao(
        pendente.intencao, pendente.params, aval_do_dono=True, carimbo=pendente.carimbo
    ))
    assert out.ok and "mexi" in out.mensagem


def test_aval_sem_carimbo_e_negado():
    out = despachar(_Decisao(
        "mexer_no_disco", {"alvo": "~/x", "confirmado": True}, aval_do_dono=True
    ))
    assert out.ok is False and "cancelei por segurança" in out.mensagem


def test_params_trocados_depois_do_sim_sao_negados():
    pendente = despachar(_Decisao("mexer_no_disco", {"alvo": "~/x"}))
    adulterado = {**pendente.params, "alvo": "~/OUTRO"}
    out = despachar(_Decisao(
        pendente.intencao, adulterado, aval_do_dono=True, carimbo=pendente.carimbo
    ))
    assert out.ok is False and "cancelei por segurança" in out.mensagem


def test_arquivo_trocado_entre_o_sim_e_o_rodar_e_negado(tmp_path):
    alvo = tmp_path / "nota.txt"
    alvo.write_text("antes", encoding="utf-8")
    pendente = despachar(_Decisao("mexer_no_disco", {"alvo": str(alvo)}))
    alvo.write_text("DEPOIS — outro conteúdo", encoding="utf-8")
    out = despachar(_Decisao(
        pendente.intencao, pendente.params, aval_do_dono=True, carimbo=pendente.carimbo
    ))
    assert out.ok is False and "cancelei por segurança" in out.mensagem


def test_carimbo_de_material_igual_e_igual():
    assert carimbar("x", {"a": 1}) == carimbar("x", {"a": 1})
    assert carimbar("x", {"a": 1}) != carimbar("x", {"a": 2})
