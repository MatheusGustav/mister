"""Envios do correio em SEGUNDO PLANO — o registro dos que estão em voo.

Se a tool chamasse o ekodide DENTRO dela, o turno inteiro ficaria preso até o
último byte sair — arquivo grande = Mister mudo por minutos. Em vez disso a tool
DISPARA o envio numa thread (o trabalho é I/O-bound: esperar a rede) e responde
na hora; este módulo guarda os envios em voo.

Envio vive ENTRE turnos: dispara num, pode terminar noutro. Quem drena é o laço
do agente, pelo `recados` — nunca uma barreira, só uma válvula: colhe o que já
terminou e segue.

Threads são daemon: fechar o Mister no meio mata o envio, mas o carteiro do
ekodide RETOMA de onde parou no próximo disparo (`/progresso` + `.parcial.meta`).
"""
from __future__ import annotations

import threading
from typing import Callable

_tranca = threading.Lock()
_em_voo: list[dict] = []


def disparar(descricao: str, trabalho: Callable[[], str]) -> None:
    """Roda `trabalho` numa thread e registra o voo. `trabalho` devolve a FALA
    de conclusão (sucesso ou fracasso, já em português) — exceção que escapar
    vira fala de quebra, nunca some calada."""
    slot: dict = {"descricao": descricao, "resultado": [""]}

    def _rodar() -> None:
        try:
            slot["resultado"][0] = trabalho()
        except Exception as e:  # noqa: BLE001 — thread não pode morrer calada
            slot["resultado"][0] = f"O envio de {descricao} quebrou no meio: {e}"

    slot["thread"] = threading.Thread(target=_rodar, daemon=True)
    with _tranca:
        _em_voo.append(slot)
    slot["thread"].start()


def colher_prontos() -> list[str]:
    """As falas dos envios que TERMINARAM (e os tira da lista); quem ainda voa
    fica. Nunca bloqueia — o join só acontece em thread já morta."""
    prontos: list[str] = []
    with _tranca:
        ainda = []
        for slot in _em_voo:
            if slot["thread"].is_alive():
                ainda.append(slot)
            else:
                slot["thread"].join()
                prontos.append(slot["resultado"][0])
        _em_voo[:] = ainda
    return prontos


def em_voo() -> list[str]:
    """Descrição dos envios AINDA em andamento (sem colher ninguém)."""
    with _tranca:
        return [s["descricao"] for s in _em_voo if s["thread"].is_alive()]


def aguardar(timeout: float | None = None) -> None:
    """Espera os envios em voo terminarem (uso: testes e despedidas)."""
    with _tranca:
        threads = [s["thread"] for s in _em_voo]
    for t in threads:
        t.join(timeout)
