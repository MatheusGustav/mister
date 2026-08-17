"""A TUI — a pele de TELA CHEIA do Mister, no desenho do OpenCode.

A estrutura, a textura E o comportamento vêm do OpenCode, por pedido do dono
(13/08/2026); as cores e os atalhos são dele pra mexer aos poucos:

    ┌──────────────────────────────────┬────────────────────┐
    │ ▌ a fala do dono, em bloco       │      ▗▟█           │
    │                                  │    ▟█████▙         │  o Pantero
    │ a resposta do Mister             │   ███████▛         │
    │   › passo de ferramenta          │                    │
    │                                  │ Contexto           │
    │                                  │ ██████░░░░░░  38%  │  o painel da
    │                                  │ 24.1k / 128k tokens│  direita (ctrl+b)
    ├──────────────────────────────────┤                    │
    │   conversa · openai/gpt-5.6-luna │ Arquivos mexidos   │
    │ ▌ caixa de digitar               │ ~/notas/hoje.md    │
    ├──────────────────────────────────┤                    │
    │ esc interromper · ctrl+a comandos│ Tarefas  1/3 feitas│
    └──────────────────────────────────┴────────────────────┘

Sem cabeçalho (decisão do dono): a conversa começa no topo da esquerda. À
direita, o PAINEL — 42 colunas fixas, com rolagem própria.

O COMPORTAMENTO (decidido em 13/08/2026, na conferência da doc do OpenCode):

  - ESC INTERROMPE o turno em andamento, COOPERATIVO: o pedido fica anotado
    num Event e a PeleTui o transforma em KeyboardInterrupt no fecho do passo
    (ou na hora, se o Mister estava esperando o dono responder uma pergunta —
    aí um sentinela acorda a fila de resposta). Honestidade: uma ida à API que
    já saiu não é derrubada no meio — o corte vale quando ela voltar; derrubar
    no meio exige mexer no motor (brain._chamar), que é assunto do repo, não
    da pele. (Já foi PyThreadState_SetAsyncExc: a exceção assíncrona não
    acordava a thread parada na fila, aterrissava atrasada comendo a fala
    seguinte, e um ESC duplo podia matar o laço calado.)
  - Digitar com o Mister ocupado NÃO se perde: entra na fila e é atendido
    quando ele desocupar. Resposta de pergunta vai por FILA PRÓPRIA — fala
    que já estava na outra fila nunca é consumida como resposta.
  - COMANDOS DE BARRA na caixa: /nova, /conversas, /exportar, /sair
    (sem /ajuda, decisão do dono — a paleta cumpre esse papel).
  - PALETA no ctrl+a (não ctrl+p, decisão do dono): os mesmos comandos da
    barra, pra escolher com as setas.
  - SESSÕES DENTRO DA TUI: /nova começa do zero, /conversas lista as
    guardadas e retoma a escolhida com a conversa redesenhada na tela.
  - `!` NA FRENTE roda comando no shell e mostra a saída na conversa (só
    mostra — não entra no histórico do cérebro). Sem o `@` de anexar
    arquivo, decisão do dono.
  - O RELÓGIO DO REVISAR: 10 min do dono parado (janela aberta) e o laço
    dispara UMA passada de arrumação das notas em segundo plano — ver
    `mister/revisar.py`; rearma na próxima mensagem.
  - /interruptores: os botões do dono — anotar, revisar e celular — pra
    ligar/desligar na tela (`mister/interruptores.py`).
  - O PAINEL DA DIREITA (copiado do OpenCode, por pedido do dono): o brasão,
    o uso do contexto, os arquivos que o Mister gravou nesta conversa e a
    lista de tarefas do pedido atual. Aparece SOZINHO com mais de 120 colunas
    de tela — aí as duas colunas dividem o espaço. Com 120 ou menos fica
    escondido, e ligar na mão (ctrl+b) o traz POR CIMA da conversa, encostado
    na direita, sem encolher ninguém. A escolha manual sempre ganha do
    automático. Ele relê o estado sozinho de segundo em segundo: é tudo o
    mesmo processo, não passa recado pela thread de trabalho.

MUDAR O VISUAL: as cores moram no dicionário `CORES` e o desenho no `_CSS`,
logo abaixo — mexer ali não toca na lógica. Os atalhos moram em `BINDINGS` e
no texto `ATALHOS`; os comandos, no dicionário `COMANDOS` (a barra e a paleta
leem o MESMO — comando novo aparece nos dois lugares sozinho).

ARQUITETURA: o app textual é o dono da THREAD PRINCIPAL (e do teclado); o
laço do agente — o mesmo do `__main__` — roda numa thread de trabalho. A
fronteira se cruza por dois caminhos só: `call_from_thread` (trabalho -> tela)
e as filas (teclado/comandos e respostas de pergunta -> trabalho). A `PeleTui` embrulha os dois no
MESMO contrato da pele simples (`interface.Pele`), então o agente não sabe em
qual das duas está falando.

Texto do modelo NUNCA vira marcação (a regra da pele simples vale aqui): tudo
que veio da API entra como `Text`, nunca como markup.

O que ainda NÃO tem, de propósito: contagem de tokens/custo no medidor (hoje
conta mensagens).
"""
from __future__ import annotations

import os
import queue
import time
import subprocess
import threading
from contextlib import contextmanager
from datetime import datetime
from pathlib import Path

from rich.text import Text

from mister import contexto, conversa, interruptores, memoria, mexidos, tarefas

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
    "acento": "#b87f4c",     # o título e as bordas de destaque — doce de leite
    "barra": "#77522a",      # a barra na esquerda dos blocos — café fechado
    "ambar": "#e3bc6a",      # pergunta/confirmação — mais dourado, pra não sumir no acento
    "vermelho": "#e0555f",   # erro
}

# --- O BRASÃO DO PAINEL -------------------------------------------------------
# O Pantero, o gato do dono — o mesmo que dá nome à paleta aí em cima. Assim o
# painel abre com a identidade que as cores da tela já contam, em vez de um
# logo genérico colado por cima.
#
# A técnica são os caracteres de QUADRANTE do Unicode (▖▗▘▝▚▞▙▟▛▜▌▐▀▄█): cada
# um divide a célula do terminal em 2×2, então 24 colunas × 12 linhas viram uma
# grade de 48×24 pontos — resolução de sobra pra silhueta, que só tem duas
# cores. Meio-bloco (▀▄█, 1×2 por célula) foi testado e serrilha as pernas e a
# cauda: não usar.
#
# Tamanho 24×12 e cor `acento`, decisão do dono (17/08/2026). Cabe com folga
# nas 36 colunas úteis do painel (42 menos o padding de 2+2 e a barra de
# rolagem do textual, que come mais 2). As versões média (30×15) e grande
# (36×18) estão no doc do painel.
#
# NÃO ENCOLHER ESTA ARTE. Os olhos aqui são BURACO na silhueta, e buraco fino
# some quando a resolução cai — foi assim que o olho esquerdo se perdeu na
# redução anterior. Tamanho novo se faz REGERANDO da foto de referência
# (~/Imagens/Capturas de tela/Captura_de_tela_20260816_230227.png) pela receita
# do doc do painel: PPM em texto do ImageMagick (esta máquina não tem PIL nem
# numpy), máscara de corpo (média RGB < 128), corta a moldura, aperta na caixa,
# reamostra por média de área numa grade de 2·colunas × 2·linhas e rebinariza
# no corte 0,45.
#
# ATENÇÃO AO MEXER: os espaços do começo de cada linha FAZEM PARTE do desenho.
# Editor que apara espaço à esquerda ou à direita entorta o gato.
#
# O OLHO ESQUERDO foi consertado na mão (17/08/2026): a redução pra 24×12 tinha
# comido o topo e a base do anel dele, e o que sobrava eram dois riscos
# verticais em vez de olho. Os dois olhos agora são anel — um buraco em volta
# de uma pupila cheia —, como no direito, que sobreviveu inteiro.
BRASAO = (
    "                     ▄█▖\n"
    "                   ▗████\n"
    "                   ████▛\n"
    "▄▄    ▗▟█    ▄    ▐████▘\n"
    "█████████▙██████▙▄████▘\n"
    "▜█▚▞█▛▄▝█████████████▘\n"
    "▐█▄▄██▄▟████████████\n"
    " ▀▜█████████████████▖\n"
    "    ▐███████▀▀▜█████▌\n"
    "    ███▜██▛   ▝██▛██▌\n"
    "   ▟██▘██▛     ██▌▜█▌\n"
    "   █▀ ▐█▛      ▝▛ ▝█▘"
)

# O rodapé de atalhos (só texto — o comportamento mora em BINDINGS/COMANDOS).
# Curto de propósito (decisão do dono): o caderno do ctrl+a é quem mostra o
# que existe — o ESC e a barra continuam funcionando, só não moram aqui. O
# ctrl+b do painel entrou a pedido dele (17/08/2026).
ATALHOS = "ctrl+a comandos · ctrl+b painel · ctrl+s sair"

# O PAINEL DA DIREITA (copiado do OpenCode, por pedido do dono): 42 colunas
# fixas, e ele aparece SOZINHO só quando a tela tem mais de 120 colunas — daí
# pra baixo fica escondido, e ligar na mão (ctrl+b) o traz POR CIMA da conversa
# em vez de dividir espaço com ela. A escolha manual sempre ganha do automático.
LARGURA_PAINEL = 42
COLUNAS_PRO_PAINEL = 120
# O que sobra pra escrever dentro do painel: 42 menos o padding (2+2) e menos a
# barra de rolagem do textual 8.2.8 (scrollbar_size_vertical = 2). É a medida
# do brasão e o teto pra encurtar caminho de arquivo.
LARGURA_UTIL_PAINEL = LARGURA_PAINEL - 4 - 2
# De quanto em quanto tempo o painel relê o estado (contexto, arquivos,
# tarefas). É tudo o mesmo processo — ler é barato, não precisa de recado da
# thread de trabalho.
INTERVALO_PAINEL_S = 1.0

# Os comandos — a barra (/nome) e a paleta (ctrl+a) leem ESTE dicionário:
# comando novo entra aqui e aparece nos dois lugares. Sem /ajuda de propósito
# (decisão do dono): a paleta é quem mostra o que existe.
COMANDOS = {
    "nova": "começar uma conversa nova (a atual fica guardada)",
    "conversas": "listar as conversas guardadas e retomar uma",
    "exportar": "salvar esta conversa num arquivo .md",
    "interruptores": "ligar/desligar o anotar, o revisar e o celular",
    "sair": "fechar o Mister",
}

# O gatilho do revisar: o dono parado este tanto de tempo (janela aberta) e o
# Mister vai arrumar as próprias notas em segundo plano. Uma passada só — o
# gatilho rearma na próxima mensagem. Ver mister/revisar.py.
OCIOSIDADE_S = 10 * 60

# Onde o /exportar grava (o env é o mesmo truque do resto do ~/.mister: os
# testes apontam pra longe do real).
EXPORTADAS_PADRAO = str(Path.home() / ".mister" / "exportadas")

# --- O DESENHO (CSS do textual; $nome vem de CORES) ---------------------------
_CSS = """
Screen {
    background: $fundo;
    color: $texto;
}
/* As duas colunas: a conversa (tudo o que sempre existiu) e o painel. A
   camada 'sobre' é o que deixa o painel desenhar POR CIMA em tela estreita,
   sem tirar espaço da conversa — na camada de baixo ele nem conta. */
#corpo {
    layers: base sobre;
}
#coluna {
    width: 1fr;
    height: 100%;
}
#painel {
    width: 42;
    height: 100%;
    background: $painel;
    padding: 1 2;
}
#painel.escondido {
    display: none;
}
#painel.sobreposto {
    layer: sobre;
    dock: right;
}
.painel_secao {
    margin: 0 0 1 0;
}
#medidor {
    width: auto;
    color: $apagado;
}
#conversa {
    height: 1fr;
    padding: 1 2;
}
.dono {
    background: $painel;
    border-left: thick $barra;
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
.espera {
    color: $apagado;
    text-style: italic;
    margin: 0 0 1 0;
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
    border-left: thick $barra;
    padding: 0 1;
    height: auto;
}
/* Sem border/padding aqui de propósito: quem zera os dois é o `compact=True`
   do Input (o CSS padrão dele no textual 8.2.8 traz `height: 3`, e era isso
   que fazia a caixa comer 4 linhas de tela). */
#entrada {
    background: $painel;
}
#linha_modo {
    height: 1;
    color: $apagado;
}
#rodape {
    height: 1;
    padding: 0 2;
}
#atalhos {
    width: 1fr;
    color: $apagado;
}
Paleta, Sessoes, Interruptores {
    align: center middle;
}
#paleta_lista, #sessoes_lista, #interruptores_lista {
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


# O sentinela do ESC numa pergunta pendente: o action_interromper o põe na
# fila de RESPOSTA pra acordar a thread parada; a PeleTui o transforma em
# KeyboardInterrupt na hora — sem exceção assíncrona.
CANCELAR = object()


def brasao(largura: int = LARGURA_UTIL_PAINEL) -> Text:
    """O Pantero pronto pra tela, na cor escolhida e CENTRADO na largura útil
    do painel.

    O recuo é UM SÓ, o mesmo pra todas as linhas: centrar cada linha por conta
    (o `text-align: center` do CSS) embaralharia o desenho, porque as linhas
    têm comprimentos diferentes de propósito. Quem manda é a caixa do gato
    inteiro, não a linha.

    `no_wrap` pelo mesmo motivo: se a largura apertar, a arte é CORTADA na
    direita — quebrar linha embaralha o desenho todo."""
    linhas = BRASAO.split("\n")
    caixa = max(len(linha) for linha in linhas)
    recuo = " " * max(0, (largura - caixa) // 2)
    return Text(
        "\n".join(recuo + linha for linha in linhas),
        style=CORES["acento"],
        no_wrap=True,
    )


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
            # Text de propósito: o resumo é a 1ª FALA DO DONO — string crua o
            # OptionList interpreta como markup ('[/red]' na fala derrubava o
            # app inteiro com MarkupError).
            yield OptionList(
                *[Option(Text(f"{s['quando']} — {s['resumo']} ({s['n']} mensagens)"),
                         id=str(s["caminho"]))
                  for s in self._sessoes],
                id="sessoes_lista",
            )

        def on_option_list_option_selected(self, evento) -> None:
            self.dismiss(evento.option.id)

        def action_fechar(self) -> None:
            self.dismiss(None)

    class Interruptores(ModalScreen):
        """/interruptores: os botões do dono. Enter alterna o apontado (a
        lista se redesenha no lugar); ESC fecha."""

        BINDINGS = [Binding("escape", "fechar", "fechar")]

        def compose(self) -> ComposeResult:
            yield OptionList(*self._opcoes(), id="interruptores_lista")

        def _opcoes(self) -> list:
            # Text de propósito: como string crua, o '[ligado]' era engolido
            # pelo parser de markup do OptionList e nunca aparecia na tela.
            return [
                Option(
                    Text(
                        f"{'●' if interruptores.ligado(nome) else '○'} {nome} — "
                        f"{descricao} "
                        f"[{'ligado' if interruptores.ligado(nome) else 'DESLIGADO'}]"
                    ),
                    id=nome,
                )
                for nome, descricao in interruptores.NOMES.items()
            ]

        def on_option_list_option_selected(self, evento) -> None:
            nome = evento.option.id
            novo = interruptores.alternar(nome)
            lista = self.query_one(OptionList)
            apontado = lista.highlighted
            lista.clear_options()
            lista.add_options(self._opcoes())
            lista.highlighted = apontado
            self.app.anexar(
                "bastidor",
                f"(interruptor '{nome}' agora {'ligado' if novo else 'desligado'})",
            )

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
            # ctrl+s é o do rodapé (decisão do dono); o ctrl+q fica de
            # reserva muda — custa nada e salva quem tem o dedo viciado.
            Binding("ctrl+s", "quit", "sair", priority=True),
            Binding("ctrl+q", "quit", "sair"),
            # ctrl+b (decisão do dono): liga/desliga o painel da direita. O
            # Input do textual 8.2.8 não usa essa tecla, então o priority não
            # é obrigatório — vai por consistência com os dois de cima.
            Binding("ctrl+b", "painel", "painel", priority=True),
        ]

        def __init__(self) -> None:
            super().__init__()
            # O teclado/comandos -> trabalho: str é fala do dono; tupla
            # (comando, argumento) é ordem da tela; None é o app fechando.
            self.fila_do_teclado: "queue.Queue[object]" = queue.Queue()
            # Resposta de pergunta/confirmação vem por FILA PRÓPRIA: fala que
            # já estava na outra fila (digitada ANTES da pergunta aparecer)
            # continua sendo promessa de próximo turno, nunca vira resposta.
            self.fila_de_resposta: "queue.Queue[object]" = queue.Queue()
            # Ligada pela PeleTui enquanto uma pergunta espera resposta.
            self.aguardando_resposta: bool = False
            # O pedido do ESC: a PeleTui confere no fecho de cada passo.
            self.cancelar = threading.Event()
            # Preenchido pela thread de trabalho (só leitura aqui).
            self.ocupado: bool = False
            # O cérebro, posto aqui pelo _laco: é dele que o painel tira o
            # `ultimo_uso` pra desenhar a barra de contexto.
            self.cerebro = None
            # O painel: None = automático (decide pela largura da tela); True
            # ou False = o dono escolheu na mão e a escolha dele ganha.
            self._painel_manual: bool | None = None

        # --- a tela -----------------------------------------------------------

        def compose(self) -> ComposeResult:
            # Sem cabeçalho, decisão do dono (14/08/2026): a conversa começa
            # no topo; o medidor mora no rodapé, à direita.
            with Horizontal(id="corpo"):
                with Vertical(id="coluna"):
                    yield VerticalScroll(id="conversa")
                    with Vertical(id="caixa"):
                        # A linha do modo/modelo fica EM CIMA da caixa (decisão
                        # do dono, 14/08/2026). Sem linha de "pensando…" —
                        # excluída a pedido dele; o andamento aparece só pelo
                        # bastidor.
                        yield Static("", id="linha_modo")
                        # compact=True: é o modo do próprio textual que zera
                        # border/padding e deixa a caixa com UMA linha. Sem
                        # ele, o CSS padrão do Input (height: 3) comia duas
                        # linhas de tela à toa.
                        yield Input(
                            placeholder="fale com o Mister…",
                            id="entrada",
                            compact=True,
                        )
                    with Horizontal(id="rodape"):
                        yield Static(ATALHOS, id="atalhos")
                        yield Static("", id="medidor")
                with VerticalScroll(id="painel"):
                    # Rolagem própria: lista de arquivo comprida não pode
                    # empurrar a barra de contexto pra fora da tela.
                    yield Static("", id="brasao")
                    yield Static("", id="painel_contexto", classes="painel_secao")
                    yield Static("", id="painel_arquivos", classes="painel_secao")
                    yield Static("", id="painel_tarefas", classes="painel_secao")

        def on_mount(self) -> None:
            self.query_one("#entrada", Input).focus()
            # O relógio do revisar: confere a cada 15s se o dono está parado
            # há OCIOSIDADE_S. `_revisao_armada` garante UMA passada por
            # ociosidade — rearma quando ele digitar de novo.
            self._ultimo_toque = time.monotonic()
            self._revisao_armada = True
            self.set_interval(15, self._checar_ociosidade)
            self.query_one("#brasao", Static).update(brasao())
            self._arrumar_painel()
            # O painel relê o estado sozinho, de segundo em segundo: contexto,
            # arquivos e tarefas moram todos neste processo.
            self.set_interval(INTERVALO_PAINEL_S, self._atualizar_painel)
            threading.Thread(target=_laco, args=(self,), daemon=True).start()

        # --- o painel da direita ---------------------------------------------

        def on_resize(self, evento) -> None:
            self._arrumar_painel()

        def _painel_visivel(self) -> bool:
            """Mostra o painel? A escolha MANUAL ganha do automático (ligou na
            mão, fica ligado mesmo em tela estreita; desligou, fica desligado
            mesmo em tela larga). Sem escolha manual, é a largura que manda."""
            if self._painel_manual is not None:
                return self._painel_manual
            return self.size.width > COLUNAS_PRO_PAINEL

        def _arrumar_painel(self) -> None:
            """Põe o painel no lugar certo: escondido, do LADO (tela larga, as
            duas colunas dividem o espaço) ou POR CIMA (tela estreita, ele vai
            pra camada de sobreposição e a conversa não encolhe)."""
            # O Resize pode chegar antes da tela existir (na subida do app):
            # sem painel montado ainda não há o que arrumar.
            achados = self.query("#painel")
            if not achados:
                return
            painel = achados.first(VerticalScroll)
            visivel = self._painel_visivel()
            painel.set_class(not visivel, "escondido")
            painel.set_class(
                visivel and self.size.width <= COLUNAS_PRO_PAINEL, "sobreposto"
            )
            if visivel:
                self._atualizar_painel()

        def action_painel(self) -> None:
            """ctrl+b: liga/desliga o painel na mão. Aviso pra quem for caçar
            bug: DENTRO DO TMUX o ctrl+b é o prefixo do próprio tmux e nunca
            chega aqui — se 'não funcionar', é isso antes de qualquer coisa."""
            self._painel_manual = not self._painel_visivel()
            self._arrumar_painel()

        def _atualizar_painel(self) -> None:
            """Relê o estado e redesenha as três seções. Painel escondido nem
            gasta desenho."""
            if not self._painel_visivel():
                return
            self.query_one("#painel_contexto", Static).update(self._secao_contexto())
            self.query_one("#painel_arquivos", Static).update(self._secao_arquivos())
            self.query_one("#painel_tarefas", Static).update(self._secao_tarefas())

        @staticmethod
        def _titulo(nome: str, cauda: str = "") -> Text:
            texto = Text(nome, style=f"bold {CORES['acento']}")
            if cauda:
                texto.append(f"  {cauda}", style=CORES["apagado"])
            texto.append("\n")
            return texto

        def _secao_contexto(self) -> Text:
            """A barra de ocupação da janela do modelo. O número é o
            `prompt_tokens` da ÚLTIMA chamada — o tamanho ATUAL do contexto,
            não a soma do que já se gastou (ver mister/contexto.py)."""
            cerebro = getattr(self, "cerebro", None)
            uso = getattr(cerebro, "ultimo_uso", None) or {}
            try:
                usados = int(uso.get("prompt_tokens") or 0)
            except (TypeError, ValueError):
                usados = 0
            total, estimado = contexto.teto(getattr(cerebro, "modelo", "") or "")

            texto = self._titulo("Contexto")
            linhas = contexto.barra(usados, total)
            if usados <= 0:
                texto.append(linhas[0], style=CORES["apagado"])
                return texto
            cor = {
                "normal": CORES["acento"],
                "alto": CORES["ambar"],
                "critico": CORES["vermelho"],
            }[contexto.nivel(usados, total)]
            desenho, conta = linhas
            cheias = desenho.count(contexto.CHEIO)
            vazias = desenho.count(contexto.VAZIO)
            texto.append(contexto.CHEIO * cheias, style=cor)
            texto.append(contexto.VAZIO * vazias, style=CORES["apagado"])
            texto.append(desenho[cheias + vazias:], style=CORES["apagado"])
            texto.append(f"\n{conta}", style=CORES["apagado"])
            if estimado:
                # Barra sem aviso é barra que mente: o teto de reserva tem que
                # aparecer como reserva.
                texto.append("\n(teto estimado)", style=CORES["apagado"])
            return texto

        def _secao_arquivos(self) -> Text:
            """Os arquivos que o Mister GRAVOU nesta conversa, do mais novo pro
            mais antigo. `Text` de propósito: caminho vem do mundo, não vira
            marcação."""
            caminhos = mexidos.listar()
            texto = self._titulo("Arquivos mexidos")
            if not caminhos:
                texto.append("nenhum ainda", style=CORES["apagado"])
                return texto
            for i, caminho in enumerate(caminhos):
                if i:
                    texto.append("\n")
                texto.append(
                    mexidos.encurtar(caminho, LARGURA_UTIL_PAINEL),
                    style=CORES["texto"],
                )
            return texto

        def _secao_tarefas(self) -> Text:
            """O plano do pedido atual, do jeito que o cérebro anotou pela tool
            `lista_de_tarefas`."""
            itens = tarefas.listar()
            texto = self._titulo("Tarefas", tarefas.resumo())
            if not itens:
                texto.append("nenhuma ainda", style=CORES["apagado"])
                return texto
            marcas = {"feito": "✓", "fazendo": "▸", "pendente": "○"}
            cores = {
                "feito": CORES["apagado"],
                "fazendo": CORES["ambar"],
                "pendente": CORES["texto"],
            }
            for i, item in enumerate(itens):
                if i:
                    texto.append("\n")
                estado = item["estado"]
                texto.append(
                    f"{marcas[estado]} {item['texto']}", style=cores[estado]
                )
            return texto

        def _checar_ociosidade(self) -> None:
            if not self._revisao_armada or self.ocupado:
                return
            if time.monotonic() - self._ultimo_toque < OCIOSIDADE_S:
                return
            # Interruptor desligado ou grafo vazio: nem dispara (e continua
            # armado — se o dono ligar o botão, a próxima conferida pega).
            if not interruptores.ligado("revisar") or not memoria.listar():
                return
            self._revisao_armada = False
            self.fila_do_teclado.put(("revisar", None))

        # --- o teclado --------------------------------------------------------

        def on_input_changed(self, evento) -> None:
            # Digitar SEM enviar também é atividade: o relógio do revisar não
            # pode disparar com o dono no meio de uma frase longa.
            self._ultimo_toque = time.monotonic()

        def on_input_submitted(self, evento) -> None:
            self._ultimo_toque = time.monotonic()
            self._revisao_armada = True  # o gatilho do revisar rearma aqui
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

            # Fala do dono. Com uma pergunta na tela, é a RESPOSTA dela (fila
            # própria); senão, fica na fila do teclado e entra quando o Mister
            # desocupar — digitado nunca se perde.
            self.anexar("dono", texto)
            if self.aguardando_resposta:
                self.fila_de_resposta.put(texto)
            else:
                self.fila_do_teclado.put(texto)

        def action_interromper(self) -> None:
            """ESC: cancela o turno em andamento — cooperativo, o corte vale
            no fim do passo (ver o topo do arquivo); parado, não faz nada. A
            ORDEM importa: primeiro o Event, depois olhar a pergunta — é o que
            garante que uma pergunta abrindo neste exato instante ou vê o
            Event, ou recebe o sentinela (nunca escapa dos dois)."""
            if not self.ocupado:
                return
            self.anexar("bastidor", "(interrompendo — corto no fim do passo em andamento)")
            self.cancelar.set()
            if self.aguardando_resposta:
                self.fila_de_resposta.put(CANCELAR)  # acorda a pergunta parada

        def action_paleta(self) -> None:
            if self.ocupado:
                self.anexar("bastidor", "(termina o turno antes de usar comandos — ESC interrompe)")
                return
            self.push_screen(Paleta(), self._da_paleta)

        def action_quit(self) -> None:
            self.fila_do_teclado.put(None)   # acorda a thread parada no get()
            self.fila_de_resposta.put(None)  # ...ou parada esperando resposta
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
            elif comando == "interruptores":
                self.push_screen(Interruptores())
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

        # O giro da espera (os mesmos quadros do spinner "dots" do rich).
        QUADROS_ESPERA = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

        def mostrar_espera(self, rotulo: str) -> None:
            """O aviso de espera, no pé da conversa — onde a resposta vai
            surgir: o desenho girando na frente do rótulo. Um por vez: o novo
            tira o anterior. O widget vai por REFERÊNCIA, sem id fixo — o
            remove() do textual é assíncrono, e um id fixo colidia
            (DuplicateIds) se dois mostrar caíssem no mesmo tick."""
            self.tirar_espera()
            self._rotulo_espera = rotulo
            self._quadro_espera = 0
            painel = self.query_one("#conversa", VerticalScroll)
            self._peca_espera = Static(
                Text(f"{self.QUADROS_ESPERA[0]} {rotulo}"), classes="espera"
            )
            painel.mount(self._peca_espera)
            painel.scroll_end(animate=False)
            self._relogio_espera = self.set_interval(0.08, self._girar_espera)

        def _girar_espera(self) -> None:
            peca = getattr(self, "_peca_espera", None)
            if peca is None:
                return
            self._quadro_espera = (self._quadro_espera + 1) % len(self.QUADROS_ESPERA)
            peca.update(Text(
                f"{self.QUADROS_ESPERA[self._quadro_espera]} {self._rotulo_espera}"
            ))

        def tirar_espera(self) -> None:
            relogio = getattr(self, "_relogio_espera", None)
            if relogio is not None:
                relogio.stop()
                self._relogio_espera = None
            peca = getattr(self, "_peca_espera", None)
            if peca is not None:
                self._peca_espera = None
                peca.remove()

        def medir(self, texto: str) -> None:
            self.query_one("#medidor", Static).update(texto)

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
        resposta vem pela mesma caixa — mas pela fila DE RESPOSTA: só vale o
        que o dono digitar com a pergunta na tela (fala enfileirada antes fica
        guardada pro próximo turno, como prometido). ESC aqui dentro chega
        como o sentinela CANCELAR e cancela na hora. Comando de barra não vale
        como resposta (a tela já os bloqueia com o Mister ocupado)."""
        app = self.app
        # Sobra de rodada passada (um ESC que chegou tarde demais) não pode
        # valer como resposta desta pergunta: esvazia antes de perguntar.
        while True:
            try:
                app.fila_de_resposta.get_nowait()
            except queue.Empty:
                break
        app.aguardando_resposta = True
        try:
            # ESC que chegou ENTRE o passo anterior e a pergunta: o Event já
            # está de pé, e o sentinela não veio (a fila ainda não esperava).
            if app.cancelar.is_set():
                raise KeyboardInterrupt
            app.call_from_thread(app.anexar, "pergunta", prompt.strip())
            resposta = app.fila_de_resposta.get()
        finally:
            app.aguardando_resposta = False
        if resposta is CANCELAR:
            raise KeyboardInterrupt
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

    # O "pensando…" aparece NO FIM DA CONVERSA — no lugar onde a resposta vai
    # surgir (decisão do dono, 14/08/2026) — e sai quando o passo termina.
    @contextmanager
    def _ocupado(self, rotulo: str):
        self.app.call_from_thread(self.app.mostrar_espera, rotulo)
        try:
            yield
        finally:
            self.app.call_from_thread(self.app.tirar_espera)
            # O corte do ESC, cooperativo: SEMPRE aqui, no fecho de um passo
            # — nunca no meio de bytecode alheio como era com o SetAsyncExc.
            if self.app.cancelar.is_set():
                raise KeyboardInterrupt

    def pensando(self):
        return self._ocupado("pensando")

    def atividade(self, tipo: str, detalhe: str = ""):
        rotulo = {"pensando": "pensando", "executando": "fazendo"}.get(tipo, tipo)
        return self._ocupado(f"{rotulo} {detalhe}".strip())


def _laco(app) -> None:
    """O laço do agente — o mesmo vai-e-vem do `__main__`, rodando na thread de
    trabalho e falando com a tela pela PeleTui. Também atende as ordens da
    tela (as tuplas de comando), porque o histórico mora aqui."""
    from mister import envios, revisar
    from mister.agente import conversar
    from mister.brain import criar_cerebro
    from mister.dispatcher import despachar

    # As tools se cadastram no registro quando o módulo é importado.
    import mister.tools.basic  # noqa: F401
    import mister.tools.correio  # noqa: F401
    import mister.tools.maquina  # noqa: F401
    import mister.tools.memoria  # noqa: F401
    import mister.tools.regras  # noqa: F401
    import mister.tools.tarefas  # noqa: F401

    pele = PeleTui(app)
    try:
        cerebro = criar_cerebro()
    except RuntimeError as erro:
        pele.erro(str(erro))
        pele.nota("dica: confira o .env (a chave da API vai em MISTER_API_KEY).")
        return

    # O painel precisa do cérebro pra ler o `ultimo_uso` (a barra de contexto)
    # — sem esta linha ele não tem de onde tirar o número.
    app.cerebro = cerebro
    # O teto de contexto do modelo vem da OpenRouter e fica em cache. Vai numa
    # thread à parte de propósito: uma ida à rede aqui atrasaria a primeira
    # fala do dono. Não deu (sem rede, modelo fora da lista)? O painel usa o
    # teto de reserva e AVISA que é estimado.
    threading.Thread(
        target=lambda: contexto.atualizar(cerebro.modelo), daemon=True
    ).start()

    app.call_from_thread(app.modo, f"conversa · {cerebro.modelo}")
    app.call_from_thread(app.medir, "0 mensagens")
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
                    # O painel é da CONVERSA, não do processo: conversa nova
                    # começa com a lista de arquivos e a de tarefas zeradas.
                    mexidos.limpar()
                    tarefas.limpar()
                    pele.nota("(conversa nova — a anterior ficou guardada)")
                elif nome == "retomar":
                    conversa.iniciar_sessao(argumento)
                    historico = conversa.carregar_sessao(argumento)
                    mexidos.limpar()
                    tarefas.limpar()
                    app.call_from_thread(app.repovoar, blocos_da_conversa(historico))
                    pele.nota(f"(retomada — {len(historico)} mensagens lembradas)")
                elif nome == "revisar":
                    # O gatilho da ociosidade (uma passada; ver revisar.py).
                    # Roda pelo envios: a fala de desfecho chega como recado
                    # no turno seguinte, o caminho de sempre. Passada anterior
                    # ainda em voo (elas podem passar de 10 min)? Não empilha
                    # — duas revisões juntas mexeriam nas mesmas notas. E o
                    # `parar` faz a passada ceder a vez quando o dono voltar.
                    if revisar.DESCRICAO in envios.em_voo():
                        pele.nota("(ia revisar as notas de novo, mas a passada anterior ainda não terminou)")
                    else:
                        envios.disparar(
                            revisar.DESCRICAO,
                            lambda: revisar.rodar(cerebro, parar=lambda: app.ocupado),
                        )
                        pele.nota(
                            "(10 min parado — fui revisar minhas notas em segundo "
                            "plano; conto o desfecho na próxima fala)"
                        )
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
                    # Sem `tracar` de propósito (decisão do dono, 14/08/2026):
                    # o "[pensei: …]"/"[passo: …]" não aparece na TUI.
                    pensando=pele.pensando,
                    atividade=pele.atividade,
                    historico=historico,
                    recados=envios.colher_prontos,
                )
                historico = conversa.compactar(historico)
                conversa.salvar(historico)
            finally:
                app.ocupado = False
                # Pedido de ESC que sobrou (o turno acabou antes de um passo
                # conferir o Event) morre aqui — não vaza pro próximo turno.
                app.cancelar.clear()
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
