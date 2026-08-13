"""O grafo e a caneta: nota .md com [[links]], escrita em segundo plano.

Aqui o Mister ESCREVE memória — ainda não lê (isso é das etapas do índice e da
leitura automática). O que está sob teste: o slug é um funil só (escrever e
linkar apontam pro mesmo arquivo), anotar no mesmo título ACRESCENTA em vez de
apagar, e a caneta nunca trava o turno.
"""
from mister import envios, memoria
from mister.dispatcher import despachar

import mister.tools.memoria  # noqa: F401  (cadastra a tool no registro)


class _Decisao:
    def __init__(self, intencao, params):
        self.intencao = intencao
        self.params = params
        self.aval_do_dono = False
        self.carimbo = ""


def _drenar():
    """Zera o registro de voos antes do teste (ele é global do processo)."""
    envios.aguardar(timeout=2)
    envios.colher_prontos()


# --- o slug (um funil só) -----------------------------------------------------

def test_slug_normaliza_acento_caixa_e_espaco():
    assert memoria.slug("Celular Redmi") == "celular-redmi"
    assert memoria.slug("Configuração do Mïcrofone!") == "configuracao-do-microfone"
    assert memoria.slug("ekodide") == "ekodide"


def test_titulo_e_link_apontam_pro_mesmo_arquivo():
    """'Celular Redmi' na escrita e '[[celular-redmi]]' num link são a MESMA
    nota — é o funil único que faz o grafo se fechar."""
    assert memoria.caminho_da("Celular Redmi") == memoria.caminho_da("celular-redmi")


# --- a escrita ----------------------------------------------------------------

def test_nota_nova_nasce_com_titulo_e_links_intactos():
    caminho = memoria.escrever("Celular Redmi", "6/128 GB. Falha do ADB em [[macetes-adb]].")
    corpo = caminho.read_text(encoding="utf-8")
    assert corpo.startswith("# Celular Redmi\n")
    assert "[[macetes-adb]]" in corpo
    assert caminho.name == "celular-redmi.md"


def test_anotar_no_mesmo_titulo_acrescenta_sem_apagar():
    memoria.escrever("Celular Redmi", "6/128 GB.")
    caminho = memoria.escrever("celular redmi", "A bateria segura 2 dias.")
    corpo = caminho.read_text(encoding="utf-8")
    assert "6/128 GB." in corpo and "A bateria segura 2 dias." in corpo
    assert corpo.index("6/128") < corpo.index("bateria")
    assert len(memoria.listar()) == 1  # mesma nota, não uma cópia


def test_listar_sem_pasta_devolve_vazio():
    assert memoria.listar() == []


# --- a tool (a caneta em segundo plano) ---------------------------------------

def test_anotar_responde_na_hora_e_o_desfecho_vira_recado():
    _drenar()
    saida = despachar(_Decisao("anotar_memoria", {
        "titulo": "tuned powersave",
        "conteudo": "O tuned em powersave crava o CPU em 1200 MHz.",
    }))
    assert saida.ok and "segundo plano" in saida.mensagem
    envios.aguardar(timeout=2)
    (fala,) = envios.colher_prontos()
    assert "Anotei 'tuned powersave'" in fala
    corpo = memoria.caminho_da("tuned powersave").read_text(encoding="utf-8")
    assert "1200 MHz" in corpo


def test_anotar_nao_pede_confirmacao():
    """O grafo é o caderno do Mister: escrever nele não passa pelo dono (a
    divisão da memória — quem passa pelo dono é o MISTER.md)."""
    _drenar()
    saida = despachar(_Decisao("anotar_memoria", {"titulo": "x", "conteudo": "y"}))
    assert saida.ok  # Resultado direto, nunca Pendente
    _drenar()


def test_titulo_sem_letra_e_recusado():
    saida = despachar(_Decisao("anotar_memoria", {"titulo": "???", "conteudo": "z"}))
    assert not saida.ok
    assert memoria.listar() == []


def test_conteudo_vazio_e_recusado():
    saida = despachar(_Decisao("anotar_memoria", {"titulo": "ok", "conteudo": "  "}))
    assert not saida.ok
    assert memoria.listar() == []
