"""O USO DO CONTEXTO — quanto da janela do modelo a conversa já ocupa.

Duas peças, de propósito separadas:

  - O TETO (o denominador): quantos tokens o modelo aguenta. Quem sabe isso é a
    OpenRouter (`GET /api/v1/models`, campo `context_length`). Buscar custa uma
    ida à rede, então o resultado fica em cache num arquivo e o `teto()` NUNCA
    vai à rede — quem busca é o `atualizar()`, chamado uma vez, de propósito,
    pela thread de trabalho da TUI. Assim ninguém (nem teste) toca em rede sem
    pedir.
  - O DESENHO (`barra`): número vira texto de barra. Função pura, sem tela e
    sem rede — é o que os testes provam.

O NUMERADOR não mora aqui: é o `prompt_tokens` da ÚLTIMA chamada
(`brain._CerebroBase.ultimo_uso`), que é o tamanho ATUAL do contexto. Somar os
`prompt_tokens` de todas as chamadas daria consumo acumulado, não ocupação — a
barra passaria de 100% em minutos.

Sobre a barra ENCOLHER sozinha: o Mister compacta a conversa
(`conversa.compactar`). Quando a compactação corta, o `prompt_tokens` da
chamada seguinte cai e a barra desce. É certo, não é bug.
"""
from __future__ import annotations

import json
import os
import urllib.error
import urllib.request
from pathlib import Path

# O teto de reserva, quando não dá pra saber o de verdade (sem rede, modelo
# fora da lista da OpenRouter). Decisão do dono (17/08/2026): 128k, que é o
# teto comum da maioria dos modelos de hoje. Quando ele vale, a tela DIZ que o
# número é estimado — barra sem aviso é barra que mente.
TETO_PADRAO = 128_000

# O cache do teto por modelo. O nome NÃO é 'modelos' de propósito: já existem
# ~/.mister/modelos/ (os pesos do embedding, ver indice.py) e um modelos.json
# avulso na mesma pasta — mais um 'modelos' aqui seria pedir confusão.
ARQUIVO_PADRAO = str(Path.home() / ".mister" / "contexto_modelos.json")

URL_MODELOS = "https://openrouter.ai/api/v1/models"

# O desenho da barra: cheio e vazio.
CHEIO = "█"
VAZIO = "░"

# Os cortes de cor da barra (fração ocupada). Abaixo do primeiro é o acento
# normal; daí pra cima a tela vai avisando.
CORTE_ALTO = 0.80
CORTE_CRITICO = 0.95


def _arquivo() -> Path:
    """Lê MISTER_CONTEXTO_MODELOS A CADA chamada (não na importação) — o mesmo
    truque do resto do ~/.mister: o conftest aponta os testes pra longe."""
    return Path(os.environ.get("MISTER_CONTEXTO_MODELOS", ARQUIVO_PADRAO))


def _cache() -> dict:
    try:
        lido = json.loads(_arquivo().read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return {}  # sem arquivo (ou quebrado) = nada em cache, e pronto
    return lido if isinstance(lido, dict) else {}


def teto(modelo: str) -> tuple[int, bool]:
    """O teto de contexto do `modelo`: (tokens, estimado).

    `estimado=True` quer dizer "não sei o de verdade, este é o de reserva" — a
    tela precisa dizer isso ao dono. NUNCA vai à rede: só lê o cache que o
    `atualizar` deixou."""
    valor = _cache().get(modelo)
    if isinstance(valor, int) and valor > 0:
        return valor, False
    return TETO_PADRAO, True


def atualizar(modelo: str = "", tempo_limite: int = 15) -> bool:
    """Busca os tetos na OpenRouter e grava no cache. Devolve se deu certo.

    Falha (rede caída, resposta estranha, disco recusando) volta False e SÓ —
    quem chama segue com o teto de reserva. O `modelo` é só pra poder sair cedo
    quando ele já está em cache; sem ele, atualiza a lista inteira mesmo assim.

    Vai à REDE: chame de propósito, de uma thread de trabalho, nunca no
    desenho da tela."""
    if modelo and not teto(modelo)[1]:
        return True  # já está em cache: nem incomoda a rede
    try:
        req = urllib.request.Request(URL_MODELOS, method="GET")
        with urllib.request.urlopen(req, timeout=tempo_limite) as resp:
            resposta = json.loads(resp.read().decode("utf-8"))
        tetos = {
            str(item.get("id")): int(item.get("context_length"))
            for item in (resposta.get("data") or [])
            if isinstance(item, dict)
            and item.get("id")
            and isinstance(item.get("context_length"), int)
            and item.get("context_length") > 0
        }
    except (urllib.error.URLError, OSError, ValueError, TypeError):
        return False
    if not tetos:
        return False
    try:
        arquivo = _arquivo()
        arquivo.parent.mkdir(parents=True, exist_ok=True)
        arquivo.write_text(json.dumps(tetos, ensure_ascii=False), encoding="utf-8")
    except OSError:
        return False
    return True


def curto(tokens: int) -> str:
    """O número como o painel mostra: 850, 24.1k, 128k."""
    if tokens < 1000:
        return str(tokens)
    texto = f"{tokens / 1000:.1f}" if tokens < 100_000 else f"{tokens / 1000:.0f}"
    return f"{texto.removesuffix('.0')}k"


def nivel(usados: int, total: int) -> str:
    """Como a barra deve ser pintada: 'normal', 'alto' ou 'critico'."""
    fracao = (usados / total) if total > 0 else 0.0
    if fracao >= CORTE_CRITICO:
        return "critico"
    if fracao >= CORTE_ALTO:
        return "alto"
    return "normal"


def barra(usados: int, total: int, largura: int = 18) -> list[str]:
    """As duas linhas da barra, prontas pra tela:

        ['████████░░░░░░░░░░  38%', '24.1k / 64k tokens']

    Sem medida ainda (usados=0) devolve a linha de espera no lugar delas. Passar
    do teto não estoura o desenho: a barra trava em cheio e a conta segue
    dizendo a verdade (pode dar mais de 100%)."""
    if usados <= 0:
        return ["(sem medida ainda — a barra aparece na primeira resposta)"]
    fracao = (usados / total) if total > 0 else 0.0
    cheias = min(largura, max(0, round(fracao * largura)))
    desenho = CHEIO * cheias + VAZIO * (largura - cheias)
    return [
        f"{desenho}  {round(fracao * 100)}%",
        f"{curto(usados)} / {curto(total)} tokens",
    ]
