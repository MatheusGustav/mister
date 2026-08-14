"""Testes dos INTERRUPTORES — os botões do dono.

O que se prova: tudo nasce ligado, o toque alterna e persiste, e as peças
desligáveis (anotar, correio) de fato obedecem o botão."""
from __future__ import annotations

from mister import interruptores
from mister.tools.memoria import AnotarMemoriaParams, anotar_memoria


def test_tudo_nasce_ligado():
    for nome in interruptores.NOMES:
        assert interruptores.ligado(nome)
    assert interruptores.ligado("botao-que-nao-existe")  # desconhecido = ligado


def test_alternar_desliga_e_religa():
    assert interruptores.alternar("revisar") is False
    assert not interruptores.ligado("revisar")
    assert interruptores.alternar("revisar") is True
    assert interruptores.ligado("revisar")


def test_o_toque_persiste_no_arquivo():
    interruptores.alternar("celular")
    # outro "processo" (leitura fresca do arquivo) vê o mesmo estado
    assert not interruptores.ligado("celular")
    assert interruptores._arquivo().exists()


def test_anotar_desligado_recusa_sem_gravar():
    interruptores.alternar("anotar")
    saida = anotar_memoria(AnotarMemoriaParams(titulo="Teste", conteudo="fato"))
    assert not saida.ok
    assert "DESLIGADO" in saida.mensagem
