"""Testes da LISTA DE TAREFAS — o módulo e a tool que escreve nele."""
from __future__ import annotations

import pytest
from pydantic import ValidationError

from mister import tarefas
from mister.tools.tarefas import ListaDeTarefasParams, lista_de_tarefas


def test_definir_reescreve_a_lista_inteira():
    """Não é remendo item a item: a chamada nova SUBSTITUI a anterior."""
    tarefas.definir([{"texto": "ler", "estado": "feito"}, {"texto": "escrever"}])
    tarefas.definir([{"texto": "só esta"}])
    assert tarefas.listar() == [{"texto": "só esta", "estado": "pendente"}]


def test_estado_que_falta_ou_e_estranho_vira_pendente():
    """O painel só sabe desenhar três estados — o resto cai no padrão."""
    tarefas.definir([{"texto": "a"}, {"texto": "b", "estado": "quase"}])
    assert [item["estado"] for item in tarefas.listar()] == ["pendente", "pendente"]


def test_item_sem_texto_e_descartado():
    tarefas.definir([{"texto": "  "}, {"texto": "vale", "estado": "fazendo"}])
    assert tarefas.listar() == [{"texto": "vale", "estado": "fazendo"}]


def test_listar_devolve_copia():
    """Mexer no que o painel leu não pode mexer na lista de verdade."""
    tarefas.definir([{"texto": "a"}])
    copia = tarefas.listar()
    copia[0]["texto"] = "trocado"
    assert tarefas.listar()[0]["texto"] == "a"


def test_resumo_conta_as_feitas():
    assert tarefas.resumo() == ""
    tarefas.definir([
        {"texto": "a", "estado": "feito"},
        {"texto": "b", "estado": "fazendo"},
        {"texto": "c"},
    ])
    assert tarefas.resumo() == "1/3 feitas"


def test_limpar_zera():
    tarefas.definir([{"texto": "a"}])
    tarefas.limpar()
    assert tarefas.listar() == []


def test_tool_grava_no_modulo():
    resultado = lista_de_tarefas(ListaDeTarefasParams(itens=[
        {"texto": "ler o arquivo", "estado": "feito"},
        {"texto": "trocar o trecho", "estado": "fazendo"},
    ]))
    assert resultado.ok
    assert "1/2 feitas" in resultado.mensagem
    assert [item["texto"] for item in tarefas.listar()] == [
        "ler o arquivo", "trocar o trecho",
    ]


def test_tool_com_lista_vazia_limpa():
    tarefas.definir([{"texto": "sobra"}])
    assert lista_de_tarefas(ListaDeTarefasParams(itens=[])).ok
    assert tarefas.listar() == []


def test_formulario_recusa_estado_inventado():
    """A trava é do formulário (Literal), então estado alucinado vira
    'parâmetro inválido' no despachante — nunca item desenhado errado."""
    with pytest.raises(ValidationError):
        ListaDeTarefasParams(itens=[{"texto": "a", "estado": "quase"}])


def test_a_tool_esta_no_registro_com_ficha_valida():
    """Tool que não se cadastra some calada da API. E o schema dela precisa
    sair inteiro do formulário — a lista de itens usa sub-modelo, que é o caso
    que exigia o `$defs` no prompts._schema_params."""
    from mister.prompts import montar_tools

    fichas = {f["function"]["name"]: f for f in montar_tools()}
    assert "lista_de_tarefas" in fichas
    params = fichas["lista_de_tarefas"]["function"]["parameters"]
    assert params["required"] == ["itens"]
    assert "$defs" in params
