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
    """Cérebro de mentira: devolve os LOTES de um roteiro, em ordem. Guarda o
    histórico que viu em cada volta (é como se confere o que o cérebro
    enxerga). Cada item do roteiro é uma `Decisao` solta (vira lote de 1 —
    o jeito antigo, ainda o mais comum nos testes) ou já uma LISTA de
    `Decisao` (um lote de verdade, pra testar o item 1 — o lote de 4)."""

    def __init__(self, *decisoes):
        self._roteiro = list(decisoes)
        self.vistos: list[list[dict]] = []

    def proximo_passo(self, historico):
        self.vistos.append([dict(m) for m in historico])
        if not self._roteiro:
            return [Decisao("responder", {"mensagem": "(roteiro acabou)"})]
        proxima = self._roteiro.pop(0)
        if isinstance(proxima, Exception):
            raise proxima
        return proxima if isinstance(proxima, list) else [proxima]


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


# --- o lote (item 1: até 4 chamadas por resposta) -----------------------------

def test_lote_de_acoes_independentes_roda_todas_em_ordem():
    lote = [
        Decisao("olhar_pasta_celular", {"pasta": "Download"}, id_chamada="a"),
        Decisao("que_horas_sao", {}, id_chamada="b"),
    ]
    cerebro = _CerebroRoteirizado(lote, Decisao("responder", {"mensagem": "prontinho"}))
    feitas = []
    ditas, mostrar = _falas()
    hist = conversar(
        cerebro, lambda d: (feitas.append(d.intencao), Resultado(True, "ok"))[1],
        "olha e diz a hora", perguntar=lambda p: "", mostrar=mostrar,
    )
    assert feitas == ["olhar_pasta_celular", "que_horas_sao"]
    assert ditas == ["prontinho"]
    # UMA jogada nativa com as DUAS chamadas — o protocolo exige isso quando o
    # modelo manda mais de uma chamada na mesma resposta.
    jogada = next(m for m in hist if m.get("tool_calls"))
    assert [tc["id"] for tc in jogada["tool_calls"]] == ["a", "b"]
    respostas_ids = {m["tool_call_id"] for m in hist if m.get("role") == "tool"}
    assert respostas_ids == {"a", "b"}


def test_lote_para_na_confirmacao_e_o_resto_nao_roda():
    lote = [
        Decisao("que_horas_sao", {}, id_chamada="a"),
        Decisao("puxar_do_celular", {"nome": "a.pdf"}, id_chamada="b"),
        Decisao("olhar_pasta_celular", {"pasta": "Download"}, id_chamada="c"),
    ]
    cerebro = _CerebroRoteirizado(lote, Decisao("responder", {"mensagem": "fim"}))
    feitas = []

    def _executar(d):
        feitas.append(d.intencao)
        if d.intencao == "puxar_do_celular" and not d.aval_do_dono:
            return Pendente("Posso puxar?", "puxar_do_celular", {"nome": "a.pdf"})
        return Resultado(True, "ok")

    hist = conversar(
        cerebro, _executar, "faz tudo", perguntar=lambda p: "s", mostrar=lambda m: None,
    )
    # a (rodou), b (pediu aval, confirmou e rodou DE NOVO) — c nunca chegou a rodar
    assert feitas == ["que_horas_sao", "puxar_do_celular", "puxar_do_celular"]
    respostas = {m["tool_call_id"]: m["content"] for m in hist if m.get("role") == "tool"}
    assert "não executei" in respostas["c"]


def test_lote_para_na_confirmacao_recusada_e_o_resto_tambem_nao_roda():
    lote = [
        Decisao("puxar_do_celular", {"nome": "a.pdf"}, id_chamada="a"),
        Decisao("que_horas_sao", {}, id_chamada="b"),
    ]
    cerebro = _CerebroRoteirizado(lote, Decisao("responder", {"mensagem": "ok, não puxei"}))
    feitas = []

    def _executar(d):
        feitas.append(d.intencao)
        return Pendente("Posso puxar?", "puxar_do_celular", {"nome": "a.pdf"})

    hist = conversar(
        cerebro, _executar, "traz", perguntar=lambda p: "n", mostrar=lambda m: None,
    )
    assert feitas == ["puxar_do_celular"]  # a segunda nunca chega a rodar
    respostas = {m["tool_call_id"]: m["content"] for m in hist if m.get("role") == "tool"}
    assert "não executei" in respostas["b"]


def test_lote_para_no_perguntar_e_o_resto_nao_roda():
    lote = [
        Decisao("que_horas_sao", {}, id_chamada="a"),
        Decisao("perguntar", {"pergunta": "qual pasta?"}, id_chamada="b"),
        Decisao("olhar_pasta_celular", {"pasta": "Download"}, id_chamada="c"),
    ]
    cerebro = _CerebroRoteirizado(lote, Decisao("responder", {"mensagem": "ok"}))
    feitas = []
    hist = conversar(
        cerebro, lambda d: (feitas.append(d.intencao), Resultado(True, "ok"))[1],
        "faz umas coisas", perguntar=lambda p: "Download", mostrar=lambda m: None,
    )
    assert feitas == ["que_horas_sao"]  # olhar_pasta_celular nunca chegou a rodar
    respostas = {m["tool_call_id"]: m["content"] for m in hist if m.get("role") == "tool"}
    assert respostas["b"] == "Download"
    assert "não executei" in respostas["c"]


def test_lote_com_mais_de_4_so_roda_as_4_primeiras():
    lote = [Decisao("que_horas_sao", {}, id_chamada=str(i)) for i in range(6)]
    cerebro = _CerebroRoteirizado(lote, Decisao("responder", {"mensagem": "fim"}))
    feitas = []
    conversar(
        cerebro, lambda d: (feitas.append(d.intencao), Resultado(True, "ok"))[1],
        "faz 6 coisas", perguntar=lambda p: "", mostrar=lambda m: None,
    )
    assert len(feitas) == 4


def test_chamadas_cortadas_pelo_teto_tambem_ganham_resposta():
    """Chamada sem resposta invalida o histórico nativo — mesmo a que nunca
    rodou (cortada pelo teto de 4) precisa da sua."""
    lote = [Decisao("que_horas_sao", {}, id_chamada=str(i)) for i in range(6)]
    cerebro = _CerebroRoteirizado(lote, Decisao("responder", {"mensagem": "fim"}))
    hist = conversar(
        cerebro, lambda d: Resultado(True, "ok"),
        "faz 6 coisas", perguntar=lambda p: "", mostrar=lambda m: None,
    )
    jogada = next(m for m in hist if m.get("tool_calls"))
    assert len(jogada["tool_calls"]) == 6
    respostas = {m["tool_call_id"]: m["content"] for m in hist if m.get("role") == "tool"}
    assert set(respostas) == {str(i) for i in range(6)}
    assert "mais de 4" in respostas["4"] and "mais de 4" in respostas["5"]


def test_cancelar_no_meio_do_lote_ainda_registra_o_que_ja_rodou():
    import pytest

    lote = [
        Decisao("que_horas_sao", {}, id_chamada="a"),
        Decisao("perguntar", {"pergunta": "qual pasta?"}, id_chamada="b"),
    ]
    cerebro = _CerebroRoteirizado(lote)
    historico: list[dict] = []

    def _cancela(_):
        raise KeyboardInterrupt

    with pytest.raises(KeyboardInterrupt):
        conversar(
            cerebro, lambda d: Resultado(True, "10h"), "faz coisas",
            perguntar=_cancela, mostrar=lambda m: None, historico=historico,
        )
    jogada = next(m for m in historico if m.get("tool_calls"))
    assert [tc["id"] for tc in jogada["tool_calls"]] == ["a", "b"]
    respostas = {m["tool_call_id"]: m["content"] for m in historico if m.get("role") == "tool"}
    assert "10h" in respostas["a"]
    assert "cancelou" in respostas["b"]


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


def test_lote_identico_repetido_dispara_o_detector():
    lote = [
        Decisao("olhar_pasta_celular", {"pasta": "Download"}),
        Decisao("que_horas_sao", {}),
    ]
    cerebro = _CerebroRoteirizado(
        *[lote for _ in range(LOOP_JANELA)],
        Decisao("responder", {"mensagem": "travei, me ajuda"}),
    )
    feitas = []
    ditas, mostrar = _falas()
    conversar(
        cerebro, lambda d: (feitas.append(d.intencao), Resultado(True, "ok"))[1],
        "repete", perguntar=lambda p: "", mostrar=mostrar,
    )
    # a última repetição do LOTE (as duas chamadas) não chega a executar
    assert len(feitas) == 2 * (LOOP_JANELA - 1)
    assert ditas == ["travei, me ajuda"]
    assert "loop detectado" in cerebro.vistos[-1][-1]["content"]


def test_detector_de_loop_e_pelo_lote_inteiro_nao_por_chamada_solta():
    """Lotes DIFERENTES que compartilham uma chamada em comum não disparam o
    detector — a chave é o LOTE inteiro, não cada intenção isolada."""
    lote_a = [Decisao("olhar_pasta_celular", {"pasta": "Download"})]
    lote_b = [
        Decisao("olhar_pasta_celular", {"pasta": "Download"}),
        Decisao("que_horas_sao", {}),
    ]
    cerebro = _CerebroRoteirizado(
        lote_a, lote_b, lote_a, lote_b, Decisao("responder", {"mensagem": "fim"}),
    )
    feitas = []
    ditas, mostrar = _falas()
    conversar(
        cerebro, lambda d: (feitas.append(d.intencao), Resultado(True, "ok"))[1],
        "alterna", perguntar=lambda p: "", mostrar=mostrar,
    )
    assert feitas.count("olhar_pasta_celular") == 4  # nenhum turno foi bloqueado
    assert ditas == ["fim"]


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
