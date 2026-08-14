"""As tools do celular com o EKODIDE DUBLADO — nenhum teste sobe rede.

Os testes do transporte (lacre, cofre, pedaços, retomada) não são daqui: moram
no repo do ekodide e rodam lá, junto com o CI do Android. Aqui se testa o que é
do Mister: recusar com jeito quando o ekodide falta, e traduzir o resultado
neutro dele pra fala.
"""
from pathlib import Path
from types import SimpleNamespace

import pytest

from mister import envios
from mister.dispatcher import despachar
from mister.resultado import Resultado
from mister.tools import correio


class _ErroConfig(Exception):
    """O ekodide.config.ErroConfig de mentira."""


class _ErroPuxar(Exception):
    """O ekodide.ErroPuxar de mentira."""


def _envio(ok=True, is_pasta=False, total=1, enviados=1, destino="", falhas=None):
    """Um EnvioResultado de mentira — os mesmos campos do ekodide."""
    return SimpleNamespace(
        ok=ok, is_pasta=is_pasta, total=total, enviados=enviados,
        destino=destino, falhas=falhas or [],
    )


@pytest.fixture
def ekodide_dublado(monkeypatch):
    """Veste o ekodide com um dublê. Devolve o dublê pra cada teste programar o
    que ele responde e conferir com que argumentos foi chamado."""
    chamadas = {"enviar": [], "puxar": [], "listar": []}

    def _enviar(origem, url, segredo):
        chamadas["enviar"].append((Path(origem), url, segredo))
        return dublê.resposta_enviar

    def _puxar(nome, url, segredo, base, tamanho=None):
        chamadas["puxar"].append((nome, url, segredo, Path(base)))
        return dublê.resposta_puxar

    def _listar(url, segredo):
        chamadas["listar"].append((url, segredo))
        if isinstance(dublê.resposta_listar, Exception):
            raise dublê.resposta_listar
        return dublê.resposta_listar

    def _espiar(nome, url, segredo, limite=None):
        chamadas["espiar"].append((nome, url, segredo, limite))
        return dublê.resposta_espiar

    chamadas["espiar"] = []
    dublê = SimpleNamespace(
        chamadas=chamadas,
        enviar=_enviar,
        puxar=_puxar,
        listar_remoto=_listar,
        espiar=_espiar,
        ErroPuxar=_ErroPuxar,
        resposta_enviar=_envio(),
        resposta_puxar=(True, "/home/matheus/Downloads/a.pdf"),
        resposta_listar=[],
        resposta_espiar=(True, b"conteudo", 8),
    )
    config = SimpleNamespace(
        ErroConfig=_ErroConfig,
        url_do_destino=lambda nome: "http://192.168.0.9:8778",
        segredo=lambda: "frase-secreta",
        carregar=lambda: {"receber": {"dir": "/tmp/recebidos-de-mentira"}},
    )
    monkeypatch.setattr(correio, "ekodide", dublê)
    monkeypatch.setattr(correio, "ekodide_config", config)
    monkeypatch.setattr(correio, "ekodide_vizinhanca", SimpleNamespace(
        procurar=lambda: [], url_de=lambda a: a["url"],
    ))
    dublê.config = config
    return dublê


@pytest.fixture(autouse=True)
def sem_envios_pendentes():
    yield
    envios.aguardar(timeout=2)
    envios.colher_prontos()


# --- o extra opcional: sem ekodide, recusa com receita -----------------------

@pytest.mark.parametrize("nome,params", [
    ("enviar_para_celular", {"caminho": "~/a.pdf"}),
    ("olhar_pasta_celular", {"pasta": "Download"}),
    ("puxar_do_celular", {"nome": "a.pdf"}),
])
def test_sem_ekodide_recusa_com_receita_em_vez_de_estourar(monkeypatch, nome, params):
    monkeypatch.setattr(correio, "ekodide", None)
    saida = despachar(_decisao(nome, params))
    assert isinstance(saida, Resultado)
    assert saida.ok is False
    assert "ekodide" in saida.mensagem
    assert "install" in saida.sugestao and "ekodide pair" in saida.sugestao


def test_disponivel_e_um_lugar_so(monkeypatch):
    monkeypatch.setattr(correio, "ekodide", None)
    assert correio._disponivel() is False
    monkeypatch.setattr(correio, "ekodide", SimpleNamespace())
    assert correio._disponivel() is True


def test_andamento_funciona_mesmo_sem_ekodide(monkeypatch):
    """Ela olha o registro LOCAL de envios, não a rede — não é do ekodide."""
    monkeypatch.setattr(correio, "ekodide", None)
    saida = despachar(_decisao("andamento_envios", {}))
    assert saida.ok and "Nenhum envio" in saida.mensagem


# --- enviar ------------------------------------------------------------------

def test_enviar_dispara_em_segundo_plano_e_responde_na_hora(ekodide_dublado, tmp_path):
    arquivo = tmp_path / "relatorio.pdf"
    arquivo.write_text("conteúdo", encoding="utf-8")
    ekodide_dublado.resposta_enviar = _envio(destino="/Download/relatorio.pdf")

    saida = despachar(_decisao("enviar_para_celular", {"caminho": str(arquivo)}))
    assert saida.ok and "segundo plano" in saida.mensagem

    envios.aguardar(timeout=2)
    (fala,) = envios.colher_prontos()
    assert "Mandei 'relatorio.pdf' pro celular" in fala
    assert "/Download/relatorio.pdf" in fala
    # a origem chegou no ekodide como caminho de verdade, com o segredo da config
    origem, url, segredo = ekodide_dublado.chamadas["enviar"][0]
    assert origem == arquivo and url.startswith("http://") and segredo == "frase-secreta"


def test_enviar_arquivo_inexistente_falha_na_hora_sem_ir_pra_rede(ekodide_dublado, tmp_path):
    (tmp_path / "relatorio.pdf").write_text("x", encoding="utf-8")
    saida = despachar(_decisao("enviar_para_celular", {"caminho": str(tmp_path / "relatoro.pdf")}))
    assert saida.ok is False
    assert "relatorio.pdf" in saida.sugestao      # o "corrigir": nome parecido
    assert ekodide_dublado.chamadas["enviar"] == []


def test_enviar_sem_config_recusa_com_a_receita_de_parear(ekodide_dublado, tmp_path):
    arquivo = tmp_path / "a.pdf"
    arquivo.write_text("x", encoding="utf-8")

    def _sem_segredo():
        raise _ErroConfig("Sem segredo.")

    ekodide_dublado.config.segredo = _sem_segredo
    saida = despachar(_decisao("enviar_para_celular", {"caminho": str(arquivo)}))
    assert saida.ok is False and "não está pronto" in saida.mensagem
    assert "ekodide pair" in saida.sugestao


def test_falha_do_envio_vira_fala_honesta(ekodide_dublado, tmp_path):
    arquivo = tmp_path / "a.pdf"
    arquivo.write_text("x", encoding="utf-8")
    ekodide_dublado.resposta_enviar = _envio(ok=False, enviados=0, falhas=["conexão recusada"])
    despachar(_decisao("enviar_para_celular", {"caminho": str(arquivo)}))
    envios.aguardar(timeout=2)
    (fala,) = envios.colher_prontos()
    assert "Não consegui enviar" in fala and "conexão recusada" in fala


def test_pasta_vazia_avisa_em_vez_de_dizer_que_mandou(ekodide_dublado, tmp_path):
    pasta = tmp_path / "vazia"
    pasta.mkdir()
    ekodide_dublado.resposta_enviar = _envio(ok=True, is_pasta=True, total=0, enviados=0)
    despachar(_decisao("enviar_para_celular", {"caminho": str(pasta)}))
    envios.aguardar(timeout=2)
    (fala,) = envios.colher_prontos()
    assert "está vazia" in fala


def test_pasta_conta_quantos_foram(ekodide_dublado, tmp_path):
    pasta = tmp_path / "fotos"
    pasta.mkdir()
    ekodide_dublado.resposta_enviar = _envio(is_pasta=True, total=10, enviados=8, falhas=["x", "y"])
    despachar(_decisao("enviar_para_celular", {"caminho": str(pasta)}))
    envios.aguardar(timeout=2)
    (fala,) = envios.colher_prontos()
    assert "8 de 10" in fala and "Falharam 2" in fala


def test_andamento_mostra_o_que_ainda_voa(ekodide_dublado, tmp_path):
    import threading

    arquivo = tmp_path / "grande.iso"
    arquivo.write_text("x", encoding="utf-8")
    solta = threading.Event()
    ekodide_dublado.enviar = lambda *a: (solta.wait(3), _envio())[1]

    despachar(_decisao("enviar_para_celular", {"caminho": str(arquivo)}))
    saida = despachar(_decisao("andamento_envios", {}))
    assert saida.ok and "grande.iso" in saida.mensagem
    solta.set()


# --- olhar a pasta -----------------------------------------------------------

def test_olhar_lista_arquivos_e_subpastas_de_um_nivel(ekodide_dublado):
    ekodide_dublado.resposta_listar = [
        {"nome": "Download/a.pdf", "tamanho": 2048},
        {"nome": "Download/notas.txt", "tamanho": 120},
        {"nome": "Download/velhos/b.pdf", "tamanho": 999},
        {"nome": "DCIM/Camera/foto.jpg", "tamanho": 3_000_000},
    ]
    saida = despachar(_decisao("olhar_pasta_celular", {"pasta": "Download"}))
    assert saida.ok
    assert "a.pdf" in saida.mensagem and "notas.txt" in saida.mensagem
    assert "[pasta] velhos/" in saida.mensagem
    assert "foto.jpg" not in saida.mensagem      # é de outro galho
    assert "2.0 KB" in saida.mensagem            # tamanho legível


def test_olhar_a_raiz_mostra_as_pastas_que_existem(ekodide_dublado):
    ekodide_dublado.resposta_listar = [
        {"nome": "Download/a.pdf", "tamanho": 1},
        {"nome": "DCIM/Camera/foto.jpg", "tamanho": 1},
        {"nome": "solto.txt", "tamanho": 1},
    ]
    saida = despachar(_decisao("olhar_pasta_celular", {}))
    assert "[pasta] Download/" in saida.mensagem
    assert "[pasta] DCIM/" in saida.mensagem
    assert "solto.txt" in saida.mensagem


def test_olhar_pasta_que_nao_existe_explica_em_vez_de_mentir(ekodide_dublado):
    ekodide_dublado.resposta_listar = [{"nome": "Download/a.pdf", "tamanho": 1}]
    saida = despachar(_decisao("olhar_pasta_celular", {"pasta": "NaoExiste"}))
    assert saida.ok and "Não vi nada" in saida.mensagem and saida.sugestao


def test_olhar_com_celular_fora_do_ar_devolve_o_motivo(ekodide_dublado):
    ekodide_dublado.resposta_listar = _ErroPuxar("não alcancei a origem")
    saida = despachar(_decisao("olhar_pasta_celular", {"pasta": "Download"}))
    assert saida.ok is False
    assert "não alcancei a origem" in saida.sugestao
    assert "pareado" in saida.sugestao


def test_a_pasta_e_visivel_pro_cerebro():
    """Campo com default NÃO é campo interno: se 'pasta' ficasse escondida, o
    cérebro nunca conseguiria olhar nada além da raiz."""
    from mister.prompts import montar_tools

    ficha = next(
        f for f in montar_tools() if f["function"]["name"] == "olhar_pasta_celular"
    )
    assert "pasta" in ficha["function"]["parameters"]["properties"]
    assert ficha["function"]["parameters"]["required"] == []


# --- puxar: grava no disco, mas não apaga nada — não confirma ---------------

def test_puxar_baixa_direto_sem_confirmar(ekodide_dublado):
    saida = despachar(_decisao("puxar_do_celular", {"nome": "a.pdf", "pasta": "Download"}))
    assert isinstance(saida, Resultado)          # não é Pendente: não pede aval
    assert saida.ok and "Puxei 'Download/a.pdf'" in saida.mensagem
    nome, _url, _segredo, base = ekodide_dublado.chamadas["puxar"][0]
    assert nome == "Download/a.pdf"
    # onde o arquivo cai é decisão do EKODIDE (a config dele), não do Mister
    assert base == Path("/tmp/recebidos-de-mentira")


def test_puxar_que_falha_diz_o_motivo(ekodide_dublado):
    ekodide_dublado.resposta_puxar = (False, "'a.pdf' não está disponível pra puxar")
    saida = despachar(_decisao("puxar_do_celular", {"nome": "a.pdf"}))
    assert saida.ok is False
    assert "não está disponível" in saida.sugestao
    assert "olhar_pasta_celular" in saida.sugestao


def test_puxar_sem_ekodide_recusa_com_receita(monkeypatch):
    monkeypatch.setattr(correio, "ekodide", None)
    saida = despachar(_decisao("puxar_do_celular", {"nome": "a.pdf"}))
    assert isinstance(saida, Resultado) and saida.ok is False


# --- olhar: acontece na RAM, então NÃO confirma ------------------------------

def test_olhar_le_o_texto_sem_gravar_e_sem_perguntar(ekodide_dublado):
    conteudo = "anotação do celular".encode()
    ekodide_dublado.resposta_espiar = (True, conteudo, len(conteudo))
    saida = despachar(_decisao("olhar_no_celular", {"nome": "nota.txt", "pasta": "Download"}))
    assert isinstance(saida, Resultado)          # não é Pendente: não pede aval
    assert saida.ok
    assert "anotação do celular" in saida.mensagem
    assert "nada foi salvo no PC" in saida.mensagem
    nome, _url, _segredo, limite = ekodide_dublado.chamadas["espiar"][0]
    assert nome == "Download/nota.txt"
    assert limite == correio.ESPIADA_MAX        # o resto do arquivo nem viaja
    assert ekodide_dublado.chamadas["puxar"] == []


def test_olhar_avisa_quando_cortou(ekodide_dublado):
    ekodide_dublado.resposta_espiar = (True, b"a" * correio.ESPIADA_MAX, 5_000_000)
    saida = despachar(_decisao("olhar_no_celular", {"nome": "grande.log"}))
    assert saida.ok and "cortei aqui" in saida.mensagem
    assert "4.8 MB" in saida.mensagem            # o tamanho REAL do arquivo


def test_olhar_arquivo_vazio_diz_que_esta_vazio(ekodide_dublado):
    ekodide_dublado.resposta_espiar = (True, b"", 0)
    saida = despachar(_decisao("olhar_no_celular", {"nome": "vazio.txt"}))
    assert saida.ok and "está vazio" in saida.mensagem


def test_olhar_apara_caractere_partido_no_corte(ekodide_dublado):
    """A espiada corta por BYTES: se o corte cai no meio de um acento, o rabo
    quebrado é aparado — não é motivo pra chamar o arquivo de binário."""
    partido = "café".encode()[:-1]   # perde o segundo byte do 'é'
    ekodide_dublado.resposta_espiar = (True, partido, 9_000)
    saida = despachar(_decisao("olhar_no_celular", {"nome": "nota.txt"}))
    assert saida.ok and "caf" in saida.mensagem


def test_olhar_binario_disfarcado_de_txt_recusa(ekodide_dublado):
    ekodide_dublado.resposta_espiar = (True, b"\xff\xfe\x00\x01mais lixo aqui", 15)
    saida = despachar(_decisao("olhar_no_celular", {"nome": "falso.txt"}))
    assert saida.ok is False and "binário disfarçado" in saida.mensagem


@pytest.mark.parametrize("nome,palavra", [
    ("foto.jpg", "foto"), ("manual.pdf", "PDF"),
    ("clipe.mp4", "vídeo"), ("audio.ogg", "áudio"),
])
def test_olhar_recusa_o_que_ainda_nao_enxerga_sem_ir_pra_rede(ekodide_dublado, nome, palavra):
    saida = despachar(_decisao("olhar_no_celular", {"nome": nome}))
    assert saida.ok is False and palavra in saida.mensagem
    assert "puxar_do_celular" in saida.sugestao
    assert ekodide_dublado.chamadas["espiar"] == []


def test_olhar_tipo_desconhecido_recusa(ekodide_dublado):
    saida = despachar(_decisao("olhar_no_celular", {"nome": "coisa.xyz"}))
    assert saida.ok is False and "tipo de texto" in saida.mensagem
    assert ekodide_dublado.chamadas["espiar"] == []


def test_olhar_que_falha_diz_o_motivo(ekodide_dublado):
    ekodide_dublado.resposta_espiar = (False, "não alcancei a origem", 0)
    saida = despachar(_decisao("olhar_no_celular", {"nome": "nota.txt"}))
    assert saida.ok is False and "não alcancei a origem" in saida.sugestao
    assert "olhar_pasta_celular" in saida.sugestao


def test_olhar_sem_ekodide_recusa_com_receita(monkeypatch):
    monkeypatch.setattr(correio, "ekodide", None)
    saida = despachar(_decisao("olhar_no_celular", {"nome": "nota.txt"}))
    assert saida.ok is False and saida.sugestao == correio.RECEITA_EKODIDE


# --- as 5 tools do MVP --------------------------------------------------------

def test_o_mvp_tem_as_cinco_tools_do_celular():
    from mister.registry import REGISTRO

    esperadas = {
        "enviar_para_celular", "olhar_pasta_celular", "puxar_do_celular",
        "olhar_no_celular", "andamento_envios",
    }
    assert esperadas <= set(REGISTRO)


def test_nenhuma_das_cinco_pede_confirmacao(ekodide_dublado):
    """Puxar baixa mas não apaga nada — não é irreversível, então nenhuma das
    cinco tools do celular pede aval (ver confirmacao.pergunta_de_confirmacao)."""
    ekodide_dublado.resposta_listar = [{"nome": "nota.txt", "tamanho": 8}]
    ekodide_dublado.resposta_espiar = (True, b"conteudo", 8)
    sem_aval = {
        "olhar_pasta_celular": {},
        "olhar_no_celular": {"nome": "nota.txt"},
        "andamento_envios": {},
        "puxar_do_celular": {"nome": "nota.txt"},
    }
    for nome, params in sem_aval.items():
        assert isinstance(despachar(_decisao(nome, params)), Resultado), nome


# --- o endereço do celular ---------------------------------------------------

def test_destino_fora_da_config_e_procurado_na_rede(ekodide_dublado, monkeypatch):
    def _nao_cadastrado(nome):
        raise _ErroConfig("Destino 'celular' não está na config.")

    ekodide_dublado.config.url_do_destino = _nao_cadastrado
    monkeypatch.setattr(correio, "ekodide_vizinhanca", SimpleNamespace(
        procurar=lambda: [{"nome": "celular", "url": "http://192.168.0.55:8778"}],
        url_de=lambda a: a["url"],
    ))
    assert correio._endereco("celular") == "http://192.168.0.55:8778"


def test_destino_que_ninguem_conhece_vira_erro_de_config(ekodide_dublado):
    def _nao_cadastrado(nome):
        raise _ErroConfig("não está na config")

    ekodide_dublado.config.url_do_destino = _nao_cadastrado
    with pytest.raises(_ErroConfig, match="nem na config, nem na rede"):
        correio._endereco("celular")


# --- ajudantes ---------------------------------------------------------------

def _decisao(intencao, params, aval_do_dono=False, carimbo=""):
    from mister.brain import Decisao

    return Decisao(intencao, params, aval_do_dono=aval_do_dono, carimbo=carimbo)


def test_tamanho_humano():
    assert correio._tam_humano(512) == "512 B"
    assert correio._tam_humano(2048) == "2.0 KB"
    assert correio._tam_humano(15_500_000) == "14.8 MB"


def test_caminho_remoto_junta_pasta_e_nome():
    assert correio._caminho_remoto("Download", "a.pdf") == "Download/a.pdf"
    assert correio._caminho_remoto("", "a.pdf") == "a.pdf"
    assert correio._caminho_remoto("/Download/", "a.pdf") == "Download/a.pdf"


