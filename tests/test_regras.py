"""O MISTER.md: o arquivo inteiro no prompt em toda mensagem, e a regra
gravada na hora, sem confirmar (escrever regra é reversível — não bate no
critério de irreversibilidade, ver `confirmacao.pergunta_de_confirmacao`).

O que está sob teste é a PROMESSA da divisão da memória: regra não espera o
assunto puxar — ela vale sempre, inclusive na fala SEGUINTE à gravação, sem
reiniciar nada.
"""
import os
from pathlib import Path

from mister import brain, prompts, regras
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


# --- a tool (grava direto, sem pedir aval) ------------------------------------

def test_a_regra_entra_direto_sem_perguntar():
    saida = despachar(_Decisao("guardar_regra", {"regra": "nunca usar emoji"}))
    assert saida.ok
    assert "- nunca usar emoji" in regras.ler()


def test_regra_vazia_e_recusada():
    saida = despachar(_Decisao("guardar_regra", {"regra": "   "}))
    assert not saida.ok
    assert regras.ler() == ""
