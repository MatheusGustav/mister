"""O que roda quando você digita `mister`: lê a linha, chama o agente, salva.

Este arquivo só monta as peças e conduz o vai-e-vem — decisão é do cérebro,
segurança é do despachante, desenho é da pele. Cada uma no seu lugar.
"""
from __future__ import annotations

import argparse

from mister import conversa, interface
from mister.agente import conversar
from mister.brain import criar_cerebro
from mister.dispatcher import despachar

# As tools se cadastram no registro quando o módulo é importado — importar aqui
# é o que faz o cérebro enxergá-las.
import mister.tools.basic  # noqa: F401


def _escolher_conversa(ui) -> list[dict]:
    """O menu do `-r`: lista as conversas guardadas e retoma a escolhida."""
    sessoes = conversa.listar_sessoes()
    if not sessoes:
        ui.nota("(nenhuma conversa guardada ainda — começando uma nova)")
        conversa.iniciar_sessao()
        return []
    ui.nota("Conversas guardadas:")
    for i, s in enumerate(sessoes, 1):
        ui.nota(f"  {i}. {s['quando']} — {s['resumo']} ({s['n']} mensagens)")
    try:
        escolha = ui.perguntar("Qual? (número, ou Enter pra começar uma nova) ").strip()
    except (EOFError, KeyboardInterrupt):
        escolha = ""
    if escolha.isdigit() and 1 <= int(escolha) <= len(sessoes):
        alvo = sessoes[int(escolha) - 1]["caminho"]
        conversa.iniciar_sessao(alvo)
        return conversa.carregar_sessao(alvo)
    conversa.iniciar_sessao()
    return []


def _retomar(ui, args) -> list[dict]:
    """De onde a sessão começa: `-r` escolhe qualquer conversa guardada,
    `--continue` retoma a última, e sem nenhum dos dois começa limpa. Toda
    sessão do terminal é ARQUIVADA, pra dar pra voltar nela depois."""
    if args.retomar:
        return _escolher_conversa(ui)
    if args.continuar:
        sessoes = conversa.listar_sessoes()
        if sessoes:
            # A última sessão arquivada É a última conversa: retomá-la mantém os
            # saves no MESMO arquivo (sem duplicar no acervo).
            conversa.iniciar_sessao(sessoes[0]["caminho"])
            return conversa.carregar_sessao(sessoes[0]["caminho"])
        # Acervo ainda vazio: puxa da "última" clássica e já nasce arquivando.
        historico = conversa.carregar()
        conversa.iniciar_sessao()
        return historico
    conversa.iniciar_sessao()
    return []


def main() -> None:
    parser = argparse.ArgumentParser(
        prog="mister",
        description="Assistente pessoal do Matheus Gustav, no terminal.",
        epilog="o correio (arquivo entre PC e celular) é o ekodide, à parte: ekodide --help",
    )
    parser.add_argument(
        "--continue", dest="continuar", action="store_true",
        help="retoma a última conversa (do ponto em que você fechou)",
    )
    parser.add_argument(
        "-r", "--retomar", action="store_true",
        help="mostra TODAS as conversas guardadas e retoma a que você escolher",
    )
    args = parser.parse_args()

    ui = interface.criar()
    try:
        cerebro = criar_cerebro()
    except RuntimeError as erro:
        # Falta de chave não merece traceback: avisa com jeito e sai.
        ui.erro(str(erro))
        ui.nota("dica: confira o .env (a chave da API vai em MISTER_API_KEY).")
        raise SystemExit(1)

    historico = _retomar(ui, args)
    ui.abrir(len(historico))

    while True:
        try:
            texto = ui.entrada().strip()
        except (EOFError, KeyboardInterrupt):
            ui.fechar()
            break
        if not texto:
            continue

        try:
            historico = conversar(
                cerebro,
                despachar,
                texto,
                perguntar=ui.perguntar,
                mostrar=ui.mostrar,
                tracar=ui.tracar,
                pensando=ui.pensando,
                atividade=ui.atividade,
                historico=historico,
            )
            historico = conversa.compactar(historico)
            conversa.salvar(historico)
        except KeyboardInterrupt:
            # ESC (ou Ctrl+C) no meio do trabalho: cancela SÓ este turno; a
            # conversa e a sessão seguem de pé.
            ui.nota("\n  (cancelado)\n")


if __name__ == "__main__":
    main()
