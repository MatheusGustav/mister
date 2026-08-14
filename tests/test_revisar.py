"""Testes do REVISAR — a passada de arrumação em segundo plano.

Tudo SEM API: o cérebro é um dublê com o roteiro pronto. O que se prova é o
LAÇO da revisão — as tools de verdade rodam (grafo do tmp_path), o apagar
passa pelo Pendente e volta aprovado, e o que é fora do escopo não executa."""
from __future__ import annotations

from mister import memoria, revisar
from mister.brain import Decisao

# As tools de nota se cadastram no registro ao importar (o despachante precisa).
import mister.tools.memoria  # noqa: F401
import mister.tools.maquina  # noqa: F401


class _CerebroDeMentira:
    """Devolve os lotes do roteiro, um por chamada."""

    def __init__(self, roteiro: list[list[Decisao]]):
        self._roteiro = list(roteiro)

    def proximo_passo(self, historico: list[dict]) -> list[Decisao]:
        return self._roteiro.pop(0)


def test_revisar_corrige_e_apaga_e_resume():
    memoria.escrever("Celular", "- Bateria em 47%")
    memoria.escrever("Velha", "assunto que morreu")
    cerebro = _CerebroDeMentira([
        [Decisao("trocar_trecho", {"nome": "Celular", "velho": "47%", "novo": "30%"})],
        [Decisao("apagar_nota", {"nome": "Velha"})],  # passa pelo Pendente
        [Decisao("responder", {"mensagem": "atualizei a bateria e apaguei a morta."})],
    ])
    fala = revisar.rodar(cerebro)
    assert "30%" in memoria.caminho_da("Celular").read_text(encoding="utf-8")
    assert not memoria.caminho_da("Velha").exists()  # o aval de projeto valeu
    assert fala == "Revisei minhas notas (2). atualizei a bateria e apaguei a morta."


def test_revisar_com_grafo_vazio_nem_chama_o_cerebro():
    class _Explode:
        def proximo_passo(self, historico):
            raise AssertionError("não era pra chamar a API sem nota")

    assert "vazio" in revisar.rodar(_Explode())


def test_fora_do_escopo_nao_executa():
    saida = revisar._executar(Decisao("rodar_comando", {"comando": "rm -rf /"}))
    assert "fora do escopo" in saida


def test_estourar_o_teto_para_com_aviso():
    memoria.escrever("Nota", "algo")
    girando = _CerebroDeMentira(
        [[Decisao("ler_nota", {"nome": "Nota"})]] * revisar.MAX_LOTES
    )
    fala = revisar.rodar(girando)
    assert "teto" in fala
