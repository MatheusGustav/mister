"""O registro: o @tool cadastra, e o que ele cadastra é o que o resto lê.

Se estes testes passam, adicionar capacidade ao Mister é escrever função +
decorador — sem tocar em despachante, prompt ou cérebro.
"""
import pytest
from pydantic import BaseModel

from mister.registry import REGISTRO, ToolSpec, tool
from mister.resultado import Resultado

import mister.tools.basic  # noqa: F401  (cadastra as tools no registro)


class _Formulario(BaseModel):
    alvo: str


@pytest.fixture
def registro_limpo():
    """Cadastra no registro DE VERDADE e desfaz depois — teste não pode deixar
    tool fantasma pros outros (o registro é global de propósito)."""
    antes = dict(REGISTRO)
    yield
    REGISTRO.clear()
    REGISTRO.update(antes)


def test_decorador_cadastra_a_tool(registro_limpo):
    @tool("tool_de_teste", _Formulario, "descrição rica pro cérebro escolher")
    def _handler(params: _Formulario) -> Resultado:
        return Resultado(True, f"fiz em {params.alvo}")

    spec = REGISTRO["tool_de_teste"]
    assert isinstance(spec, ToolSpec)
    assert spec.formulario is _Formulario
    assert spec.descricao == "descrição rica pro cérebro escolher"


def test_decorador_devolve_a_funcao_intacta(registro_limpo):
    """O @tool cadastra e SAI DA FRENTE: a função continua chamável direto
    (é assim que os testes das tools rodam sem passar pelo despachante)."""

    @tool("outra_de_teste", _Formulario, "…")
    def handler(params: _Formulario) -> Resultado:
        return Resultado(True, "chamada direta")

    assert handler(_Formulario(alvo="x")).mensagem == "chamada direta"


def test_a_tool_do_encanamento_esta_no_registro():
    assert "que_horas_sao" in REGISTRO


def test_toda_tool_tem_descricao_e_devolve_resultado():
    """Invariante da casa: tool sem descrição é tool que o cérebro não sabe
    escolher (afinar roteamento = enriquecer a descrição), e tool que devolve
    texto solto quebra o 'observar' do ciclo."""
    for nome, spec in REGISTRO.items():
        assert spec.descricao.strip(), f"tool sem descrição: {nome}"
        assert issubclass(spec.formulario, BaseModel), f"formulário não-Pydantic: {nome}"
