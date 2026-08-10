"""A PELE: imprime na tela e lê o que o dono digita.

Terminal simples de propósito — `input()` + `rich`. Sobe rápido e sai da frente:
a TUI de verdade do Mister vai ser outra coisa e ganha etapa própria, então aqui
não se investe em layout.

Duas regras que valem manter quando a pele mudar:

  - **Texto do modelo NUNCA vira marcação.** Tudo que veio da API (ou de um
    arquivo, ou do celular) é impresso como `Text`, não como markup do rich —
    senão um "[bold]" no meio da resposta vira comando de formatação, e o mesmo
    buraco serve pra coisa pior.
  - **O ESC é o freio.** Enquanto o Mister trabalha, uma vigia lê o teclado cru;
    ESC vira o mesmo SIGINT do Ctrl+C, que o laço já sabe tratar como "cancela
    só este turno". A vigia NUNCA roda durante um input (roubaria a tecla).
"""
from __future__ import annotations

import os
import select
import signal
import sys
import threading
from contextlib import contextmanager, nullcontext

from rich.console import Console
from rich.text import Text

COR_VIVO = "#4cc3e6"    # o acento (prompt, título)
COR_DIM = "#5f7a8f"     # bastidor e notas
AMBAR = "#d9b44a"       # pergunta/confirmação: pede atenção sem gritar
VERMELHO = "#e0555f"    # erro

# O desenho de cada estado: o agente diz O QUE está acontecendo, a pele escolhe
# como mostrar. Estado desconhecido cai no genérico — nunca quebra.
_ATIVIDADES = {
    "pensando": ("dots", "pensando"),
    "executando": ("dots2", "fazendo"),
}

# Uma vigia de teclado por vez: atividades podem se aninhar (o laço abre uma
# dentro da outra) e duas threads brigando pelo stdin embaralham o terminal.
_freio_ligado = False


def _vigiar_teclado(parar: threading.Event) -> None:
    """Corpo da thread: lê o teclado cru até o evento de parada. ESC puro cancela
    o turno (pelo mesmo caminho do Ctrl+C); o resto é descartado."""
    import termios
    import tty

    fd = sys.stdin.fileno()
    velho = termios.tcgetattr(fd)
    try:
        tty.setcbreak(fd)
        while not parar.is_set():
            if not select.select([fd], [], [], 0.1)[0]:
                continue
            if os.read(fd, 1) != b"\x1b":
                continue
            # ESC[A/B… = seta/sequência; ESC sem continuação imediata = ESC puro.
            if select.select([fd], [], [], 0.05)[0]:
                os.read(fd, 8)  # drena a sequência e segue vigiando
                continue
            os.kill(os.getpid(), signal.SIGINT)
            return
    except Exception:
        pass  # o freio é conforto, nunca pode derrubar o trabalho
    finally:
        try:
            termios.tcsetattr(fd, termios.TCSADRAIN, velho)
        except Exception:
            pass


@contextmanager
def _com_freio():
    """Liga a vigia do ESC enquanto o bloco roda. Sem terminal de verdade (pipe,
    teste, cron) não liga nada — e continua funcionando igual."""
    global _freio_ligado
    try:
        interativo = sys.stdin.isatty()
    except Exception:
        interativo = False
    if _freio_ligado or not interativo:
        yield
        return
    _freio_ligado = True
    parar = threading.Event()
    thread = threading.Thread(target=_vigiar_teclado, args=(parar,), daemon=True)
    thread.start()
    try:
        yield
    finally:
        parar.set()
        thread.join(timeout=0.5)
        _freio_ligado = False


class Pele:
    """A pele do terminal: um console do rich e nada de estado escondido."""

    def __init__(self) -> None:
        self.console = Console()

    # --- abertura e fecho ---------------------------------------------------

    def abrir(self, retomando: int = 0) -> None:
        self.console.print(Text("◆ MISTER", style=f"bold {COR_VIVO}"))
        if retomando:
            self.console.print(Text(
                f"(retomando a conversa anterior — {retomando} mensagens lembradas)",
                style=COR_DIM,
            ))
        self.console.print(Text(
            "Fale em português. ESC cancela o que estiver rodando; Ctrl+D sai.\n",
            style=COR_DIM,
        ))

    def fechar(self) -> None:
        self.console.print(Text("\naté mais. ◆", style=COR_DIM))

    # --- ler o dono ---------------------------------------------------------

    def entrada(self) -> str:
        return self.console.input(f"[bold {COR_VIVO}]❯[/] ")

    def perguntar(self, prompt: str) -> str:
        """Pergunta do cérebro ou confirmação de ação. O prompt pode conter texto
        do modelo, então vai como Text (sem markup) — só a cor é nossa."""
        self.console.print(Text(prompt, style=AMBAR), end="")
        try:
            return input()
        except EOFError:
            return ""

    # --- falar com o dono ---------------------------------------------------

    def mostrar(self, mensagem: str) -> None:
        """A resposta do Mister. `Text` de propósito: nada do que o modelo
        escreveu é interpretado como marcação."""
        self.console.print(Text("Mister ", style=f"bold {COR_VIVO}"), end="")
        self.console.print(Text(mensagem))
        self.console.print()

    def tracar(self, linha: str) -> None:
        """Bastidor: o que o cérebro pensou e fez, discreto."""
        self.console.print(Text(f"  {linha}", style=COR_DIM))

    def nota(self, texto: str) -> None:
        self.console.print(Text(texto, style=COR_DIM))

    def erro(self, texto: str) -> None:
        self.console.print(Text(f"⚠ {texto}", style=VERMELHO))

    # --- "estou ocupado" ----------------------------------------------------

    @contextmanager
    def _girando(self, spinner: str, rotulo: str):
        with _com_freio():
            with self.console.status(Text(rotulo, style=COR_DIM), spinner=spinner):
                yield

    def pensando(self):
        return self._girando(*_ATIVIDADES["pensando"])

    def atividade(self, tipo: str, detalhe: str = ""):
        spinner, rotulo = _ATIVIDADES.get(tipo, ("dots", tipo))
        return self._girando(spinner, f"{rotulo} {detalhe}".strip())


class PeleCrua:
    """Fallback sem terminal de verdade (pipe, redirecionamento, script): prints
    secos, sem spinner e sem freio — o rich sozinho já se comporta, mas o
    `status` fica piscando à toa quando ninguém está olhando."""

    def abrir(self, retomando: int = 0) -> None:
        print("◆ MISTER — pronto.")
        if retomando:
            print(f"(retomando a conversa anterior — {retomando} mensagens lembradas)")

    def fechar(self) -> None:
        print("\naté mais. ◆")

    def entrada(self) -> str:
        return input("❯ ")

    def perguntar(self, prompt: str) -> str:
        try:
            return input(prompt)
        except EOFError:
            return ""

    def mostrar(self, mensagem: str) -> None:
        print(f"Mister> {mensagem}\n")

    def tracar(self, linha: str) -> None:
        print(f"  {linha}")

    def nota(self, texto: str) -> None:
        print(texto)

    def erro(self, texto: str) -> None:
        print(f"⚠ {texto}")

    def pensando(self):
        return nullcontext()

    def atividade(self, tipo: str, detalhe: str = ""):
        return nullcontext()


def criar():
    """A pele desta sessão: a do rich quando há terminal de verdade dos dois
    lados, a crua quando a saída está indo pra um arquivo/pipe."""
    try:
        interativo = sys.stdin.isatty() and sys.stdout.isatty()
    except Exception:
        interativo = False
    return Pele() if interativo else PeleCrua()
