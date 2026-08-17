"""As FICHAS que o cérebro lê — o formato STRICT, campo por campo.

Por que isto merece teste próprio: o provedor VALIDA o schema quando `strict`
está ligado (conferido em 16/08/2026 — Azure/OpenAI via OpenRouter). Ficha fora
das regras não é "ganho que não veio": é HTTP 400 em TODO turno, o Mister
inteiro parado. Um campo novo declarado errado quebraria tudo, e é isso que
estes testes pegam antes de a rede pegar.
"""
from __future__ import annotations

import pytest

from mister.confirmacao import CAMPOS_INTERNOS
from mister.prompts import _schema_params, montar_tools
from mister.registry import Formulario

import mister.tools.basic  # noqa: F401  (cadastram as tools no registro)
import mister.tools.correio  # noqa: F401
import mister.tools.internet  # noqa: F401
import mister.tools.maquina  # noqa: F401
import mister.tools.memoria  # noqa: F401
import mister.tools.regras  # noqa: F401


class _Exemplo(Formulario):
    obrigatorio: str
    com_default: str = "x"
    numero: int = 3
    confirmado: bool = False  # CAMPO INTERNO: não pode aparecer na ficha


# --- as três regras do strict, em TODA ficha ----------------------------------

@pytest.mark.parametrize("ficha", montar_tools(), ids=lambda f: f["function"]["name"])
def test_toda_ficha_segue_as_regras_do_strict(ficha):
    funcao = ficha["function"]
    parametros = funcao["parameters"]
    assert funcao["strict"] is True
    assert parametros["additionalProperties"] is False
    # No strict não existe campo ausente: required tem TODOS, na mesma ordem.
    assert parametros["required"] == list(parametros["properties"])


def test_a_lista_tem_as_tools_do_registro_mais_o_perguntar():
    nomes = [f["function"]["name"] for f in montar_tools()]
    assert "perguntar" in nomes and "pesquisar_web" in nomes
    assert len(nomes) == len(set(nomes))


# --- opcional vira "aceita null", não "fica de fora" ---------------------------

def test_campo_obrigatorio_nao_aceita_nulo():
    assert _schema_params(_Exemplo)["properties"]["obrigatorio"]["type"] == "string"


def test_campo_com_default_aceita_nulo():
    props = _schema_params(_Exemplo)["properties"]
    assert props["com_default"]["type"] == ["string", "null"]
    assert props["numero"]["type"] == ["integer", "null"]


def test_campo_interno_continua_escondido_do_cerebro():
    schema = _schema_params(_Exemplo)
    for interno in CAMPOS_INTERNOS:
        assert interno not in schema["properties"]
        assert interno not in schema["required"]


def test_o_default_nao_vaza_pra_ficha():
    """Default é assunto do formulário, não do modelo — e no strict ele nem
    faria sentido, já que o campo sempre vem preenchido (ou com null)."""
    assert "default" not in _schema_params(_Exemplo)["properties"]["com_default"]
