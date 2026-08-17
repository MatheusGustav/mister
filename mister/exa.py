"""O TRANSPORTE da internet: o POST cru pro MCP público da Exa.

A internet do Mister é a Exa, direto — sem chave de API, sem cadastro, sem
`mcporter`, sem npm, sem dependência nova. Este arquivo fala HTTP na mão com o
`urllib` da stdlib, do mesmo jeito que o `brain.py` fala com o OpenRouter.

Duas coisas que este arquivo sabe e ninguém mais precisa saber:

  - **É um POST só, sem handshake.** Não precisa de `initialize` nem de
    `Mcp-Session-Id`: mandar `tools/call` cru já responde 200 com o resultado.
  - **A resposta vem em SSE, não em JSON puro** — `event: message` seguido de
    uma linha `data: {...}`. O prefixo `data: ` sai antes do `json.loads`.

Mora FORA do `brain.py` de propósito: internet é ferramenta, não motor. Quem
decide buscar é o cérebro (chamando a tool), nunca este código — e as tools em
`tools/internet.py` são as únicas clientes daqui.

Falha (rede fora, timeout, SSE ilegível, erro da Exa) sai como `ErroExa`, e
quem chama traduz pra recusa com explicação — nunca exceção estourando o turno.
"""
from __future__ import annotations

import json
import os
import urllib.error
import urllib.request

# Endpoint por ambiente, no mesmo desenho do brain.API_URL (trocar sem mexer no
# código). Padrão: o MCP público da Exa.
ENDPOINT = os.environ.get("MISTER_EXA_URL", "https://mcp.exa.ai/mcp")

# 30s, não os 240s do cérebro: busca responde em segundos. Timeout longo aqui só
# faria o dono esperar à toa quando a Exa está fora.
TIMEOUT_S = 30

_CABECALHOS = {
    "Content-Type": "application/json",
    # Os DOIS: o endpoint recusa quem não aceita text/event-stream.
    "Accept": "application/json, text/event-stream",
    # NÃO TIRE ESTA LINHA. Sem ela o urllib se apresenta como
    # 'Python-urllib/3.x' e a Exa devolve HTTP 403 — conferido nesta máquina em
    # 16/08/2026: mesmo POST, mesmo corpo, muda só o User-Agent e vira 200. Foi
    # o que fez o teste com curl passar e o código falhar.
    "User-Agent": "Mister/0.1",
}


class ErroExa(RuntimeError):
    """Falha ao falar com a Exa: rede caída, timeout, resposta fora do formato
    ou erro devolvido por ela. Quem chama transforma em `Resultado(False, ...)`."""


def _decodificar(corpo: str) -> dict:
    """Tira o embrulho SSE e devolve o objeto JSON-RPC de dentro.

    O corpo normal é `event: message` + `data: {...}`, mas o mesmo endpoint pode
    responder JSON puro — as duas formas passam por aqui. Linha `data:` que não
    for JSON (ou que não trouxer `result`/`error`) é pulada: SSE pode carregar
    quadro de controle no meio."""
    linhas = [
        linha[len("data:"):].strip()
        for linha in corpo.splitlines()
        if linha.startswith("data:")
    ]
    for bruto in linhas or [corpo.strip()]:
        try:
            objeto = json.loads(bruto)
        except ValueError:
            continue
        if isinstance(objeto, dict) and ("result" in objeto or "error" in objeto):
            return objeto
    raise ErroExa("a Exa respondeu fora do formato esperado.")


def _texto_do_resultado(resposta: dict) -> str:
    """O texto útil de dentro do JSON-RPC: `result.content[*].text`, já limpo e
    legível (título, URL, data, autor, trechos — ou a página em markdown)."""
    if "error" in resposta:
        erro = resposta["error"]
        recado = erro.get("message") if isinstance(erro, dict) else erro
        raise ErroExa(f"a Exa recusou a chamada: {recado}")
    resultado = resposta.get("result")
    if not isinstance(resultado, dict):
        raise ErroExa("a Exa respondeu sem resultado.")
    partes = [
        str(bloco.get("text") or "")
        for bloco in resultado.get("content") or []
        if isinstance(bloco, dict) and bloco.get("type") == "text"
    ]
    texto = "\n".join(parte for parte in partes if parte).strip()
    # `isError` é o jeito do MCP dizer "a ferramenta rodou e deu errado" (URL
    # inválida, por exemplo) — é falha, mesmo tendo vindo em HTTP 200.
    if resultado.get("isError"):
        raise ErroExa(texto or "a Exa não conseguiu atender.")
    if not texto:
        raise ErroExa("a Exa respondeu sem texto.")
    return texto


def chamar(ferramenta: str, argumentos: dict) -> str:
    """Chama UMA ferramenta da Exa (`web_search_exa` ou `web_fetch_exa`) e
    devolve o texto pronto. Levanta `ErroExa` em qualquer tropeço."""
    pedido = {
        "jsonrpc": "2.0",
        "id": 1,
        "method": "tools/call",
        "params": {"name": ferramenta, "arguments": argumentos},
    }
    req = urllib.request.Request(
        ENDPOINT,
        data=json.dumps(pedido).encode("utf-8"),
        headers=_CABECALHOS,
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            corpo = resp.read().decode("utf-8", errors="replace")
    except urllib.error.HTTPError as erro:
        raise ErroExa(f"a Exa recusou (HTTP {erro.code}).") from erro
    except urllib.error.URLError as erro:
        raise ErroExa(f"não cheguei na Exa — confira a rede ({erro.reason}).") from erro
    except OSError as erro:  # timeout do socket e afins
        raise ErroExa(f"a Exa não respondeu a tempo ({erro}).") from erro
    return _texto_do_resultado(_decodificar(corpo))
