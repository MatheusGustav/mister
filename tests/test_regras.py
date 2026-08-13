"""O MISTER.md: o arquivo inteiro no prompt em toda mensagem, e a regra nova
só entrando com o "s" do dono.

O que está sob teste é a PROMESSA da divisão da memória: regra não espera o
assunto puxar — ela vale sempre, inclusive na fala SEGUINTE à gravação, sem
reiniciar nada. E quem grava é o dono, nunca o cérebro sozinho.
"""
import os
from pathlib import Path

from mister import brain, prompts, regras
from mister.confirmacao import Pendente
from mister.dispatcher import despachar

import mister.tools.regras  # noqa: F401  (cadastra a tool no registro)


class _Decisao:
    """Só a forma que o despachante espera — sem importar o cérebro."""

    def __init__(self, intencao, params, aval_do_dono=False, carimbo=""):
        self.intencao = intencao
        self.params = params
        self.aval_do_dono = aval_do_dono
        self.carimbo = carimbo


# --- o arquivo ----------------------------------------------------------------

def test_sem_arquivo_nao_ha_regras():
    assert regras.ler() == ""


def test_adicionar_cria_o_arquivo_e_acumula():
    regras.adicionar("nunca publicar nada em serviço externo")
    regras.adicionar("sempre  avisar\nantes de apagar")  # quebra vira espaço
    corpo = regras.ler()
    assert "- nunca publicar nada em serviço externo" in corpo
    assert "- sempre avisar antes de apagar" in corpo
    # a primeira não foi engolida pela segunda
    assert corpo.index("nunca publicar") < corpo.index("sempre avisar")


def test_arquivo_do_dono_e_lido_como_esta():
    """O dono pode editar o MISTER.md na mão — o que estiver lá é o que vale,
    sem o Mister reformatar nada."""
    Path(os.environ["MISTER_REGRAS"]).write_text(
        "## Minhas regras\ntexto livre, sem bullet\n", encoding="utf-8"
    )
    assert "texto livre, sem bullet" in regras.ler()


# --- o prompt -----------------------------------------------------------------

def test_sem_regras_o_prompt_nao_ganha_bloco():
    assert "REGRAS DO DONO" not in prompts.montar_instrucao()


def test_com_regras_o_arquivo_entra_inteiro_no_prompt():
    regras.adicionar("nunca publicar Artifact")
    instrucao = prompts.montar_instrucao()
    assert "REGRAS DO DONO" in instrucao
    assert "- nunca publicar Artifact" in instrucao


def test_regra_gravada_no_meio_da_conversa_vale_na_fala_seguinte():
    """A instrução é remontada a cada mensagem: o cérebro da PRÓXIMA chamada
    já enxerga a regra que acabou de entrar — sem reabrir sessão."""

    class _Cerebro(brain._CerebroBase):
        def __init__(self):
            self.sistemas = []

        def _chamar(self, mensagens, tools):
            self.sistemas.append(mensagens[0]["content"])
            return {"content": "ok"}

    cerebro = _Cerebro()
    cerebro.proximo_passo([{"role": "user", "content": "oi"}])
    regras.adicionar("responder sempre em português")
    cerebro.proximo_passo([{"role": "user", "content": "oi de novo"}])
    assert "responder sempre em português" not in cerebro.sistemas[0]
    assert "responder sempre em português" in cerebro.sistemas[1]


# --- a tool (o "s" do dono no meio do caminho) --------------------------------

def test_sem_aval_a_regra_nao_entra():
    saida = despachar(_Decisao("guardar_regra", {"regra": "nunca usar emoji"}))
    assert isinstance(saida, Pendente)
    assert "nunca usar emoji" in saida.pergunta
    assert regras.ler() == ""  # nada foi gravado antes do "s"


def test_com_o_s_do_dono_a_regra_entra():
    pendente = despachar(_Decisao("guardar_regra", {"regra": "nunca usar emoji"}))
    saida = despachar(_Decisao(
        pendente.intencao, pendente.params,
        aval_do_dono=True, carimbo=pendente.carimbo,
    ))
    assert saida.ok
    assert "- nunca usar emoji" in regras.ler()


def test_regra_vazia_e_recusada_sem_perguntar_nada():
    saida = despachar(_Decisao("guardar_regra", {"regra": "   "}))
    assert not isinstance(saida, Pendente)
    assert not saida.ok


def test_cerebro_nao_preenche_o_confirmado_sozinho():
    """A tranca de sempre: 'confirmado' vindo do modelo (sem aval) é descartado
    pelo despachante — vira Pendente, não gravação."""
    saida = despachar(_Decisao("guardar_regra", {"regra": "x", "confirmado": True}))
    assert isinstance(saida, Pendente)
    assert regras.ler() == ""
