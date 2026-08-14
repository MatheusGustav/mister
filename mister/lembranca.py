"""A LEITURA AUTOMÁTICA — o grafo devolvendo, na hora certa, o que a caneta
escreveu.

Roda ANTES de toda resposta, sem ser ferramenta — de propósito: como
ferramenta, o cérebro só consultaria quando LEMBRASSE de consultar, e
responderia errado tendo a resposta guardada. Automático não esquece.

O caminho é o do desenho:

1. A PORTA DE ENTRADA é por significado: o índice aponta a nota mais parecida
   com o que o dono falou (não é busca por palavra — acha mesmo escrita com
   outras palavras).
2. Achou, LÊ O ARQUIVO DE VERDADE: os números só apontam onde está; quem vai
   pro cérebro é o texto da nota, INTEIRO.
3. CAMINHA PELOS [[links]] da nota de entrada — e só segue o link se aquela
   nota ainda tiver a ver com o assunto. Deixou de ter, para ali.

A parada é POR ASSUNTO (não por distância nem por tamanho): ninguém chuta
número de notas. Os limiares vêm da calibração na máquina (13/08/2026, com os
prefixos de tarefa do embeddinggemma): consulta certa pontua 0.41+, a melhor
errada 0.26, fora-de-assunto fica abaixo de 0.20. A CAMINHADA aceita um pouco
menos que a entrada porque o link é um fiador — o grafo já diz que as duas
notas andam juntas; o limiar só confere se o assunto ainda é este.

Toda nota que chega aqui até o cérebro conta como LIDA (ver `leituras.py`): se
ele mandar corrigi-la na mesma conversa, a trava de ler-antes já deixa passar.
"""
from __future__ import annotations

import re

from mister import indice, leituras, memoria

LIMIAR_ENTRADA = 0.35
LIMIAR_CAMINHADA = 0.30

_RE_LINK = re.compile(r"\[\[([^\[\]]+)\]\]")


def lembrar(texto: str) -> list[tuple[str, str]]:
    """O que a memória tem a ver com `texto`: [(nome, corpo inteiro)], a nota
    de entrada primeiro e depois as visitadas pela caminhada, na ordem.

    Vazio quando nada casa de verdade — memória calada é melhor que memória
    metida. Levanta `IndiceIndisponivel` (do índice) com o modelo indisponível;
    a palavra pro dono é de quem monta o prompt, não daqui."""
    pontuadas = indice.procurar(texto)
    if not pontuadas or pontuadas[0][1] < LIMIAR_ENTRADA:
        return []
    parecenca = dict(pontuadas)
    fila = [pontuadas[0][0]]
    vistos = set(fila)
    notas: list[tuple[str, str]] = []
    while fila:
        nome = fila.pop(0)
        caminho = memoria.caminho_da(nome)
        try:
            corpo = caminho.read_text(encoding="utf-8").strip()
        except OSError:
            continue  # apontada mas sumida do disco: a caminhada segue sem ela
        leituras.marcar(caminho)  # a trava de ler-antes conta isto como leitura
        notas.append((nome, corpo))
        for link in _RE_LINK.findall(corpo):
            alvo = memoria.slug(link)
            # Link pra nota que não existe não pontua (fica de fora sozinho);
            # o ciclo A<->B morre no `vistos`.
            if alvo not in vistos and parecenca.get(alvo, 0.0) >= LIMIAR_CAMINHADA:
                vistos.add(alvo)
                fila.append(alvo)
    return notas
