"""A TUI — a pele de TELA CHEIA do Mister, no desenho do OpenCode.

ESQUELETO (13/08/2026). A estrutura e a textura vêm do OpenCode, por pedido
do dono; as cores e os atalhos são dele pra mexer aos poucos:

    ┌─────────────────────────────────────────────────┐
    │ ◆ MISTER                     12 mensagens       │  cabeçalho
    ├─────────────────────────────────────────────────┤
    │ ▌ a fala do dono, em bloco destacado            │
    │                                                 │
    │ a resposta do Mister, em texto corrido          │
    │   › passo de ferramenta, em linha discreta      │  conversa (scroll)
    │                                                 │
    ├─────────────────────────────────────────────────┤
    │ ▌ caixa de digitar                              │
    │   conversa · openai/gpt-5.6-luna                │  modo + modelo
    ├─────────────────────────────────────────────────┤
    │ enter enviar · ctrl+q sair                      │  rodapé de atalhos
    └─────────────────────────────────────────────────┘

MUDAR O VISUAL: as cores moram no dicionário `CORES` e o desenho no `_CSS`,
logo abaixo — mexer ali não toca na lógica. Os atalhos moram em `BINDINGS` e
no texto `ATALHOS`.

ARQUITETURA: o app textual é o dono da THREAD PRINCIPAL (e do teclado); o
laço do agente — o mesmo do `__main__` — roda numa thread de trabalho. A
fronteira se cruza por dois caminhos só: `call_from_thread` (trabalho -> tela)
e uma fila (teclado -> trabalho). A `PeleTui` embrulha os dois caminhos no
MESMO contrato da pele simples (`interface.Pele`), então o agente não sabe em
qual das duas está falando.

Texto do modelo NUNCA vira marcação (a regra da pele simples vale aqui): tudo
que veio da API entra como `Text`, nunca como markup.

O que o esqueleto ainda NÃO tem, de propósito (vem com os ajustes do dono):
ESC cancelando o turno em andamento, retomar conversa (`--continue`/`-r`), e
contagem de tokens/custo no medidor (hoje conta mensagens).
"""
from __future__ import annotations

import queue
import threading
from contextlib import contextmanager

from rich.text import Text

# --- AS CORES (mexa à vontade) ------------------------------------------------
# A textura do OpenCode: fundo quase preto, painéis um tom acima, texto claro,
# bastidor apagado. O acento é o azul do Mister (o mesmo da pele simples).
CORES = {
    "fundo": "#0d0d0d",      # o chão da tela
    "painel": "#161616",     # cabeçalho, bloco do dono, caixa de digitar
    "texto": "#d6d6d6",      # texto normal
    "apagado": "#6f7a85",    # bastidor, medidor, atalhos, linha do modelo
    "acento": "#4cc3e6",     # a barra dos blocos, o título
    "ambar": "#d9b44a",      # pergunta/confirmação
    "vermelho": "#e0555f",   # erro
}

# O rodapé de atalhos (só texto — o comportamento mora em BINDINGS).
ATALHOS = "enter enviar · ctrl+q sair"

# --- O DESENHO (CSS do textual; $nome vem de CORES) ---------------------------
_CSS = """
Screen {
    background: $fundo;
    color: $texto;
}
#cabecalho {
    height: 3;
    background: $painel;
    padding: 0 2;
}
#titulo {
    width: 1fr;
    content-align: left middle;
    text-style: bold;
    color: $acento;
}
#medidor {
    width: auto;
    content-align: right middle;
    color: $apagado;
}
#conversa {
    height: 1fr;
    padding: 1 2;
}
.dono {
    background: $painel;
    border-left: thick $acento;
    padding: 0 2;
    margin: 0 0 1 0;
}
.mister {
    margin: 0 0 1 0;
}
.bastidor {
    color: $apagado;
    margin: 0 0 0 2;
}
.pergunta {
    color: $ambar;
    margin: 0 0 1 0;
}
.erro {
    color: $vermelho;
    margin: 0 0 1 0;
}
#caixa {
    background: $painel;
    border-left: thick $acento;
    padding: 0 1;
    height: auto;
}
#estado {
    height: 1;
    color: $apagado;
}
#entrada {
    background: $painel;
    border: none;
    padding: 0;
}
#linha_modo {
    height: 1;
    color: $apagado;
}
#atalhos {
    height: 1;
    color: $apagado;
    padding: 0 2;
}
"""


def _css() -> str:
    """O CSS pronto: troca cada $nome pela cor do dicionário."""
    css = _CSS
    for nome, cor in CORES.items():
        css = css.replace(f"${nome}", cor)
    return css


def criar_app():
    """Monta a classe do app (import do textual fica AQUI dentro: quem não
    instalou o extra ganha a recusa com receita do `main`, não um
    ImportError seco no import do módulo)."""
    from textual.app import App, ComposeResult
    from textual.binding import Binding
    from textual.containers import Horizontal, Vertical, VerticalScroll
    from textual.widgets import Input, Static

    class MisterTui(App):
        """O app: desenha a tela e faz a ponte com a thread de trabalho."""

        CSS = _css()
        BINDINGS = [Binding("ctrl+q", "quit", "sair")]

        def __init__(self) -> None:
            super().__init__()
            # O teclado -> trabalho: a PeleTui bloqueia lendo daqui.
            self.fila_do_teclado: "queue.Queue[str | None]" = queue.Queue()

        # --- a tela -----------------------------------------------------------

        def compose(self) -> ComposeResult:
            with Horizontal(id="cabecalho"):
                yield Static("◆ MISTER", id="titulo")
                yield Static("", id="medidor")
            yield VerticalScroll(id="conversa")
            with Vertical(id="caixa"):
                yield Static("", id="estado")
                yield Input(placeholder="fale com o Mister…", id="entrada")
                yield Static("", id="linha_modo")
            yield Static(ATALHOS, id="atalhos")

        def on_mount(self) -> None:
            self.query_one("#entrada", Input).focus()
            threading.Thread(target=_laco, args=(self,), daemon=True).start()

        def on_input_submitted(self, evento) -> None:
            texto = evento.value.strip()
            if not texto:
                return
            evento.input.value = ""
            # Toda fala do dono aparece no bloco destacado — inclusive resposta
            # a pergunta/confirmação. Digitada com o Mister ocupado, fica na
            # fila e entra quando ele desocupar.
            self.anexar("dono", texto)
            self.fila_do_teclado.put(texto)

        def action_quit(self) -> None:
            self.fila_do_teclado.put(None)  # acorda a thread parada no get()
            self.exit()

        # --- o que a thread de trabalho pede pra desenhar ---------------------
        # (sempre via call_from_thread — nunca direto)

        def anexar(self, classe: str, texto: str) -> None:
            """Põe uma mensagem no fim da conversa. `Text` de propósito: nada
            do que veio do modelo é interpretado como marcação."""
            conversa = self.query_one("#conversa", VerticalScroll)
            conversa.mount(Static(Text(texto), classes=classe))
            conversa.scroll_end(animate=False)

        def medir(self, texto: str) -> None:
            self.query_one("#medidor", Static).update(texto)

        def estado(self, texto: str) -> None:
            self.query_one("#estado", Static).update(
                Text(texto, style=CORES["apagado"])
            )

        def modo(self, texto: str) -> None:
            self.query_one("#linha_modo", Static).update(
                Text(texto, style=CORES["apagado"])
            )

    return MisterTui


class PeleTui:
    """O contrato da pele (o mesmo da `interface.Pele`), falando com o app.

    Vive na THREAD DE TRABALHO: desenhar é sempre `call_from_thread`; ler o
    dono é bloquear na fila do teclado."""

    def __init__(self, app) -> None:
        self.app = app

    # --- abertura e fecho -----------------------------------------------------

    def abrir(self, retomando: int = 0) -> None:
        if retomando:
            self.nota(f"(retomando a conversa anterior — {retomando} mensagens lembradas)")

    def fechar(self) -> None:
        pass  # o app fecha a tela sozinho

    # --- ler o dono -----------------------------------------------------------

    def entrada(self) -> str | None:
        """A próxima fala do dono (bloqueia até vir). None = o app fechou."""
        return self.app.fila_do_teclado.get()

    def perguntar(self, prompt: str) -> str:
        """Pergunta do cérebro ou confirmação de ação: âmbar na conversa, e a
        resposta vem pela mesma caixa de sempre."""
        self.app.call_from_thread(self.app.anexar, "pergunta", prompt.strip())
        resposta = self.app.fila_do_teclado.get()
        return "" if resposta is None else resposta

    # --- falar com o dono -----------------------------------------------------

    def mostrar(self, mensagem: str) -> None:
        self.app.call_from_thread(self.app.anexar, "mister", mensagem)

    def tracar(self, linha: str) -> None:
        self.app.call_from_thread(self.app.anexar, "bastidor", f"› {linha}")

    def nota(self, texto: str) -> None:
        self.app.call_from_thread(self.app.anexar, "bastidor", texto)

    def erro(self, texto: str) -> None:
        self.app.call_from_thread(self.app.anexar, "erro", f"⚠ {texto}")

    # --- "estou ocupado" ------------------------------------------------------

    @contextmanager
    def _ocupado(self, rotulo: str):
        self.app.call_from_thread(self.app.estado, rotulo)
        try:
            yield
        finally:
            self.app.call_from_thread(self.app.estado, "")

    def pensando(self):
        return self._ocupado("pensando…")

    def atividade(self, tipo: str, detalhe: str = ""):
        rotulo = {"pensando": "pensando…", "executando": "fazendo"}.get(tipo, tipo)
        return self._ocupado(f"{rotulo} {detalhe}".strip())


def _laco(app) -> None:
    """O laço do agente — o mesmo vai-e-vem do `__main__`, rodando na thread de
    trabalho e falando com a tela pela PeleTui."""
    from mister import conversa, envios
    from mister.agente import conversar
    from mister.brain import criar_cerebro
    from mister.dispatcher import despachar

    # As tools se cadastram no registro quando o módulo é importado.
    import mister.tools.basic  # noqa: F401
    import mister.tools.correio  # noqa: F401
    import mister.tools.memoria  # noqa: F401
    import mister.tools.regras  # noqa: F401

    pele = PeleTui(app)
    try:
        cerebro = criar_cerebro()
    except RuntimeError as erro:
        pele.erro(str(erro))
        pele.nota("dica: confira o .env (a chave da API vai em MISTER_API_KEY).")
        return

    app.call_from_thread(app.modo, f"conversa · {cerebro.modelo}")
    conversa.iniciar_sessao()
    historico: list[dict] = []
    pele.abrir(0)

    while True:
        texto = pele.entrada()
        if texto is None:
            return  # o app fechou
        try:
            historico = conversar(
                cerebro,
                despachar,
                texto,
                perguntar=pele.perguntar,
                mostrar=pele.mostrar,
                tracar=pele.tracar,
                pensando=pele.pensando,
                atividade=pele.atividade,
                historico=historico,
                recados=envios.colher_prontos,
            )
            historico = conversa.compactar(historico)
            conversa.salvar(historico)
        except Exception as erro:  # noqa: BLE001 — a thread não pode morrer calada
            pele.erro(f"o turno quebrou no meio: {erro}")
        app.call_from_thread(app.medir, f"{len(historico)} mensagens")


def main() -> None:
    """O que roda quando você digita `mister-tui`."""
    try:
        import textual  # noqa: F401
    except ImportError:
        print("A TUI precisa do extra: pip install 'mister-ai[tui]'")
        raise SystemExit(1)
    criar_app()().run()


if __name__ == "__main__":
    main()
