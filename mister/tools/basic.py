"""A tool de brinquedo do encanamento (Etapa 1): prova que o cano tem fim.

Uma tool só, sem rede e sem risco — o registro cadastra, o cérebro enxerga a
ficha, o despachante valida o formulário e executa, e volta um `Resultado`.
Quando isto responde a hora certa, o encanamento inteiro está de pé; o resto do
MVP (as 5 tools do celular) é só pendurar peça no mesmo cano.
"""
from __future__ import annotations

from datetime import datetime

from mister.registry import Formulario, tool
from mister.resultado import Resultado


class SemParametros(Formulario):
    """Formulário vazio: a tool não precisa de nada."""


@tool(
    "que_horas_sao",
    SemParametros,
    "Diz a DATA e a HORA atuais do computador. Use quando o usuário perguntar "
    "'que horas são', 'que dia é hoje', 'qual a data de hoje'.",
)
def que_horas_sao(_: SemParametros) -> Resultado:
    return Resultado(True, datetime.now().strftime("Agora são %H:%M de %d/%m/%Y."))
