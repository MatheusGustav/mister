"""A CANETA — a tool de anotar no grafo.

O próprio cérebro decide o que virou memória, NA HORA que quiser, como
ferramenta — não num gancho de fim de conversa. Encaixa sem peça nova: o laço
já funciona assim (cérebro pede, executa, vê o resultado, segue), e o registro
reconhece a tool sozinho.

A escrita roda em SEGUNDO PLANO, no jeito do `envios.py`: a tool responde na
hora e o desfecho chega como recado no turno seguinte — anotar nunca trava a
conversa. Sem confirmação de propósito: o grafo é o caderno do Mister (a
divisão da memória — regra é do dono, fato é dele). Até a TUI existir, o
anotar fica sempre ligado — mesma regra do ekodide (instalado = disponível);
o interruptor é botão, e botão mora na TUI.
"""
from __future__ import annotations

from pydantic import BaseModel

from mister import envios, memoria
from mister.registry import tool
from mister.resultado import Resultado


class AnotarMemoriaParams(BaseModel):
    titulo: str
    conteudo: str


def _anotar(titulo: str, conteudo: str) -> str:
    """O trabalho do disparo: grava e devolve a fala de conclusão (exceção que
    escapar o envios transforma em fala de quebra — nunca some calada)."""
    caminho = memoria.escrever(titulo, conteudo)
    return f"Anotei '{titulo}' na memória ({caminho.name})."


@tool(
    "anotar_memoria",
    AnotarMemoriaParams,
    "ANOTA um FATO na sua memória de longo prazo (o grafo de notas), pra você "
    "mesmo reencontrar depois — a conversa some, a nota fica. Use POR CONTA "
    "PRÓPRIA quando aprender algo que vale guardar: spec de um aparelho, um "
    "macete descoberto, o que um projeto é, uma pegadinha que custou tempo. "
    "'titulo' é o assunto da nota, curto (ex.: 'Celular Redmi'); anotar num "
    "título que já existe ACRESCENTA ao fim da nota, então reuse o título pra "
    "completar um assunto. 'conteudo' é o fato em markdown, e assunto "
    "relacionado se liga com [[titulo-da-outra-nota]] — os links são o que "
    "deixa a memória acharável; linkar nota que ainda não existe é permitido e "
    "marca o que vale escrever depois. NÃO use pra regra de comportamento "
    "('nunca faça X') — regra vai no guardar_regra, que passa pelo dono.",
)
def anotar_memoria(params: AnotarMemoriaParams) -> Resultado:
    titulo = params.titulo.strip()
    conteudo = params.conteudo.strip()
    if not memoria.slug(titulo):
        return Resultado(False, "Me dá um título com pelo menos uma letra ou número.")
    if not conteudo:
        return Resultado(False, "A nota veio sem conteúdo — o que era pra guardar?")
    envios.disparar(
        f"nota '{titulo}' → memória",
        lambda: _anotar(titulo, conteudo),
    )
    return Resultado(
        True,
        f"Comecei a anotar '{titulo}' na memória, em segundo plano.",
        "Siga o assunto normalmente; o desfecho chega sozinho.",
    )
