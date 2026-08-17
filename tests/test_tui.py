"""Testes da TUI — o esqueleto, sem subir tela.

O que se prova aqui é o que quebraria CALADO: o contrato da pele (o agente
chama os mesmos métodos da pele simples — método faltando só apareceria com a
TUI aberta) e o CSS (um $nome esquecido no dicionário CORES viraria cor
inválida em tempo de execução). Subir o app de verdade é teste de olho, não de
suite."""
from __future__ import annotations

import re

import pytest

from mister import interface, tui


def test_cores_cobrem_todos_os_nomes_do_css():
    """Todo $nome usado no desenho existe no dicionário CORES (e o contrário:
    cor sobrando no dicionário é lixo que confunde quem for mexer)."""
    usados = set(re.findall(r"\$([a-z_]+)", tui._CSS))
    assert usados == set(tui.CORES)


def test_css_pronto_nao_deixa_nome_sem_cor():
    assert "$" not in tui._css()


def test_pele_tui_tem_o_mesmo_contrato_da_pele_simples():
    """O agente/__main__ não sabem em qual pele estão falando — a TUI precisa
    responder a TUDO que a pele simples responde."""
    contrato = {
        nome
        for nome in dir(interface.Pele)
        if not nome.startswith("_")
    }
    faltando = contrato - set(dir(tui.PeleTui))
    assert not faltando, f"a PeleTui não tem: {sorted(faltando)}"


def test_blocos_da_conversa_filtra_o_que_nao_e_conversa():
    """Só fala do dono e fala do Mister viram bloco — mensagem de sistema
    (user com '['), jogada de ferramenta e resposta de tool ficam de fora."""
    historico = [
        {"role": "user", "content": "oi"},
        {"role": "user", "content": "[recado do segundo plano] terminou"},
        {"role": "assistant", "content": "vou olhar", "tool_calls": [{"id": "x"}]},
        {"role": "tool", "tool_call_id": "x", "content": "resultado"},
        {"role": "assistant", "content": "olá!"},
    ]
    assert tui.blocos_da_conversa(historico) == [("dono", "oi"), ("mister", "olá!")]


def test_exportar_grava_md_legivel(tmp_path):
    historico = [
        {"role": "user", "content": "oi"},
        {"role": "assistant", "content": "olá!"},
    ]
    caminho = tui.exportar(historico, pasta=str(tmp_path))
    corpo = caminho.read_text(encoding="utf-8")
    assert caminho.suffix == ".md"
    assert "**Dono:** oi" in corpo
    assert "**Mister:** olá!" in corpo


def test_interpretar_barra():
    assert tui.interpretar_barra("oi, tudo bem?") is None
    assert tui.interpretar_barra("!ls") is None
    assert tui.interpretar_barra("/nova") == "nova"
    assert tui.interpretar_barra("/naoexiste") == "naoexiste"  # quem avisa é a tela
    assert tui.interpretar_barra("/") == ""


def test_sem_ajuda_nos_comandos():
    """Decisão do dono (13/08/2026): /ajuda não existe — a paleta cumpre o
    papel. O teste segura a decisão contra regressão."""
    assert "ajuda" not in tui.COMANDOS


def test_laco_registra_todas_as_familias_de_tools():
    """O _laco cadastra as tools importando os módulos dela — módulo esquecido
    é tool que SOME CALADA da TUI (as fichas nem chegam à API). Foi o caso do
    maquina.py: rodar_comando, ler_arquivo, escrever_arquivo e procurar_arquivo
    não existiam na TUI. O espelho é a lista do __main__."""
    import inspect
    from pathlib import Path

    import mister.tools

    fonte = inspect.getsource(tui._laco)
    modulos = {
        arquivo.stem
        for arquivo in Path(mister.tools.__file__).parent.glob("*.py")
    } - {"__init__"}
    faltando = {m for m in modulos if f"import mister.tools.{m}" not in fonte}
    assert not faltando, f"o _laco não registra as tools de: {sorted(faltando)}"


def _rodar_app(corpo, monkeypatch, tamanho=(100, 30)):
    """Sobe o app de verdade (headless, via pilot do textual) e roda `corpo`
    dentro dele. Sem chave de API de propósito: o laço de trabalho morre no
    aviso de chave e não encosta em rede.

    `tamanho` é (colunas, linhas) — o painel da direita decide sozinho pela
    largura, então tem teste que precisa de tela larga e teste que precisa de
    tela estreita."""
    import asyncio

    textual = pytest.importorskip("textual")  # noqa: F841 — extra opcional
    monkeypatch.delenv("MISTER_API_KEY", raising=False)

    async def _dentro():
        app = tui.criar_app()()
        async with app.run_test(size=tamanho) as pilot:
            await pilot.pause(0.2)
            await corpo(app, pilot)

    asyncio.run(_dentro())


def test_sessoes_nao_interpreta_a_fala_do_dono_como_markup(tmp_path, monkeypatch):
    """O resumo no menu do /conversas é a 1ª FALA DO DONO — como string crua o
    OptionList a parseava como markup: '[/red]' na fala DERRUBAVA o app inteiro
    (MarkupError no render). Tem que entrar como Text e aparecer literal."""
    import json

    pasta = tmp_path / "conversas"  # é pra cá que o conftest aponta o acervo
    pasta.mkdir(parents=True, exist_ok=True)
    (pasta / "2026-08-14-000000.json").write_text(json.dumps([
        {"role": "user", "content": "arruma o css do [/red] ali"},
        {"role": "assistant", "content": "feito"},
    ]), encoding="utf-8")

    async def corpo(app, pilot):
        from textual.widgets import OptionList

        app._executar_comando("conversas")
        await pilot.pause(0.3)  # o render é onde o MarkupError estourava
        assert type(app.screen).__name__ == "Sessoes"
        linha = "".join(
            seg.text for seg in app.screen.query_one(OptionList).render_line(0)
        )
        assert "[/red]" in linha  # literal, do jeito que o dono digitou
        assert app.is_running

    _rodar_app(corpo, monkeypatch)


def test_interruptores_mostram_o_estado_por_extenso(monkeypatch):
    """O rótulo '[ligado]/[DESLIGADO]' era engolido pelo parser de markup do
    OptionList e nunca aparecia na tela — tem que sair literal."""
    async def corpo(app, pilot):
        from textual.widgets import OptionList

        app._executar_comando("interruptores")
        await pilot.pause()
        lista = app.screen.query_one(OptionList)
        # O rótulo quebra de linha na largura do modal: junta as linhas todas.
        tela = "\n".join(
            "".join(seg.text for seg in lista.render_line(i)) for i in range(12)
        )
        assert "[ligado]" in tela

    _rodar_app(corpo, monkeypatch)


def test_esc_cancela_pergunta_pendente_na_hora(monkeypatch):
    """ESC com uma pergunta esperando resposta cancela NA HORA: o sentinela
    acorda a fila de resposta e vira KeyboardInterrupt na thread de trabalho.
    (Com o SetAsyncExc antigo, a exceção não acordava o get() bloqueado — o
    ESC só valia depois que o dono digitasse algo, e essa fala se perdia.)"""
    import threading

    async def corpo(app, pilot):
        desfecho = []

        def trabalho():
            try:
                tui.PeleTui(app).perguntar("apago a nota? (s/n)")
                desfecho.append("respondida")
            except KeyboardInterrupt:
                desfecho.append("cancelada")

        app.ocupado = True
        threading.Thread(target=trabalho, daemon=True).start()
        for _ in range(100):  # espera a pergunta bloquear na fila
            await pilot.pause(0.02)
            if app.aguardando_resposta:
                break
        await pilot.press("escape")
        for _ in range(100):
            await pilot.pause(0.02)
            if desfecho:
                break
        assert desfecho == ["cancelada"]

    _rodar_app(corpo, monkeypatch)


def test_fala_enfileirada_nao_vira_resposta_de_pergunta(monkeypatch):
    """Fala digitada com o Mister ocupado é promessa de PRÓXIMO TURNO — uma
    pergunta que apareça depois não pode consumi-la como resposta. Só vale o
    que o dono digitar com a pergunta na tela."""
    import threading

    async def corpo(app, pilot):
        app.ocupado = True
        entrada = app.query_one("#entrada")
        entrada.value = "depois faz X"
        await pilot.press("enter")  # vai pra fila do teclado (turno futuro)

        respostas = []
        threading.Thread(
            target=lambda: respostas.append(tui.PeleTui(app).perguntar("apago? (s/n)")),
            daemon=True,
        ).start()
        for _ in range(100):
            await pilot.pause(0.02)
            if app.aguardando_resposta:
                break
        entrada.value = "s"
        await pilot.press("enter")  # digitado COM a pergunta na tela: resposta
        for _ in range(100):
            await pilot.pause(0.02)
            if respostas:
                break
        assert respostas == ["s"]
        assert app.fila_do_teclado.get_nowait() == "depois faz X"

    _rodar_app(corpo, monkeypatch)


def test_mostrar_espera_duas_vezes_no_mesmo_tick_nao_estoura(monkeypatch):
    """Dois mostrar_espera sem o loop girar no meio estouravam DuplicateIds
    (o remove() do textual é assíncrono e o id fixo colidia) — o widget agora
    vai por referência, sem id."""
    async def corpo(app, pilot):
        app.mostrar_espera("um")
        app.mostrar_espera("dois")  # mesmo tick: era o DuplicateIds
        await pilot.pause()
        assert len(app.query(".espera")) == 1
        app.tirar_espera()
        await pilot.pause()
        assert len(app.query(".espera")) == 0

    _rodar_app(corpo, monkeypatch)


def test_digitar_sem_enviar_conta_como_atividade(monkeypatch):
    """O relógio da ociosidade rearma a cada tecla na caixa, não só no Enter —
    a revisão não pode disparar com o dono no meio de uma frase longa."""
    import time as tempo

    async def corpo(app, pilot):
        app._ultimo_toque = tempo.monotonic() - 10_000
        await pilot.press("a")  # digita sem enviar
        assert tempo.monotonic() - app._ultimo_toque < 5

    _rodar_app(corpo, monkeypatch)


# --- o painel da direita ------------------------------------------------------

def test_brasao_cabe_na_largura_util_do_painel():
    """O painel tem 42 colunas, o padding come 2+2 e a barra de rolagem do
    textual 8.2.8 come mais 2: sobram 36. Arte mais larga ganha rolagem
    horizontal e sai torta."""
    linhas = tui.BRASAO.split("\n")
    assert max(len(linha) for linha in linhas) <= tui.LARGURA_UTIL_PAINEL
    # E os espaços da esquerda são o desenho: linha aparada entorta o gato.
    assert linhas[0].startswith("  ")


def test_brasao_sai_centrado_sem_torcer_o_desenho():
    """O recuo é UM só, igual em todas as linhas: centrar linha a linha
    (text-align do CSS) embaralharia o gato, porque elas têm comprimentos
    diferentes de propósito."""
    caixa = max(len(linha) for linha in tui.BRASAO.split("\n"))
    saidas = tui.brasao(largura=36).plain.split("\n")
    originais = tui.BRASAO.split("\n")
    recuos = {len(linha) - len(linha.lstrip(" ")) - (len(o) - len(o.lstrip(" ")))
              for linha, o in zip(saidas, originais)}
    assert recuos == {(36 - caixa) // 2}
    assert max(len(linha) for linha in saidas) <= 36


def test_painel_aparece_sozinho_em_tela_larga(monkeypatch):
    async def corpo(app, pilot):
        painel = app.query_one("#painel")
        assert app._painel_visivel()
        assert not painel.has_class("escondido")
        assert not painel.has_class("sobreposto")  # do LADO, dividindo a tela
        assert app.query_one("#coluna").size.width == 160 - tui.LARGURA_PAINEL

    _rodar_app(corpo, monkeypatch, tamanho=(160, 30))


def test_painel_fica_escondido_em_tela_estreita(monkeypatch):
    async def corpo(app, pilot):
        assert not app._painel_visivel()
        assert app.query_one("#painel").has_class("escondido")
        assert app.query_one("#coluna").size.width == 100  # a conversa fica inteira

    _rodar_app(corpo, monkeypatch, tamanho=(100, 30))


def test_ctrl_b_em_tela_estreita_poe_o_painel_por_cima(monkeypatch):
    """Ligado na mão com pouca coluna, ele NÃO divide espaço: vai pra camada de
    sobreposição, encostado na direita, e a conversa continua do tamanho que
    estava."""
    async def corpo(app, pilot):
        await pilot.press("ctrl+b")
        await pilot.pause()
        painel = app.query_one("#painel")
        assert not painel.has_class("escondido")
        assert painel.has_class("sobreposto")
        assert app.query_one("#coluna").size.width == 100

    _rodar_app(corpo, monkeypatch, tamanho=(100, 30))


def test_a_escolha_manual_ganha_do_automatico(monkeypatch):
    """Desligou na mão em tela larga: continua desligado, mesmo com espaço de
    sobra. (E o ctrl+b tem que chegar no app com o foco na caixa de digitar —
    é o Input que engolia o ctrl+a antes do priority.)"""
    async def corpo(app, pilot):
        assert app._painel_visivel()
        await pilot.press("ctrl+b")
        await pilot.pause()
        assert not app._painel_visivel()
        assert app.query_one("#painel").has_class("escondido")
        await pilot.press("ctrl+b")
        await pilot.pause()
        assert app._painel_visivel()

    _rodar_app(corpo, monkeypatch, tamanho=(160, 30))


def test_painel_mostra_arquivos_mexidos_e_tarefas(monkeypatch, tmp_path):
    """As duas seções lêem os módulos direto, de segundo em segundo — o teste
    chama o redesenho na mão pra não depender do relógio."""
    from mister import mexidos, tarefas

    monkeypatch.setenv("HOME", str(tmp_path))

    async def corpo(app, pilot):
        mexidos.marcar(tmp_path / "notas" / "hoje.md")
        tarefas.definir([
            {"texto": "ler o arquivo", "estado": "feito"},
            {"texto": "trocar o trecho", "estado": "fazendo"},
        ])
        app._atualizar_painel()
        await pilot.pause()
        assert "~/notas/hoje.md" in app._secao_arquivos().plain
        tela_tarefas = app._secao_tarefas().plain
        assert "1/2 feitas" in tela_tarefas
        assert "✓ ler o arquivo" in tela_tarefas
        assert "▸ trocar o trecho" in tela_tarefas

    _rodar_app(corpo, monkeypatch, tamanho=(160, 30))


def test_painel_sem_nada_ainda_nao_fica_em_branco(monkeypatch):
    """Conversa recém-aberta: as três seções dizem que estão vazias em vez de
    sumirem — painel em branco parece painel quebrado."""
    async def corpo(app, pilot):
        assert "nenhum ainda" in app._secao_arquivos().plain
        assert "nenhuma ainda" in app._secao_tarefas().plain
        assert "sem medida ainda" in app._secao_contexto().plain

    _rodar_app(corpo, monkeypatch, tamanho=(160, 30))


def test_barra_de_contexto_le_o_ultimo_uso_do_cerebro(monkeypatch):
    """O caminho inteiro: o _laco põe o cérebro no app, o transporte guarda o
    `usage` da última ida, e o painel desenha o prompt_tokens (o tamanho ATUAL
    do contexto, não a soma do que já se gastou)."""
    class CerebroFalso:
        modelo = "x/y"
        ultimo_uso = {"prompt_tokens": 24_100, "completion_tokens": 900}

    async def corpo(app, pilot):
        app.cerebro = CerebroFalso()
        tela = app._secao_contexto().plain
        assert "24.1k / 128k tokens" in tela  # 128k é o teto de reserva
        assert "(teto estimado)" in tela      # e a tela DIZ que é reserva

    _rodar_app(corpo, monkeypatch, tamanho=(160, 30))


def test_caixa_de_digitar_ocupa_uma_linha_so(monkeypatch):
    """Com o textual 8.2.8 o CSS padrão do Input traz `height: 3` e a caixa
    renderizava com 4 linhas (modo/modelo + digitar + 2 em branco). O
    `compact=True` é o conserto."""
    async def corpo(app, pilot):
        assert app.query_one("#entrada").size.height == 1
        assert app.query_one("#caixa").size.height == 2  # modo/modelo + digitar

    _rodar_app(corpo, monkeypatch, tamanho=(160, 30))


def test_importar_tui_nao_exige_textual(monkeypatch):
    """O import do módulo não pode puxar o textual (extra opcional): quem
    importa `mister.tui` sem o extra só quebra — com receita — no `main`."""
    import importlib
    import sys

    monkeypatch.setitem(sys.modules, "textual", None)
    importlib.reload(tui)  # se o topo do módulo importasse textual, estourava
