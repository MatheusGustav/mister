"""As tools da INTERNET — cascas finas sobre a Exa (`mister/exa.py`).

Duas ferramentas, e elas cobrem tudo: `pesquisar_web` acha e `abrir_pagina` lê.
Não existe leitor de página separado — a `web_fetch_exa` já devolve a página
como markdown limpo, e aceita várias URLs numa chamada só.

O desenho de sempre, sem exceção nenhuma:

  - A busca é ESCOLHA DO CÉREBRO, nunca automática. Nada de chamar a Exa dentro
    do `brain.py` em cima de todo turno: se o cérebro não pediu, não acontece —
    "que horas são" não toca na internet.
  - Passam pelo DESPACHANTE como as outras: o formulário valida na ida e na
    volta, parâmetro errado vira recusa, nunca ação errada.
  - Têm BOTÃO DO DONO (`interruptores.ligado("internet")`), no mesmo desenho do
    `_disponivel()` do correio: desligado, recusam com explicação.
  - Falha do transporte vira `Resultado(False, ...)` com explicação, no padrão
    do correio — nunca exceção estourando o turno.

O ESCOPO é só o que é PÚBLICO. Nada que dependa de cookie de sessão salvo
(Twitter/X, Bilibili, Xiaohongshu) nem de CLI dirigindo navegador logado
(Reddit, Instagram, Facebook). Consequência direta, e é ela que importa: o
Mister não guarda credencial de rede social nenhuma — nenhum cookie encosta no
histórico gravado (`~/.mister/conversa.json`) nem viaja pro OpenRouter junto
com a conversa.
"""
from __future__ import annotations

from mister import exa, interruptores
from mister.registry import Formulario, tool
from mister.resultado import Resultado

# Tetos: o resultado entra no histórico e volta pro prompt do cérebro a cada
# turno. Busca espevitada (50 resultados, 5 páginas inteiras) afogaria o
# contexto — os números são cortados aqui, calados, em vez de virar recusa.
RESULTADOS_MAX = 10
URLS_MAX = 5
LETRAS_POR_PAGINA_MAX = 10_000
# Teto do texto INTEIRO que volta pro cérebro, no mesmo espírito do
# SAIDA_COMANDO_MAX do rodar_comando.
SAIDA_MAX = 20_000

_ESQUEMAS = ("http://", "https://")


# --- formulários (Pydantic) --------------------------------------------------

class PesquisarWebParams(Formulario):
    busca: str
    quantidade: int = 5


class AbrirPaginaParams(Formulario):
    urls: list[str]
    maximo_por_pagina: int = 3000


# --- o botão do dono ---------------------------------------------------------

def _desligada() -> Resultado:
    return Resultado(
        False,
        "A internet está DESLIGADA (interruptor do dono). Siga sem ela e, se a "
        "tarefa depender da internet, avise o dono.",
    )


def _cortar(texto: str) -> Resultado:
    """O texto da Exa vira `Resultado` — cortado no teto, se passar dele."""
    if len(texto) > SAIDA_MAX:
        return Resultado(
            True,
            texto[:SAIDA_MAX],
            f"Texto cortado em {SAIDA_MAX} caracteres.",
        )
    return Resultado(True, texto)


# --- as tools ----------------------------------------------------------------

@tool(
    "pesquisar_web",
    PesquisarWebParams,
    "PESQUISA na INTERNET e devolve os achados (título, URL, data, autor e "
    "trechos). Use quando a resposta depender de coisa de FORA do computador: "
    "notícia, preço, documentação, versão de um programa, 'procura aí', ou "
    "qualquer fato que você não tem certeza que continua valendo. 'busca' é o "
    "que procurar, escrito como uma frase (a busca entende SIGNIFICADO, então "
    "'como configurar X no Fedora' funciona melhor que palavras soltas); "
    f"'quantidade' é quantos achados quer, de 1 a {RESULTADOS_MAX} (padrão 5). "
    "Isto só devolve os TRECHOS — pra ler a página inteira, chame abrir_pagina "
    "com a URL depois.",
)
def pesquisar_web(params: PesquisarWebParams) -> Resultado:
    if not interruptores.ligado("internet"):
        return _desligada()
    busca = " ".join(params.busca.split())
    if not busca:
        return Resultado(False, "A busca veio vazia — o que era pra procurar?")
    quantidade = max(1, min(params.quantidade, RESULTADOS_MAX))
    try:
        texto = exa.chamar("web_search_exa", {"query": busca, "numResults": quantidade})
    except exa.ErroExa as erro:
        return Resultado(
            False,
            f"Não consegui pesquisar na internet: {erro}",
            "Siga sem a pesquisa ou tente de novo em seguida.",
        )
    return _cortar(texto)


@tool(
    "abrir_pagina",
    AbrirPaginaParams,
    "ABRE uma ou mais páginas da internet pela URL e devolve o texto delas em "
    "markdown limpo. Use pra LER de verdade o que a pesquisa só mostrou em "
    "trecho, ou quando o usuário mandar um link. 'urls' é uma LISTA de "
    f"endereços http/https (até {URLS_MAX} de uma vez — várias na MESMA "
    "chamada, não uma chamada por link); 'maximo_por_pagina' é quantos "
    "caracteres trazer de cada página (padrão 3000, aumente quando precisar do "
    "texto inteiro). Só alcança página PÚBLICA: nada que exija login (rede "
    "social, painel, e-mail) — essas não abrem, e o Mister não guarda senha "
    "nem cookie de site nenhum.",
)
def abrir_pagina(params: AbrirPaginaParams) -> Resultado:
    if not interruptores.ligado("internet"):
        return _desligada()
    urls = [url.strip() for url in params.urls if url and url.strip()]
    if not urls:
        return Resultado(False, "Não veio URL nenhuma pra abrir.")
    fora = [url for url in urls if not url.lower().startswith(_ESQUEMAS)]
    if fora:
        return Resultado(
            False,
            "Só abro endereço http:// ou https:// — isto não é: "
            + ", ".join(f"'{url}'" for url in fora),
        )
    cortadas = urls[URLS_MAX:]
    urls = urls[:URLS_MAX]
    maximo = max(200, min(params.maximo_por_pagina, LETRAS_POR_PAGINA_MAX))
    try:
        texto = exa.chamar("web_fetch_exa", {"urls": urls, "maxCharacters": maximo})
    except exa.ErroExa as erro:
        return Resultado(
            False,
            f"Não consegui abrir a página: {erro}",
            "Confira se o endereço está certo, ou siga sem ela.",
        )
    saida = _cortar(texto)
    if cortadas:
        aviso = (
            f"Abri só as {URLS_MAX} primeiras; ficaram de fora: "
            + ", ".join(f"'{url}'" for url in cortadas)
            + " (peça de novo se precisar)."
        )
        saida.sugestao = f"{saida.sugestao}\n{aviso}".strip()
    return saida
