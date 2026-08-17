"""A tool do MISTER.md — o cérebro escreve a regra direto.

Escrever regra é reversível (uma regra ruim se apaga ou se reescreve depois,
como qualquer nota) — não bate no critério de confirmação (ver
`confirmacao.pergunta_de_confirmacao`), então grava na hora, sem pedir aval.

Depois de gravar, o cérebro recebe o `Resultado` como qualquer outro e VOLTA
pra tarefa de onde parou — a regra entra no arquivo e a conversa não se perde.
E como a instrução é remontada a cada mensagem (ver `brain.proximo_passo`), a
regra recém-gravada já vale na fala seguinte.
"""
from __future__ import annotations

from mister import regras
from mister.registry import Formulario, tool
from mister.resultado import Resultado


class GuardarRegraParams(Formulario):
    regra: str


@tool(
    "guardar_regra",
    GuardarRegraParams,
    "GUARDA no MISTER.md uma REGRA FIXA de comportamento, que passa a valer em "
    "TODA conversa, pra sempre. Use quando o usuário ditar uma regra ('nunca "
    "faça X', 'sempre me avise antes de Y', 'a partir de agora...') ou quando "
    "VOCÊ perceber que acabou de aprender uma — aí proponha por conta própria. "
    "O parâmetro 'regra' é a regra em UMA frase curta, do jeito que deve ser "
    "obedecida. Grava na hora, sem perguntar. NÃO use pra fato solto (spec de "
    "aparelho, preferência de momento, coisa desta conversa) — regra é ordem "
    "permanente de comportamento.",
)
def guardar_regra(params: GuardarRegraParams) -> Resultado:
    regra = " ".join(params.regra.split())
    if not regra:
        return Resultado(False, "A regra veio vazia — me diga o que devo guardar.")
    try:
        gravada = regras.adicionar(regra)
    except OSError as erro:
        return Resultado(False, f"Não consegui gravar no MISTER.md: {erro}")
    return Resultado(
        True,
        f'Guardei no MISTER.md: "{gravada}"',
        "Ela já vale a partir da próxima fala — em toda conversa.",
    )
