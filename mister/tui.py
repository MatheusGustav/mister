"""A TUI — a pele de TELA CHEIA do Mister, no desenho do OpenCode.

A estrutura, a textura E o comportamento vêm do OpenCode, por pedido do dono
(13/08/2026); as cores e os atalhos são dele pra mexer aos poucos:

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
    │ esc interromper · /comandos · ctrl+a paleta     │  rodapé de atalhos
    └─────────────────────────────────────────────────┘

O COMPORTAMENTO (decidido em 13/08/2026, na conferência da doc do OpenCode):

  - ESC INTERROMPE o turno em andamento. Honestidade: o corte vale no fim do
    passo — uma ida à API que já saiu não é derrubada no meio; o cancelamento
    acontece quando ela voltar. Derrubar no meio exige mexer no motor
    (brain._chamar), que é assunto do repo, não da pele.
  - Digitar com o Mister ocupado NÃO se perde: entra na fila e é atendido
    quando ele desocupar.
  - COMANDOS DE BARRA na caixa: /nova, /conversas, /exportar, /sair
    (sem /ajuda, decisão do dono — a paleta cumpre esse papel).
  - PALETA no ctrl+a (não ctrl+p, decisão do dono): os mesmos comandos da
    barra, pra escolher com as setas.
  - SESSÕES DENTRO DA TUI: /nova começa do zero, /conversas lista as
    guardadas e retoma a escolhida com a conversa redesenhada na tela.
  - `!` NA FRENTE roda comando no shell e mostra a saída na conversa (só
    mostra — não entra no histórico do cérebro). Sem o `@` de anexar
    arquivo, decisão do dono.

MUDAR O VISUAL: as cores moram no dicionário `CORES` e o desenho no `_CSS`,
logo abaixo — mexer ali não toca na lógica. Os atalhos moram em `BINDINGS` e
no texto `ATALHOS`; os comandos, no dicionário `COMANDOS` (a barra e a paleta
leem o MESMO — comando novo aparece nos dois lugares sozinho).

ARQUITETURA: o app textual é o dono da THREAD PRINCIPAL (e do teclado); o
laço do agente — o mesmo do `__main__` — roda numa thread de trabalho. A
fronteira se cruza por dois caminhos só: `call_from_thread` (trabalho -> tela)
e uma fila (teclado e comandos -> trabalho). A `PeleTui` embrulha os dois no
MESMO contrato da pele simples (`interface.Pele`), então o agente não sabe em
qual das duas está falando.

Texto do modelo NUNCA vira marcação (a regra da pele simples vale aqui): tudo
que veio da API entra como `Text`, nunca como markup.

O que ainda NÃO tem, de propósito: contagem de tokens/custo no medidor (hoje
conta mensagens).
"""
from __future__ import annotations

import ctypes
import os
import queue
import subprocess
import threading
from contextlib import contextmanager
from datetime import datetime
from pathlib import Path

from rich.text import Text

from mister import conversa

# --- AS CORES (mexa à vontade) ------------------------------------------------
# A paleta é o PANTERO, o gato do dono (decisão de 13/08/2026): preto, marrom
# café e o preto levemente brilhoso do pelo. O preto puro é o chão; o brilhoso
# (um preto amornado, um tom acima) é os painéis; o café é o acento — a barra
# dos blocos e o título. Texto e bastidor acompanham quentes, pra nada destoar.
CORES = {
    "fundo": "#0b0a09",      # o chão da tela — o preto do pelo
    "painel": "#191512",     # cabeçalho, bloco do dono, caixa — o preto brilhoso
    "texto": "#ddd5cc",      # texto normal — um claro quente, pra casar
    "apagado": "#80756a",    # bastidor, medidor, atalhos, linha do modelo
    "acento": "#cd9155",     # a barra dos blocos, o título — café iluminado, doce de leite
    "ambar": "#e3bc6a",      # pergunta/confirmação — mais dourado, pra não sumir no acento
    "vermelho": "#e0555f",   # erro
}

# O rodapé de atalhos (só texto — o comportamento mora em BINDINGS/COMANDOS).
ATALHOS = "esc interromper · /comandos · ctrl+a paleta · ctrl+q sair"

# Os comandos — a barra (/nome) e a paleta (ctrl+a) leem ESTE dicionário:
# comando novo entra aqui e aparece nos dois lugares. Sem /ajuda de propósito
# (decisão do dono): a paleta é quem mostra o que existe.
COMANDOS = {
    "nova": "começar uma conversa nova (a atual fica guardada)",
    "conversas": "listar as conversas guardadas e retomar uma",
    "exportar": "salvar esta conversa num arquivo .md",
    "sair": "fechar o Mister",
}

# Onde o /exportar grava (o env é o mesmo truque do resto do ~/.mister: os
# testes apontam pra longe do real).
EXPORTADAS_PADRAO = str(Path.home() / ".mister" / "exportadas")

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
Paleta, Sessoes {
    align: center middle;
}
#paleta_lista, #sessoes_lista {
    width: 64;
    max-height: 16;
    background: $painel;
    border: solid $acento;
    padding: 1;
}
"""


def _css() -> str:
    """O CSS pronto: troca cada $nome pela cor do dicionário."""
    css = _CSS
    for nome, cor in CORES.items():
        css = css.replace(f"${nome}", cor)
    return css


# --- peças puras (sem textual — testáveis a seco) -----------------------------

def blocos_da_conversa(historico: list[dict]) -> list[tuple[str, str]]:
    """A conversa como blocos de tela [(classe, texto)]: fala do dono e fala do
    Mister, na ordem. Fica de fora o que não é conversa — mensagem de sistema
    (user começando com '[') e as jogadas de ferramenta. Serve pro /conversas
    redesenhar a tela e pro /exportar escrever o arquivo."""
    blocos: list[tuple[str, str]] = []
    for mensagem in historico:
        papel = mensagem.get("role")
        conteudo = str(mensagem.get("content") or "").strip()
        if not conteudo:
            continue
        if papel == "user" and not conteudo.startswith("["):
            blocos.append(("dono", conteudo))
        elif papel == "assistant" and not mensagem.get("tool_calls"):
            blocos.append(("mister", conteudo))
    return blocos


def exportar(historico: list[dict], pasta: str | None = None) -> Path:
    """Grava a conversa num .md legível e devolve o caminho. A pasta vem do
    argumento, do env MISTER_EXPORTADAS, ou do padrão — nessa ordem."""
    destino = Path(pasta or os.environ.get("MISTER_EXPORTADAS", EXPORTADAS_PADRAO))
    destino.mkdir(parents=True, exist_ok=True)
    caminho = destino / f"conversa-{datetime.now():%Y%m%d-%H%M%S}.md"
    linhas = [f"# Conversa com o Mister — {datetime.now():%d/%m/%Y %H:%M}", ""]
    rotulos = {"dono": "**Dono:**", "mister": "**Mister:**"}
    for classe, texto in blocos_da_conversa(historico):
        linhas += [f"{rotulos[classe]} {texto}", ""]
    caminho.write_text("\n".join(linhas), encoding="utf-8")
    return caminho


def interpretar_barra(texto: str) -> str | None:
    """O nome do comando numa mensagem de barra ('/nova' -> 'nova'), ou None se
    a mensagem não é comando. Nome desconhecido volta como veio — quem avisa
    'não conheço' é a tela."""
    if not texto.startswith("/"):
        return None
    return texto[1:].strip().split()[0] if texto[1:].strip() else ""


def criar_app():
    """Monta a classe do app (import do textual fica AQUI dentro: quem não
    instalou o extra ganha a recusa com receita do `main`, não um
    ImportError seco no import do módulo)."""
    from textual.app import App, ComposeResult
    from textual.binding import Binding
    from textual.containers import Horizontal, Vertical, VerticalScroll
    from textual.screen import ModalScreen
    from textual.widgets import Input, OptionList, Static
    from textual.widgets.option_list import Option

    class Paleta(ModalScreen):
        """ctrl+a: os comandos pra escolher com as setas. Devolve o nome do
        escolhido, ou None no ESC."""

        BINDINGS = [Binding("escape", "fechar", "fechar")]

        def compose(self) -> ComposeResult:
            yield OptionList(
                *[Option(f"/{nome} — {descricao}", id=nome)
                  for nome, descricao in COMANDOS.items()],
                id="paleta_lista",
            )

        def on_option_list_option_selected(self, evento) -> None:
            self.dismiss(evento.option.id)

        def action_fechar(self) -> None:
            self.dismiss(None)

    class Sessoes(ModalScreen):
        """/conversas: as guardadas, mais novas primeiro. Devolve o caminho da
        escolhida, ou None no ESC."""

        BINDINGS = [Binding("escape", "fechar", "fechar")]

        def __init__(self, sessoes: list[dict]) -> None:
            super().__init__()
            self._sessoes = sessoes

        def compose(self) -> ComposeResult:
            yield OptionList(
                *[Option(f"{s['quando']} — {s['resumo']} ({s['n']} mensagens)",
                         id=str(s["caminho"]))
                  for s in self._sessoes],
                id="sessoes_lista",
            )

        def on_option_list_option_selected(self, evento) -> None:
            self.dismiss(evento.option.id)

        def action_fechar(self) -> None:
            self.dismiss(None)

    class MisterTui(App):
        """O app: desenha a tela e faz a ponte com a thread de trabalho."""

        CSS = _css()
        BINDINGS = [
            # priority no ctrl+a: sem ela o Input engole a tecla (é o
            # "cursor pro começo" dele) e a paleta nunca abre.
            Binding("ctrl+a", "paleta", "paleta", priority=True),
            Binding("escape", "interromper", "interromper"),
            Binding("ctrl+q", "quit", "sair"),
        ]

        def __init__(self) -> None:
            super().__init__()
            # O teclado/comandos -> trabalho: str é fala do dono; tupla
            # (comando, argumento) é ordem da tela; None é o app fechando.
            self.fila_do_teclado: "queue.Queue[object]" = queue.Queue()
            # Preenchidos pela thread de trabalho (só leitura aqui).
            self.ident_do_laco: int = 0
            self.ocupado: bool = False

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

        # --- o teclado --------------------------------------------------------

        def on_input_submitted(self, evento) -> None:
            texto = evento.value.strip()
            if not texto:
                return
            evento.input.value = ""

            comando = interpretar_barra(texto)
            if comando is not None:
                if self.ocupado:
                    self.anexar("bastidor", "(termina o turno antes de usar comandos — ESC interrompe)")
                elif comando in COMANDOS:
                    self._executar_comando(comando)
                else:
                    self.anexar("bastidor", f"(não conheço /{comando} — ctrl+a mostra os comandos)")
                return

            if texto.startswith("!"):
                # Comando de shell: mostra a saída na conversa e SÓ — não entra
                # no histórico do cérebro (é atalho do dono, não fala).
                self.anexar("dono", texto)
                self._rodar_shell(texto[1:].strip())
                return

            # Fala do dono. Com o Mister ocupado, fica na fila e entra quando
            # ele desocupar — digitado nunca se perde.
            self.anexar("dono", texto)
            self.fila_do_teclado.put(texto)

        def action_interromper(self) -> None:
            """ESC: cancela o turno em andamento. O corte vale no fim do passo
            (ver o topo do arquivo); parado, não faz nada."""
            if not (self.ocupado and self.ident_do_laco):
                return
            self.anexar("bastidor", "(interrompendo — corto no fim do passo em andamento)")
            ctypes.pythonapi.PyThreadState_SetAsyncExc(
                ctypes.c_ulong(self.ident_do_laco),
                ctypes.py_object(KeyboardInterrupt),
            )

        def action_paleta(self) -> None:
            if self.ocupado:
                self.anexar("bastidor", "(termina o turno antes de usar comandos — ESC interrompe)")
                return
            self.push_screen(Paleta(), self._da_paleta)

        def action_quit(self) -> None:
            self.fila_do_teclado.put(None)  # acorda a thread parada no get()
            self.exit()

        # --- os comandos ------------------------------------------------------

        def _da_paleta(self, comando) -> None:
            if comando:
                self._executar_comando(comando)

        def _executar_comando(self, comando: str) -> None:
            if comando == "sair":
                self.action_quit()
            elif comando == "nova":
                self.query_one("#conversa", VerticalScroll).remove_children()
                self.fila_do_teclado.put(("nova", None))
            elif comando == "exportar":
                self.fila_do_teclado.put(("exportar", None))
            elif comando == "conversas":
                sessoes = conversa.listar_sessoes()
                if not sessoes:
                    self.anexar("bastidor", "(nenhuma conversa guardada ainda)")
                    return
                self.push_screen(Sessoes(sessoes), self._da_lista_de_sessoes)

        def _da_lista_de_sessoes(self, caminho) -> None:
            if caminho:
                self.fila_do_teclado.put(("retomar", caminho))

        def _rodar_shell(self, comando: str) -> None:
            """O `!`: roda numa thread pra não travar a tela; a saída chega
            como bloco discreto quando terminar."""
            if not comando:
                self.anexar("bastidor", "(faltou o comando depois do !)")
                return

            def trabalho() -> None:
                try:
                    feito = subprocess.run(
                        comando, shell=True, capture_output=True, text=True, timeout=120
                    )
                    saida = (feito.stdout + feito.stderr).strip()
                    if not saida:
                        saida = f"(sem saída — código {feito.returncode})"
                except subprocess.TimeoutExpired:
                    saida = "(o comando estourou 120s e foi derrubado)"
                self.call_from_thread(self.anexar, "bastidor", saida)

            threading.Thread(target=trabalho, daemon=True).start()

        # --- o que a thread de trabalho pede pra desenhar ---------------------
        # (do lado de lá, sempre via call_from_thread — nunca direto)

        def anexar(self, classe: str, texto: str) -> None:
            """Põe uma mensagem no fim da conversa. `Text` de propósito: nada
            do que veio do modelo é interpretado como marcação."""
            painel = self.query_one("#conversa", VerticalScroll)
            painel.mount(Static(Text(texto), classes=classe))
            painel.scroll_end(animate=False)

        def repovoar(self, blocos: list[tuple[str, str]]) -> None:
            """Redesenha a conversa inteira (o /conversas retomando uma)."""
            painel = self.query_one("#conversa", VerticalScroll)
            painel.remove_children()
            for classe, texto in blocos:
                painel.mount(Static(Text(texto), classes=classe))
            painel.scroll_end(animate=False)

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

    def entrada(self):
        """O próximo item da fila (bloqueia até vir): fala, tupla de comando,
        ou None quando o app fechou."""
        return self.app.fila_do_teclado.get()

    def perguntar(self, prompt: str) -> str:
        """Pergunta do cérebro ou confirmação de ação: âmbar na conversa, e a
        resposta vem pela mesma caixa de sempre. Comando de barra não vale
        como resposta (a tela já os bloqueia com o Mister ocupado)."""
        self.app.call_from_thread(self.app.anexar, "pergunta", prompt.strip())
        resposta = self.app.fila_do_teclado.get()
        return resposta if isinstance(resposta, str) else ""

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
    trabalho e falando com a tela pela PeleTui. Também atende as ordens da
    tela (as tuplas de comando), porque o histórico mora aqui."""
    from mister import envios
    from mister.agente import conversar
    from mister.brain import criar_cerebro
    from mister.dispatcher import despachar

    # As tools se cadastram no registro quando o módulo é importado.
    import mister.tools.basic  # noqa: F401
    import mister.tools.correio  # noqa: F401
    import mister.tools.memoria  # noqa: F401
    import mister.tools.regras  # noqa: F401

    app.ident_do_laco = threading.get_ident()
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
        try:
            item = pele.entrada()
            if item is None:
                return  # o app fechou

            if isinstance(item, tuple):  # ordem da tela, não fala do dono
                nome, argumento = item
                if nome == "nova":
                    conversa.iniciar_sessao()
                    historico = []
                    pele.nota("(conversa nova — a anterior ficou guardada)")
                elif nome == "retomar":
                    conversa.iniciar_sessao(argumento)
                    historico = conversa.carregar_sessao(argumento)
                    app.call_from_thread(app.repovoar, blocos_da_conversa(historico))
                    pele.nota(f"(retomada — {len(historico)} mensagens lembradas)")
                elif nome == "exportar":
                    try:
                        caminho = exportar(historico)
                        pele.nota(f"(conversa exportada em {caminho})")
                    except OSError as erro:
                        pele.erro(f"não consegui exportar: {erro}")
                app.call_from_thread(app.medir, f"{len(historico)} mensagens")
                continue

            app.ocupado = True
            try:
                historico = conversar(
                    cerebro,
                    despachar,
                    item,
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
            finally:
                app.ocupado = False
            app.call_from_thread(app.medir, f"{len(historico)} mensagens")

        except KeyboardInterrupt:
            # O ESC da tela: cancela SÓ este turno; a conversa segue de pé.
            app.ocupado = False
            pele.nota("(cancelado)")
        except Exception as erro:  # noqa: BLE001 — a thread não pode morrer calada
            app.ocupado = False
            pele.erro(f"o turno quebrou no meio: {erro}")


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
