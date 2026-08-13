"""O cérebro do Mister: transforma a conversa numa decisão {intenção, parâmetros}.

Aqui mora só o MOTOR (como falar com o modelo); os TEXTOS que o cérebro lê
(instrução e fichas) moram em `prompts.py`.

`Cerebro` fala com uma API de chat no formato OpenAI (padrão: OpenRouter) via
`urllib` da stdlib — sem SDK. A base `_CerebroBase` separa a LÓGICA (o que fazer
com a resposta) do TRANSPORTE (`_chamar`), então plugar outro motor um dia é
implementar `_chamar` numa classe nova — e é também o que deixa o teste rodar
sem tocar em rede.

A conversa usa o TOOL-CALLING NATIVO da API: as ferramentas vão como fichas
(`tools`, schema saído do formulário Pydantic — ver `prompts.montar_tools`) e o
modelo responde com uma chamada nativa ou com texto puro (a resposta final). Não
existe protocolo JSON caseiro. **Consequência viva: o modelo escolhido precisa
suportar tools** — se não suportar, a API recusa e vira `ErroCerebro`.

A correção dos parâmetros NÃO é conferida aqui: quem valida é o DESPACHANTE,
contra o formulário da tool. O cérebro só escolhe a chamada.
"""
from __future__ import annotations

import json
import os
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field

from mister.prompts import montar_instrucao, montar_memoria, montar_tools

# Configuração da API por ambiente (sem mexer no código). Padrão: OpenRouter.
API_URL = os.environ.get(
    "MISTER_API_URL", "https://openrouter.ai/api/v1/chat/completions"
)
API_MODELO = os.environ.get("MISTER_API_MODELO", "openai/gpt-5.6-luna")
API_CHAVE_ENV = "MISTER_API_KEY"  # a chave NUNCA fica no código

# Retry da API: soluço transitório de rede/limite não pode custar o turno.
# Só re-tenta o que tende a passar sozinho (rede caída, 429, 5xx) — erro de
# chave/formato falha na hora, sem insistir à toa.
TENTATIVAS_API = 3
ESPERA_RETRY_S = 1.5  # espera entre tentativas (dobra a cada uma: 1.5s, 3s)


@dataclass
class Decisao:
    """O que o cérebro entendeu. `params` ainda NÃO foi validado aqui — quem
    valida é o despachante, contra o formulário (Pydantic) da tool.

    `raciocinio` é a narração curta que vem junto da chamada — não afeta a
    execução (o despachante ignora), serve pro dono acompanhar.

    `aval_do_dono` é o sinal da confirmação: só o laço o marca (True), depois do
    dono topar uma ação pendente. Nunca vem do modelo — o despachante usa isso
    pra decidir se aceita campos internos (ex.: 'confirmado').

    `carimbo` (anti-drift) acompanha o aval_do_dono: é a impressão digital da
    ação que o dono aprovou (ver `confirmacao.carimbar`). O despachante
    recalcula na execução e NEGA se a ação mudou entre o "s" e o rodar.

    `id_chamada` é o `tool_call_id` nativo da API: o agente o usa pra responder
    a chamada no histórico (toda chamada exige um par {role: tool,
    tool_call_id}). Vazio em decisões que não nasceram de chamada nativa
    (repetição pós-confirmação, dublê de teste) — o agente gera um quando precisa."""

    intencao: str | None
    params: dict = field(default_factory=dict)
    texto_original: str = ""
    raciocinio: str = ""
    aval_do_dono: bool = False
    carimbo: str = ""
    id_chamada: str = ""


class ErroCerebro(RuntimeError):
    """Falha de INFRA ao falar com a API: rede caída, chave recusada, limite,
    resposta malformada, modelo sem suporte a tools. É DIFERENTE de "não
    entendi" (intenção None) — aquilo é o modelo dizendo "fora de escopo"; isto
    é o sistema avisando que deu ruim na camada da API, pra não mascarar
    problema de chave/rede como incompreensão."""


def _consulta_do_dono(historico: list[dict]) -> str:
    """A última fala do DONO no histórico — é com ela que a memória de longo
    prazo se busca ("o pedaço que tem a ver com o que VOCÊ falou").

    Mensagem user que começa com '[' não é o dono falando: é o sistema
    (recado do segundo plano, aviso de loop) — pulada. O preço raro de o dono
    abrir uma fala com '[' é uma mensagem sem memória, e só."""
    for mensagem in reversed(historico):
        if mensagem.get("role") != "user":
            continue
        conteudo = str(mensagem.get("content") or "").strip()
        if conteudo and not conteudo.startswith("["):
            return conteudo
    return ""


def _campo_pensar(pensar: bool) -> dict:
    """O campo que liga/desliga o RACIOCÍNIO muda de dialeto por provedor:
    OpenRouter normaliza como 'reasoning'; a forma OpenAI clássica fala
    'thinking'. Decide pela URL — trocar de provedor no .env não exige mexer
    aqui, desde que ele fale um dos dois dialetos."""
    if "openrouter" in API_URL:
        return {"reasoning": {"enabled": pensar}}
    return {"thinking": {"type": "enabled" if pensar else "disabled"}}


class _CerebroBase:
    """A LÓGICA do cérebro, separada do transporte: o que fazer com a resposta
    do modelo. O motor concreto só implementa COMO falar com ele (`_chamar`)."""

    def _chamar(self, mensagens: list[dict], tools: list[dict]) -> dict:
        raise NotImplementedError

    def proximo_passo(self, historico: list[dict]) -> Decisao:
        """Decide o PRÓXIMO passo dado o histórico da conversa (lista de
        mensagens no formato OpenAI). É a peça que sustenta o agente multi-step;
        cada passo continua passando pela validação do despachante.

        A instrução do sistema é REMONTADA aqui, a cada mensagem — é o que faz
        o MISTER.md (as regras do dono) valer na fala seguinte à gravação, sem
        reiniciar a sessão. E a MEMÓRIA DE LONGO PRAZO entra junto: a leitura
        automática busca o que tem a ver com a última fala do dono e costura as
        notas no prompt — fresca a cada chamada, nunca acumulada no histórico
        (assunto que passou sai do contexto sozinho). O transporte só envia o
        que receber.

        A tradução da resposta nativa: chamada de ferramenta vira a intenção (o
        `content` que vem junto é a narração); texto puro SEM chamada é a
        resposta final ('responder'); nada dos dois (raro) vira None — o cinto
        pra resposta vazia."""
        conteudo = montar_instrucao()
        consulta = _consulta_do_dono(historico)
        bloco = montar_memoria(consulta) if consulta else ""
        if bloco:
            conteudo = f"{conteudo}\n\n{bloco}"
        sistema = {"role": "system", "content": conteudo}
        mensagem = self._chamar([sistema, *historico], montar_tools())
        ultimo = str(historico[-1].get("content") or "") if historico else ""
        chamadas = mensagem.get("tool_calls") or []
        narracao = str(mensagem.get("content") or "").strip()
        if chamadas:
            # UM passo por vez: só a 1ª chamada vale (pedimos parallel_tool_calls
            # False; se o provedor mandar extras mesmo assim, elas nem entram no
            # histórico — o agente só registra a jogada que ele de fato responde).
            funcao = chamadas[0].get("function") or {}
            try:
                params = json.loads(funcao.get("arguments") or "{}")
            except ValueError:
                params = {}
            return Decisao(
                intencao=funcao.get("name"),
                params=params if isinstance(params, dict) else {},
                texto_original=ultimo,
                raciocinio=narracao,
                id_chamada=str(chamadas[0].get("id") or ""),
            )
        if narracao:
            # Texto puro, sem chamada: é a RESPOSTA FINAL — o jeito nativo de
            # 'responder' (a intenção segue existindo pro agente, não pra API).
            return Decisao("responder", {"mensagem": narracao}, texto_original=ultimo)
        return Decisao(None, {}, texto_original=ultimo)


class Cerebro(_CerebroBase):
    """Cérebro via API (OpenRouter e compatíveis): pergunta e devolve uma
    `Decisao` validável."""

    def __init__(self, chave: str | None = None):
        self._chave = chave or os.environ.get(API_CHAVE_ENV)
        if not self._chave:
            raise RuntimeError(
                f"Falta a chave da API. Defina {API_CHAVE_ENV} no ambiente."
            )
        self.modelo = os.environ.get("MISTER_API_MODELO") or API_MODELO

    def _chamar(self, mensagens: list[dict], tools: list[dict]) -> dict:
        """Faz UMA ida à API (com retry pra soluço transitório) e devolve a
        MENSAGEM crua do modelo ({content, tool_calls}) — quem traduz é o
        `proximo_passo`.

        Levanta `ErroCerebro` em falha de INFRA (rede/auth/formato) — inclusive
        modelo que não suporta tool-calling (a API recusa)."""
        pedido = {
            "model": self.modelo,
            # A instrução do sistema já vem DENTRO de `mensagens` (quem monta é
            # o proximo_passo, a cada mensagem) — aqui é só transporte.
            "messages": mensagens,
            # Explícito em vez de confiar no default da API, pra não mudar por
            # baixo de nós. Quem decide o próximo passo pensa antes.
            **_campo_pensar(True),
            # 0.0 mantém o ROTEAMENTO repetível (mesmo comando -> mesma escolha
            # de ferramenta). O afrouxar vem do espaço pra pensar, não de mais
            # aleatoriedade.
            "temperature": 0.0,
            # 1600 (não 800): no modo pensante o raciocínio CONSOME o teto — teto
            # curto cortaria a resposta no meio.
            "max_tokens": 1600,
            "tools": tools,
            # Um passo por vez: pede UMA chamada por resposta. Nem todo provedor
            # honra — o proximo_passo pega só a 1ª de qualquer jeito.
            "parallel_tool_calls": False,
        }
        corpo = json.dumps(pedido).encode("utf-8")

        espera = ESPERA_RETRY_S
        for tentativa in range(1, TENTATIVAS_API + 1):
            req = urllib.request.Request(
                API_URL,
                data=corpo,
                headers={
                    "Content-Type": "application/json",
                    "Authorization": f"Bearer {self._chave}",
                },
                method="POST",
            )
            try:
                # 120s: com raciocínio ligado e conversa longa, a API pensa bem
                # mais que 30s — timeout curto virava "falha na API" à toa.
                with urllib.request.urlopen(req, timeout=120) as resp:
                    resposta = json.loads(resp.read().decode("utf-8"))
                mensagem = resposta["choices"][0]["message"]
                if not isinstance(mensagem, dict):
                    raise ValueError("mensagem fora do formato")  # cai no retry
                return mensagem
            except urllib.error.HTTPError as e:
                # 429 (limite) e 5xx (servidor tossiu) tendem a passar: re-tenta.
                # 4xx de chave/pedido não passa sozinho: falha na hora.
                if e.code in (429, 500, 502, 503, 504) and tentativa < TENTATIVAS_API:
                    time.sleep(espera)
                    espera *= 2
                    continue
                raise ErroCerebro(
                    f"a API recusou (HTTP {e.code}). Confira a chave ({API_CHAVE_ENV}), "
                    f"o limite de uso, ou se o modelo '{self.modelo}' suporta ferramentas."
                ) from e
            except urllib.error.URLError as e:
                if tentativa < TENTATIVAS_API:
                    time.sleep(espera)
                    espera *= 2
                    continue
                raise ErroCerebro(f"não cheguei na API — confira a rede ({e.reason}).") from e
            except (KeyError, ValueError) as e:  # ValueError cobre JSONDecodeError
                # Resposta truncada/malformada também é soluço transitório
                # (ex.: JSON cortado no max_tokens): re-tentar costuma resolver.
                if tentativa < TENTATIVAS_API:
                    time.sleep(espera)
                    espera *= 2
                    continue
                raise ErroCerebro("a API respondeu fora do formato esperado.") from e
        raise ErroCerebro("a API não respondeu depois de várias tentativas.")


def criar_cerebro() -> Cerebro:
    """Fábrica do cérebro. Hoje só existe o motor de API — a função existe pra
    `__main__`/testes não conhecerem a classe concreta (plugar outro motor um
    dia = mexer só aqui)."""
    return Cerebro()
