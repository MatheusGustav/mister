"""Registro central de ferramentas (tools).

Cada tool se cadastra aqui dizendo quatro coisas: seu nome (a intenção), o
formulário de parâmetros que ela exige (um modelo Pydantic), a função que
executa e a DESCRIÇÃO que o cérebro lê pra escolher. O despachante
(`dispatcher.py`) consulta este registro pra validar os parâmetros e executar,
e os prompts montam as fichas nativas a partir dele — então dar uma capacidade
nova ao Mister é escrever uma função e decorá-la com @tool, sem mexer em mais nada.

Afinar roteamento = enriquecer a DESCRIÇÃO (verbos, sinônimos, exemplos). O
formulário é o mesmo dos dois lados: explica a tool na IDA e valida a resposta
na VOLTA, então alucinação vira "parâmetro inválido", nunca ação errada.
"""
from __future__ import annotations

from dataclasses import dataclass
from typing import Callable, Type

from pydantic import BaseModel

from mister.resultado import Resultado


@dataclass
class ToolSpec:
    nome: str
    formulario: Type[BaseModel]
    handler: Callable[[BaseModel], Resultado]
    descricao: str


REGISTRO: dict[str, ToolSpec] = {}


def tool(nome: str, formulario: Type[BaseModel], descricao: str = ""):
    """Decorador que cadastra uma função como tool do Mister."""

    def decorador(fn: Callable[[BaseModel], Resultado]) -> Callable[[BaseModel], Resultado]:
        REGISTRO[nome] = ToolSpec(
            nome=nome, formulario=formulario, handler=fn, descricao=descricao
        )
        return fn

    return decorador
