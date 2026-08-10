"""A MEMÓRIA entre turnos: guarda a conversa em disco pra poder retomar depois
com `mister --continue` (a última) ou `mister -r` (qualquer uma guardada).

É a lista de mensagens que o cérebro enxerga. Best-effort: se o arquivo sumir ou
corromper, começa-se uma conversa nova — memória nunca pode travar o Mister.
Trocável por MISTER_CONVERSA / MISTER_CONVERSAS (testes, ou separar perfis).

Isto é memória de CONVERSA, não memória de longo prazo: o que sai da janela sai
de vez (o "sonhar" e as lições ficaram fora do MVP, um de cada vez).
"""
from __future__ import annotations

import json
import os
from datetime import datetime
from pathlib import Path

CAMINHO_PADRAO = str(Path.home() / ".mister" / "conversa.json")


def _caminho() -> str:
    """Lê MISTER_CONVERSA A CADA chamada (não na importação): é o que deixa os
    testes (conftest) apontarem pra outro arquivo sem importar em que ordem os
    módulos carregaram."""
    return os.environ.get("MISTER_CONVERSA", CAMINHO_PADRAO)


def _pasta_sessoes() -> str:
    """O ACERVO: além da "última" (_caminho), cada sessão do terminal ganha um
    arquivo próprio — dá pra voltar a QUALQUER conversa, não só à última."""
    return os.environ.get("MISTER_CONVERSAS", str(Path.home() / ".mister" / "conversas"))


# Teto de caracteres do resumo (a 1ª fala do dono) no menu do -r.
RESUMO_TETO = 60

_sessao: str | None = None   # arquivo da sessão atual (None = ainda sem save)
_arquivar = False            # só o loop do terminal liga (iniciar_sessao)

# Teto de mensagens guardadas: conversa longa não pode crescer pra sempre (custo
# de contexto/token a cada chamada). Mantemos as ÚLTIMAS N — basta pra retomar
# o assunto.
MAX_MENSAGENS = 40


def _corte(historico: list[dict]) -> int:
    """Onde a janela começa: as últimas MAX_MENSAGENS — empurrado pra FRENTE se
    o corte cair no meio de um par nativo (jogada assistant com tool_calls +
    resposta {role: tool}). Janela começando com resposta de ferramenta ÓRFÃ é
    pedido que a API recusa; perder 1-2 mensagens a mais é o preço barato."""
    corte = max(0, len(historico) - MAX_MENSAGENS)
    while corte < len(historico) and historico[corte].get("role") == "tool":
        corte += 1
    return corte


def carregar() -> list[dict]:
    """Devolve a conversa salva — ou [] se não houver / estiver corrompida."""
    try:
        with open(_caminho(), encoding="utf-8") as arquivo:
            dados = json.load(arquivo)
    except (OSError, ValueError):  # ValueError cobre JSON inválido
        return []
    return dados if isinstance(dados, list) else []


def salvar(historico: list[dict]) -> None:
    """Grava a conversa (só as últimas MAX_MENSAGENS). Best-effort: erro não
    trava. Com o arquivamento ligado (`iniciar_sessao`), grava TAMBÉM no arquivo
    da sessão — a "última" (_caminho) segue existindo pro --continue."""
    recorte = historico[_corte(historico):]
    try:
        caminho = Path(_caminho())
        caminho.parent.mkdir(parents=True, exist_ok=True)
        with open(caminho, "w", encoding="utf-8") as arquivo:
            json.dump(recorte, arquivo, ensure_ascii=False)
    except OSError:
        pass
    if _arquivar and recorte:
        try:
            sessao = Path(_arquivo_sessao())
            sessao.parent.mkdir(parents=True, exist_ok=True)
            with open(sessao, "w", encoding="utf-8") as arquivo:
                json.dump(recorte, arquivo, ensure_ascii=False)
        except OSError:
            pass


def iniciar_sessao(caminho: str | None = None) -> None:
    """Liga o ARQUIVAMENTO desta sessão. Sem `caminho`, é conversa NOVA (o
    arquivo nasce no primeiro salvar); com ele, a sessão CONTINUA aquela
    conversa (os saves vão pro mesmo arquivo, sem duplicar)."""
    global _sessao, _arquivar
    _sessao = caminho
    _arquivar = True


def _arquivo_sessao() -> str:
    """O arquivo da sessão atual — criado (nome pela data/hora) na 1ª gravação."""
    global _sessao
    if _sessao is None:
        nome = datetime.now().strftime("%Y-%m-%d-%H%M%S") + ".json"
        _sessao = str(Path(_pasta_sessoes()) / nome)
    return _sessao


def listar_sessoes() -> list[dict]:
    """As conversas guardadas, da mais recente pra mais antiga. Cada item:
    {caminho, quando (dd/mm hh:mm), resumo (1ª fala do dono), n (mensagens)}.
    Arquivo corrompido/vazio é pulado calado (memória nunca trava o Mister)."""
    sessoes = []
    try:
        arquivos = sorted(
            Path(_pasta_sessoes()).glob("*.json"),
            key=lambda a: a.stat().st_mtime,
            reverse=True,
        )
    except OSError:
        return []
    for arq in arquivos:
        try:
            with open(arq, encoding="utf-8") as f:
                msgs = json.load(f)
        except (OSError, ValueError):
            continue
        if not isinstance(msgs, list) or not msgs:
            continue
        resumo = next(
            (
                str(m.get("content", "")).strip()
                for m in msgs
                if m.get("role") == "user" and str(m.get("content", "")).strip()
            ),
            "(sem fala do dono)",
        )
        if len(resumo) > RESUMO_TETO:
            resumo = resumo[: RESUMO_TETO - 1] + "…"
        quando = datetime.fromtimestamp(arq.stat().st_mtime).strftime("%d/%m %H:%M")
        sessoes.append({"caminho": str(arq), "quando": quando, "resumo": resumo, "n": len(msgs)})
    return sessoes


def carregar_sessao(caminho: str) -> list[dict]:
    """Carrega UMA conversa guardada — ou [] se sumiu/corrompeu."""
    try:
        with open(caminho, encoding="utf-8") as arquivo:
            dados = json.load(arquivo)
    except (OSError, ValueError):
        return []
    return dados if isinstance(dados, list) else []


def compactar(historico: list[dict]) -> list[dict]:
    """Corta a conversa EM MEMÓRIA nas últimas MAX_MENSAGENS. Sem isto ela
    cresceria sem teto dentro de uma sessão longa e cada ida à API ficaria mais
    cara que a anterior."""
    if len(historico) <= MAX_MENSAGENS:
        return historico
    return historico[_corte(historico):]


def esquecer() -> None:
    """Apaga a conversa salva (recomeçar do zero). Best-effort."""
    try:
        os.remove(_caminho())
    except OSError:
        pass
