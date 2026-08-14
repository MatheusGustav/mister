"""O grafo e a caneta: nota .md com [[links]], escrita em segundo plano — e
agora as quatro tools de corrigir memória, síncronas, com a trava de
ler-antes.

O que está sob teste: o slug é um funil só (escrever e linkar apontam pro
mesmo arquivo), anotar no mesmo título ACRESCENTA em vez de apagar, a caneta
nunca trava o turno, e ler_nota/trocar_trecho/reescrever_nota/apagar_nota só
mexem no que o cérebro já leu nesta conversa.
"""
from mister import envios, leituras, memoria
from mister.confirmacao import Pendente
from mister.dispatcher import despachar

import mister.tools.memoria  # noqa: F401  (cadastra a tool no registro)


class _Decisao:
    def __init__(self, intencao, params, aval_do_dono=False, carimbo=""):
        self.intencao = intencao
        self.params = params
        self.aval_do_dono = aval_do_dono
        self.carimbo = carimbo


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


# --- ler_nota -------------------------------------------------------------

def test_ler_nota_devolve_o_conteudo_inteiro_e_marca_como_lida():
    memoria.escrever("Celular Redmi", "6/128 GB.\n\n## ADB\ncom travas")
    saida = despachar(_Decisao("ler_nota", {"nome": "Celular Redmi"}))
    assert saida.ok
    assert "6/128 GB" in saida.mensagem and "com travas" in saida.mensagem
    assert leituras.foi_lido(memoria.caminho_da("celular-redmi"))


def test_ler_nota_que_nao_existe_e_recusada():
    saida = despachar(_Decisao("ler_nota", {"nome": "nunca-existiu"}))
    assert not saida.ok


# --- a trava de ler-antes ----------------------------------------------------

def test_trocar_trecho_sem_ler_antes_e_recusado():
    memoria.escrever("Celular Redmi", "6/128 GB.")
    saida = despachar(_Decisao("trocar_trecho", {
        "nome": "Celular Redmi", "velho": "6/128", "novo": "8/256",
    }))
    assert not saida.ok
    assert "ler_nota" in saida.sugestao


def test_reescrever_nota_sem_ler_antes_e_recusado():
    memoria.escrever("Celular Redmi", "texto velho")
    saida = despachar(_Decisao("reescrever_nota", {
        "nome": "Celular Redmi", "conteudo": "texto novo",
    }))
    assert not saida.ok


def test_apagar_nota_sem_ler_antes_ainda_pede_confirmacao_primeiro():
    """apagar_nota SEMPRE confirma (item 3, gate do despachante, roda ANTES da
    tool) — a trava de ler-antes só é conferida na hora de executar de
    verdade, depois do "sim" do dono."""
    memoria.escrever("Celular Redmi", "specs")
    saida = despachar(_Decisao("apagar_nota", {"nome": "Celular Redmi"}))
    assert isinstance(saida, Pendente)
    assert memoria.caminho_da("Celular Redmi").exists()


def test_apagar_nota_confirmada_sem_nunca_ter_lido_falha_na_execucao():
    memoria.escrever("Celular Redmi", "specs")
    pendente = despachar(_Decisao("apagar_nota", {"nome": "Celular Redmi"}))
    saida = despachar(_Decisao(
        pendente.intencao, pendente.params, aval_do_dono=True, carimbo=pendente.carimbo,
    ))
    assert not saida.ok
    assert "ler_nota" in saida.sugestao
    assert memoria.caminho_da("Celular Redmi").exists()


def test_ler_e_depois_mexer_funciona():
    memoria.escrever("Celular Redmi", "6/128 GB.")
    despachar(_Decisao("ler_nota", {"nome": "Celular Redmi"}))
    saida = despachar(_Decisao("trocar_trecho", {
        "nome": "Celular Redmi", "velho": "6/128", "novo": "8/256",
    }))
    assert saida.ok


def test_arquivo_mudado_por_fora_depois_de_ler_derruba_a_trava():
    memoria.escrever("Celular Redmi", "6/128 GB.")
    despachar(_Decisao("ler_nota", {"nome": "Celular Redmi"}))
    # alguém (o dono, outro processo) mexeu no arquivo por fora
    memoria.caminho_da("Celular Redmi").write_text("outra coisa bem diferente", encoding="utf-8")
    saida = despachar(_Decisao("trocar_trecho", {
        "nome": "Celular Redmi", "velho": "outra", "novo": "nova",
    }))
    assert not saida.ok
    assert "ler_nota" in saida.sugestao


# --- trocar_trecho: achar-e-substituir ---------------------------------------

def test_trocar_trecho_troca_a_ocorrencia_unica():
    memoria.escrever("Celular Redmi", "6/128 GB de armazenamento.")
    despachar(_Decisao("ler_nota", {"nome": "Celular Redmi"}))
    saida = despachar(_Decisao("trocar_trecho", {
        "nome": "Celular Redmi", "velho": "6/128 GB", "novo": "8/256 GB",
    }))
    assert saida.ok
    corpo = memoria.caminho_da("Celular Redmi").read_text(encoding="utf-8")
    assert "8/256 GB de armazenamento." in corpo and "6/128" not in corpo


def test_trecho_nao_encontrado_e_erro_explicando():
    memoria.escrever("Celular Redmi", "6/128 GB.")
    despachar(_Decisao("ler_nota", {"nome": "Celular Redmi"}))
    saida = despachar(_Decisao("trocar_trecho", {
        "nome": "Celular Redmi", "velho": "16/512", "novo": "x",
    }))
    assert not saida.ok
    assert "Não achei" in saida.mensagem


def test_trecho_repetido_e_erro_explicando():
    memoria.escrever("Celular Redmi", "GB e GB de novo GB.")
    despachar(_Decisao("ler_nota", {"nome": "Celular Redmi"}))
    saida = despachar(_Decisao("trocar_trecho", {
        "nome": "Celular Redmi", "velho": "GB", "novo": "gigabytes",
    }))
    assert not saida.ok
    assert "3 vezes" in saida.mensagem


# --- reescrever_nota ----------------------------------------------------------

def test_reescrever_nota_troca_o_arquivo_inteiro():
    memoria.escrever("Celular Redmi", "texto velho, bagunçado")
    despachar(_Decisao("ler_nota", {"nome": "Celular Redmi"}))
    saida = despachar(_Decisao("reescrever_nota", {
        "nome": "Celular Redmi", "conteudo": "# Celular Redmi\n\ntexto novo, organizado",
    }))
    assert saida.ok
    corpo = memoria.caminho_da("Celular Redmi").read_text(encoding="utf-8")
    assert "texto novo, organizado" in corpo and "bagunçado" not in corpo


def test_reescrever_com_conteudo_vazio_e_recusado():
    memoria.escrever("Celular Redmi", "texto velho")
    despachar(_Decisao("ler_nota", {"nome": "Celular Redmi"}))
    saida = despachar(_Decisao("reescrever_nota", {"nome": "Celular Redmi", "conteudo": "  "}))
    assert not saida.ok
    assert "texto velho" in memoria.caminho_da("Celular Redmi").read_text(encoding="utf-8")


# --- apagar_nota: sempre confirma --------------------------------------------

def test_apagar_nota_pede_confirmacao_mesmo_apos_ler():
    memoria.escrever("Celular Redmi", "specs")
    despachar(_Decisao("ler_nota", {"nome": "Celular Redmi"}))
    saida = despachar(_Decisao("apagar_nota", {"nome": "Celular Redmi"}))
    assert isinstance(saida, Pendente)
    assert "Celular Redmi" in saida.pergunta
    assert memoria.caminho_da("Celular Redmi").exists()


def test_apagar_nota_com_aval_do_dono_apaga():
    memoria.escrever("Celular Redmi", "specs")
    despachar(_Decisao("ler_nota", {"nome": "Celular Redmi"}))
    pendente = despachar(_Decisao("apagar_nota", {"nome": "Celular Redmi"}))
    saida = despachar(_Decisao(
        pendente.intencao, pendente.params, aval_do_dono=True, carimbo=pendente.carimbo,
    ))
    assert saida.ok
    assert not memoria.caminho_da("Celular Redmi").exists()
