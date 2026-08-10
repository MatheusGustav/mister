"""O laço: encadeia passos, pergunta, confirma e sabe a hora de parar.

Cérebro e despachante são dublês — o que está sob teste é a COSTURA, e a regra
de ouro: o agente nunca decide segurança, só leva a decisão pro despachante e o
"s" pro dono.
"""
from mister.agente import LOOP_JANELA, MARCA_RECADO, conversar
from mister.brain import Decisao, ErroCerebro
from mister.confirmacao import Pendente
from mister.resultado import Resultado


class _CerebroRoteirizado:
    """Cérebro de mentira: devolve as decisões de um roteiro, em ordem. Guarda o
    histórico que viu em cada passo (é como se confere o que o cérebro enxerga)."""

    def __init__(self, *decisoes):
        self._roteiro = list(decisoes)
        self.vistos: list[list[dict]] = []

    def proximo_passo(self, historico):
        self.vistos.append([dict(m) for m in historico])
        if not self._roteiro:
            return Decisao("responder", {"mensagem": "(roteiro acabou)"})
        proxima = self._roteiro.pop(0)
        if isinstance(proxima, Exception):
            raise proxima
        return proxima


def _falas():
    """Coletor de saída: o que o Mister mostrou, e o que foi pro bastidor."""
    ditas: list[str] = []
    return ditas, ditas.append


# --- encadear ----------------------------------------------------------------

def test_responder_encerra_o_turno_e_guarda_no_historico():
    cerebro = _CerebroRoteirizado(Decisao("responder", {"mensagem": "são 10h"}))
    ditas, mostrar = _falas()
    hist = conversar(
        cerebro, lambda d: Resultado(True, "?"), "que horas são?",
        perguntar=lambda p: "", mostrar=mostrar,
    )
    assert ditas == ["são 10h"]
    assert hist[0] == {"role": "user", "content": "que horas são?"}
    assert hist[-1] == {"role": "assistant", "content": "são 10h"}


def test_tool_roda_e_o_resultado_volta_pro_cerebro():
    cerebro = _CerebroRoteirizado(
        Decisao("que_horas_sao", {}, id_chamada="c1"),
        Decisao("responder", {"mensagem": "são 10h em ponto"}),
    )
    executadas = []

    def _executar(decisao):
        executadas.append(decisao.intencao)
        return Resultado(True, "Agora são 10:00 de 10/08/2026.")

    ditas, mostrar = _falas()
    hist = conversar(
        cerebro, _executar, "que horas são?", perguntar=lambda p: "", mostrar=mostrar,
    )
    assert executadas == ["que_horas_sao"]
    assert ditas == ["são 10h em ponto"]
    # O par nativo: jogada (assistant + tool_calls) e resposta amarrada pelo id.
    jogada = next(m for m in hist if m.get("tool_calls"))
    resposta = next(m for m in hist if m.get("role") == "tool")
    assert jogada["tool_calls"][0]["id"] == resposta["tool_call_id"] == "c1"
    assert "10:00" in resposta["content"]


def test_encadeia_varios_passos_diferentes():
    cerebro = _CerebroRoteirizado(
        Decisao("olhar_pasta_celular", {"pasta": "Download"}),
        Decisao("puxar_do_celular", {"nome": "a.pdf"}),
        Decisao("responder", {"mensagem": "puxei o a.pdf"}),
    )
    feitas = []
    ditas, mostrar = _falas()
    conversar(
        cerebro, lambda d: (feitas.append(d.intencao), Resultado(True, "ok"))[1],
        "traz o pdf", perguntar=lambda p: "", mostrar=mostrar,
    )
    assert feitas == ["olhar_pasta_celular", "puxar_do_celular"]
    assert ditas == ["puxei o a.pdf"]


def test_falha_da_tool_nao_derruba_o_turno():
    """O 'observar' do ciclo: resultado ok=False volta ao cérebro como dado, e
    ele decide o próximo passo (aqui, explicar)."""
    cerebro = _CerebroRoteirizado(
        Decisao("puxar_do_celular", {"nome": "sumido.pdf"}),
        Decisao("responder", {"mensagem": "não achei esse arquivo no celular"}),
    )
    ditas, mostrar = _falas()
    conversar(
        cerebro, lambda d: Resultado(False, "Não achei 'sumido.pdf'.", "Olhe a pasta antes."),
        "traz o pdf", perguntar=lambda p: "", mostrar=mostrar,
    )
    assert ditas == ["não achei esse arquivo no celular"]
    # a sugestão viaja junto com a mensagem (é o "corrigir")
    assert "Olhe a pasta antes." in cerebro.vistos[-1][-1]["content"]


# --- perguntar ---------------------------------------------------------------

def test_cancelar_durante_a_pergunta_nao_deixa_o_historico_quebrado():
    """ESC no meio de uma pergunta: a jogada já entrou no histórico e PRECISA da
    resposta dela — par nativo incompleto é pedido que a API recusa, e quem
    morreria por isso é o turno SEGUINTE."""
    import pytest

    cerebro = _CerebroRoteirizado(Decisao("perguntar", {"pergunta": "qual arquivo?"}))
    historico: list[dict] = []

    def _cancela(_):
        raise KeyboardInterrupt

    with pytest.raises(KeyboardInterrupt):
        conversar(
            cerebro, lambda d: Resultado(True, "?"), "manda um arquivo",
            perguntar=_cancela, mostrar=lambda m: None, historico=historico,
        )
    assert historico[-2].get("tool_calls") and historico[-1]["role"] == "tool"
    assert historico[-2]["tool_calls"][0]["id"] == historico[-1]["tool_call_id"]


def test_perguntar_leva_a_fala_do_dono_de_volta_pro_cerebro():
    cerebro = _CerebroRoteirizado(
        Decisao("perguntar", {"pergunta": "qual arquivo?"}, id_chamada="p1"),
        Decisao("responder", {"mensagem": "beleza"}),
    )
    ditas, mostrar = _falas()
    hist = conversar(
        cerebro, lambda d: Resultado(True, "?"), "manda um arquivo",
        perguntar=lambda p: "o relatorio.pdf", mostrar=mostrar,
    )
    resposta = next(m for m in hist if m.get("tool_call_id") == "p1")
    assert resposta["content"] == "o relatorio.pdf"


# --- confirmação -------------------------------------------------------------

def test_dono_topando_executa_com_aval_e_carimbo():
    cerebro = _CerebroRoteirizado(
        Decisao("puxar_do_celular", {"nome": "a.pdf"}),
        Decisao("responder", {"mensagem": "puxei"}),
    )
    vistas = []

    def _executar(decisao):
        vistas.append(decisao)
        if not decisao.aval_do_dono:
            return Pendente("Posso puxar 'a.pdf' pro PC?", "puxar_do_celular", {"nome": "a.pdf"})
        return Resultado(True, "puxei a.pdf")

    perguntas = []
    ditas, mostrar = _falas()
    conversar(
        cerebro, _executar, "traz o a.pdf",
        perguntar=lambda p: (perguntas.append(p), "s")[1], mostrar=mostrar,
    )
    assert "(s/n)" in perguntas[0]
    assert vistas[1].aval_do_dono is True and vistas[1].carimbo


def test_dono_recusando_nao_executa_e_avisa_o_cerebro():
    cerebro = _CerebroRoteirizado(
        Decisao("puxar_do_celular", {"nome": "a.pdf"}),
        Decisao("responder", {"mensagem": "ok, não puxei"}),
    )
    avais = []

    def _executar(decisao):
        avais.append(decisao.aval_do_dono)
        return Pendente("Posso puxar?", "puxar_do_celular", {"nome": "a.pdf"})

    ditas, mostrar = _falas()
    conversar(
        cerebro, _executar, "traz o a.pdf", perguntar=lambda p: "n", mostrar=mostrar,
    )
    assert avais == [False]  # nunca chegou a rodar com aval
    assert "recusou" in cerebro.vistos[-1][-1]["content"]


def test_so_s_e_sim_valem_como_aval():
    for resposta in ("talvez", "", "sim, mas depois...", "N"):
        cerebro = _CerebroRoteirizado(
            Decisao("puxar_do_celular", {"nome": "a.pdf"}),
            Decisao("responder", {"mensagem": "ok"}),
        )
        avais = []
        conversar(
            cerebro,
            lambda d: (avais.append(d.aval_do_dono),
                       Pendente("pode?", "puxar_do_celular", {"nome": "a.pdf"}))[1],
            "traz", perguntar=lambda p: resposta, mostrar=lambda m: None,
        )
        assert avais == [False], f"'{resposta}' não podia valer como sim"


# --- os cabrestos ------------------------------------------------------------

def test_acao_repetida_para_o_laco_e_explica():
    repetida = lambda: Decisao("olhar_pasta_celular", {"pasta": "Download"})
    cerebro = _CerebroRoteirizado(
        *[repetida() for _ in range(LOOP_JANELA)],
        Decisao("responder", {"mensagem": "travei olhando a mesma pasta, me ajuda?"}),
    )
    feitas = []
    ditas, mostrar = _falas()
    conversar(
        cerebro, lambda d: (feitas.append(d.intencao), Resultado(True, "vazia"))[1],
        "olha o Download", perguntar=lambda p: "", mostrar=mostrar,
    )
    # a 3ª repetição NÃO chega a executar — o laço para antes
    assert len(feitas) == LOOP_JANELA - 1
    assert ditas == ["travei olhando a mesma pasta, me ajuda?"]
    assert "loop detectado" in cerebro.vistos[-1][-1]["content"]


def test_passos_diferentes_nao_disparam_o_detector():
    cerebro = _CerebroRoteirizado(
        Decisao("olhar_pasta_celular", {"pasta": "Download"}),
        Decisao("olhar_pasta_celular", {"pasta": "DCIM"}),
        Decisao("olhar_pasta_celular", {"pasta": "Documents"}),
        Decisao("olhar_pasta_celular", {"pasta": "Music"}),
        Decisao("responder", {"mensagem": "olhei tudo"}),
    )
    ditas, mostrar = _falas()
    conversar(
        cerebro, lambda d: Resultado(True, "ok"), "procura o arquivo",
        perguntar=lambda p: "", mostrar=mostrar,
    )
    assert ditas == ["olhei tudo"]


def test_loop_com_api_caida_ainda_se_despede():
    cerebro = _CerebroRoteirizado(
        *[Decisao("que_horas_sao", {}) for _ in range(LOOP_JANELA)],
        ErroCerebro("caiu"),
    )
    ditas, mostrar = _falas()
    conversar(
        cerebro, lambda d: Resultado(True, "10h"), "horas",
        perguntar=lambda p: "", mostrar=mostrar,
    )
    assert "Travei repetindo" in ditas[0]


def test_falha_de_api_avisa_e_nao_vira_fala_do_mister():
    cerebro = _CerebroRoteirizado(ErroCerebro("chave recusada"))
    ditas, mostrar = _falas()
    hist = conversar(
        cerebro, lambda d: Resultado(True, "?"), "oi",
        perguntar=lambda p: "", mostrar=mostrar,
    )
    assert "falha na API" in ditas[0]
    # tropeço de infra não entra no histórico como resposta do Mister
    assert not any(m.get("role") == "assistant" for m in hist)


def test_decisao_sem_intencao_entrega_o_raciocinio():
    cerebro = _CerebroRoteirizado(Decisao(None, {}, raciocinio="não sei o que fazer com isso"))
    ditas, mostrar = _falas()
    conversar(
        cerebro, lambda d: Resultado(True, "?"), "???",
        perguntar=lambda p: "", mostrar=mostrar,
    )
    assert ditas == ["não sei o que fazer com isso"]


# --- recados do segundo plano ------------------------------------------------

def test_recado_do_segundo_plano_entra_como_dado():
    cerebro = _CerebroRoteirizado(Decisao("responder", {"mensagem": "o envio terminou"}))
    ditas, mostrar = _falas()
    conversar(
        cerebro, lambda d: Resultado(True, "?"), "e aí?",
        perguntar=lambda p: "", mostrar=mostrar,
        recados=lambda: ["Mandei 'a.pdf' pro celular."],
    )
    injetado = [m for m in cerebro.vistos[0] if MARCA_RECADO in str(m.get("content"))]
    assert injetado and "a.pdf" in injetado[0]["content"]


def test_sem_recados_o_laco_nao_muda():
    cerebro = _CerebroRoteirizado(Decisao("responder", {"mensagem": "oi"}))
    ditas, mostrar = _falas()
    conversar(
        cerebro, lambda d: Resultado(True, "?"), "oi",
        perguntar=lambda p: "", mostrar=mostrar, recados=lambda: [],
    )
    assert ditas == ["oi"]


# --- histórico entre turnos ---------------------------------------------------

def test_historico_carrega_a_conversa_toda():
    anterior = [
        {"role": "user", "content": "meu nome é Gustav"},
        {"role": "assistant", "content": "anotado"},
    ]
    cerebro = _CerebroRoteirizado(Decisao("responder", {"mensagem": "Gustav"}))
    hist = conversar(
        cerebro, lambda d: Resultado(True, "?"), "qual é o meu nome?",
        perguntar=lambda p: "", mostrar=lambda m: None, historico=anterior,
    )
    assert hist[0]["content"] == "meu nome é Gustav"
    assert len(hist) == 4
