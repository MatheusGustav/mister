"""A tool do MISTER.md — o cérebro SUGERE a regra, o DONO grava.

O caminho inteiro já existia antes desta tool: `PrecisaConfirmar` para o laço
e pergunta, o "s" do dono volta carimbado, o despachante confere o carimbo e
só então o handler roda de novo — agora escrevendo. Aqui só se pendura a
escrita de regra nesse cano.

Depois do "s", o cérebro recebe o `Resultado` como qualquer outro e VOLTA pra
tarefa de onde parou — a regra entra no arquivo e a conversa não se perde. E
como a instrução é remontada a cada mensagem (ver `brain.proximo_passo`), a
regra recém-gravada já vale na fala seguinte.
"""
from __future__ import annotations

from pydantic import BaseModel

from mister import regras
from mister.confirmacao import PrecisaConfirmar
from mister.registry import tool
from mister.resultado import Resultado


class GuardarRegraParams(BaseModel):
    regra: str
    confirmado: bool = False  # INTERNO: escondido do cérebro (CAMPOS_INTERNOS)


@tool(
    "guardar_regra",
    GuardarRegraParams,
    "GUARDA no MISTER.md uma REGRA FIXA de comportamento, que passa a valer em "
    "TODA conversa, pra sempre. Use quando o usuário ditar uma regra ('nunca "
    "faça X', 'sempre me avise antes de Y', 'a partir de agora...') ou quando "
    "VOCÊ perceber que acabou de aprender uma — aí proponha por conta própria. "
    "O parâmetro 'regra' é a regra em UMA frase curta, do jeito que deve ser "
    "obedecida. O sistema pede a aprovação do dono sozinho: sem o sim dele, "
    "nada é gravado. NÃO use pra fato solto (spec de aparelho, preferência de "
    "momento, coisa desta conversa) — regra é ordem permanente de comportamento.",
)
def guardar_regra(params: GuardarRegraParams) -> Resultado:
    regra = " ".join(params.regra.split())
    if not regra:
        return Resultado(False, "A regra veio vazia — me diga o que devo guardar.")
    # ESCREVER NO MISTER.md MUDA O COMPORTAMENTO PARA SEMPRE — e o que muda
    # comportamento, o dono aprova. É a divisão da memória: no grafo o Mister
    # escreve sozinho; aqui, nunca.
    if not params.confirmado:
        raise PrecisaConfirmar(f'Guardo esta regra no MISTER.md? "{regra}"')
    try:
        gravada = regras.adicionar(regra)
    except OSError as erro:
        return Resultado(False, f"Não consegui gravar no MISTER.md: {erro}")
    return Resultado(
        True,
        f'Guardei no MISTER.md: "{gravada}"',
        "Ela já vale a partir da próxima fala — em toda conversa.",
    )
