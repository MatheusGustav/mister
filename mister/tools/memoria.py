"""A CANETA — as tools que mexem no grafo.

`anotar_memoria` decide o próprio cérebro, NA HORA que quiser, como ferramenta
— não num gancho de fim de conversa. Encaixa sem peça nova: o laço já funciona
assim (cérebro pede, executa, vê o resultado, segue), e o registro reconhece a
tool sozinho.

A escrita de `anotar_memoria` roda em SEGUNDO PLANO, no jeito do `envios.py`: a
tool responde na hora e o desfecho chega como recado no turno seguinte —
anotar nunca trava a conversa. Sem confirmação de propósito: o grafo é o
caderno do Mister (a divisão da memória — regra é do dono, fato é dele). Até a
TUI existir, o anotar fica sempre ligado — mesma regra do ekodide (instalado =
disponível); o interruptor é botão, e botão mora na TUI.

As outras quatro (`ler_nota`, `trocar_trecho`, `reescrever_nota`,
`apagar_nota`) são SÍNCRONAS — corrigir memória depende do que a leitura
achou, não é fogo-e-esquece como anotar. `trocar_trecho`, `reescrever_nota` e
`apagar_nota` passam pela TRAVA DE LER-ANTES (`leituras.py`): recusam mexer
numa nota que o cérebro não leu nesta sessão (por `ler_nota` ou pela leitura
automática) — mesma ideia do Edit/Read do Claude Code. `apagar_nota` também
pede confirmação SEMPRE (critério de irreversibilidade, ver
`confirmacao.pergunta_de_confirmacao` — apagar não tem cópia de segurança).
"""
from __future__ import annotations

from pydantic import BaseModel

from mister import envios, leituras, memoria
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


# --- ler, corrigir e apagar (síncronas, com a trava de ler-antes) -----------

class LerNotaParams(BaseModel):
    nome: str


class TrocarTrechoParams(BaseModel):
    nome: str
    velho: str
    novo: str


class ReescreverNotaParams(BaseModel):
    nome: str
    conteudo: str


class ApagarNotaParams(BaseModel):
    nome: str


def _nao_achei(nome: str) -> Resultado:
    return Resultado(False, f"Não achei a nota '{nome}'.")


def _precisa_ler_antes(nome: str) -> Resultado:
    return Resultado(
        False,
        f"Preciso ler '{nome}' antes de mexer nela nesta conversa.",
        "Use ler_nota primeiro (ou ela pode já ter chegado pela memória automática).",
    )


@tool(
    "ler_nota",
    LerNotaParams,
    "LÊ o conteúdo INTEIRO de uma nota do grafo de memória, pelo título. Use "
    "antes de corrigir uma nota: trocar_trecho, reescrever_nota e apagar_nota "
    "recusam mexer em nota que você não leu nesta conversa. 'nome' é o título "
    "da nota, do jeito que aparece nos [[links]] ou no anotar_memoria.",
)
def ler_nota(params: LerNotaParams) -> Resultado:
    caminho = memoria.caminho_da(params.nome)
    try:
        texto = caminho.read_text(encoding="utf-8")
    except OSError:
        return _nao_achei(params.nome)
    leituras.marcar(caminho)
    return Resultado(True, texto)


@tool(
    "trocar_trecho",
    TrocarTrechoParams,
    "CORRIGE uma nota do grafo achando um trecho EXATO e trocando pelo novo — "
    "o jeito PADRÃO de editar memória (mais seguro que reescrever a nota "
    "inteira). 'velho' tem que bater com o texto da nota CARACTERE POR "
    "CARACTERE e aparecer uma vez só — se não achar, ou achar mais de uma vez, "
    "a tool recusa e explica o problema. Exige ter lido a nota antes com "
    "ler_nota nesta conversa.",
)
def trocar_trecho(params: TrocarTrechoParams) -> Resultado:
    caminho = memoria.caminho_da(params.nome)
    if not leituras.foi_lido(caminho):
        return _precisa_ler_antes(params.nome)
    try:
        texto = caminho.read_text(encoding="utf-8")
    except OSError:
        return _nao_achei(params.nome)
    ocorrencias = texto.count(params.velho)
    if ocorrencias == 0:
        return Resultado(
            False,
            f"Não achei esse trecho em '{params.nome}'.",
            "Confira com ler_nota se o texto bate exatamente (espaço, acento, "
            "quebra de linha).",
        )
    if ocorrencias > 1:
        return Resultado(
            False,
            f"O trecho aparece {ocorrencias} vezes em '{params.nome}' — tem que "
            "ser único.",
            "Inclua mais texto ao redor pra apontar o trecho certo.",
        )
    caminho.write_text(texto.replace(params.velho, params.novo, 1), encoding="utf-8")
    leituras.marcar(caminho)
    return Resultado(True, f"Troquei o trecho em '{params.nome}'.")


@tool(
    "reescrever_nota",
    ReescreverNotaParams,
    "SUBSTITUI o conteúdo INTEIRO de uma nota do grafo pelo novo — o que "
    "estava lá some. Use pra reorganizar/limpar uma nota bagunçada; pra uma "
    "correção pontual, trocar_trecho é mais seguro. Exige ter lido a nota "
    "antes com ler_nota nesta conversa. Sem cópia de segurança.",
)
def reescrever_nota(params: ReescreverNotaParams) -> Resultado:
    caminho = memoria.caminho_da(params.nome)
    if not leituras.foi_lido(caminho):
        return _precisa_ler_antes(params.nome)
    conteudo = params.conteudo.strip()
    if not conteudo:
        return Resultado(
            False,
            "O conteúdo novo veio vazio — se é pra apagar a nota, use apagar_nota.",
        )
    caminho.write_text(f"{conteudo}\n", encoding="utf-8")
    leituras.marcar(caminho)
    return Resultado(True, f"Reescrevi '{params.nome}'.")


@tool(
    "apagar_nota",
    ApagarNotaParams,
    "APAGA de vez uma nota do grafo de memória — o arquivo some da pasta, sem "
    "cópia de segurança. Pede a confirmação do dono SEMPRE antes de rodar. "
    "Exige ter lido a nota antes com ler_nota nesta conversa. Use quando o "
    "assunto morreu de vez (pra corrigir, use trocar_trecho ou "
    "reescrever_nota, não apague pra reescrever).",
)
def apagar_nota(params: ApagarNotaParams) -> Resultado:
    caminho = memoria.caminho_da(params.nome)
    if not leituras.foi_lido(caminho):
        return _precisa_ler_antes(params.nome)
    try:
        caminho.unlink()
    except OSError as erro:
        return Resultado(False, f"Não consegui apagar '{params.nome}': {erro}")
    return Resultado(True, f"Apaguei a nota '{params.nome}'.")
