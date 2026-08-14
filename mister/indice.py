"""O ÍNDICE — os números que apontam ONDE a memória está.

A porta de entrada do grafo é POR SIGNIFICADO: um modelo pequeno (o
embeddinggemma, rodando DENTRO deste processo — sem servidor nenhum)
transforma texto em números, e comparar números acha a nota mais parecida com
o que o dono falou — mesmo escrita com outras palavras. Os números só
apontam; quem vai pro cérebro é o TEXTO da nota, inteiro (isso é da leitura
automática, na etapa seguinte).

O PICADINHO: nota grande tratada como bloco único vira números borrados — uma
média de dez assuntos não casa bem com nenhum. Então o índice pica a nota por
dentro (por seção de markdown) só pra MIRAR melhor; o score da nota é o da
MELHOR seção. A nota no disco continua inteira — o picadinho é invisível,
existe só aqui.

Onde mora e quando atualiza (escolha de construção): um JSON em
`~/.mister/indice.json`, sincronizado ANTES de cada busca — só o que mudou
(mtime+tamanho) é re-embutido, então a varredura de rotina é um stat por nota.

A busca é LOCAL: custa tempo e espaço, não dinheiro — nada aqui vai à API do
cérebro. O modelo (onnxruntime + tokenizers, ~40 linhas nossas em cima deles)
carrega PREGUIÇOSO, só na primeira chamada de `embutir` — os pesos (a
conversão ONNX do embeddinggemma, com as camadas Dense do pooling já dentro do
grafo) baixam na hora pra `~/.mister/modelos/` se ainda não estiverem lá, e
ficam. Modelo que não carrega (sem internet na primeira vez, disco cheio,
arquivo corrompido) vira `IndiceIndisponivel` com a receita — quem chama
responde sem memória e avisa curto, no mesmo jeito ekodide.

Os PREFIXOS de tarefa ("task: search result | query:", "title: | text:") são o
dialeto de busca do embeddinggemma. Calibrado em português nesta máquina
(13/08/2026, com o Ollama): com eles, consulta certa pontua 0.41–0.54 e a
melhor errada 0.26; sem eles, a errada encosta em 0.42. É o que sustenta os
limiares da caminhada (ver a leitura automática). O modelo em processo (ONNX)
foi conferido contra ESSES mesmos números antes de trocar (14/08/2026):
cosseno cruzado por texto ≥ 0.99 contra o Ollama — os limiares não mudam.
"""
from __future__ import annotations

import json
import math
import os
import re
import urllib.request
from pathlib import Path

import numpy as np
import onnxruntime as ort
from tokenizers import Tokenizer

from mister import memoria

ARQUIVO_PADRAO = str(Path.home() / ".mister" / "indice.json")
PASTA_MODELO_PADRAO = str(Path.home() / ".mister" / "modelos" / "embeddinggemma-300m-onnx")
MODELO = "embeddinggemma-onnx"

# A conversão que traz as camadas Dense do pooling JÁ dentro do grafo ONNX
# (testado 14/08/2026: outras conversões guardam essas camadas fora, em
# safetensors separados, e o resultado sem elas não bate com o modelo real).
_REPO_MODELO = "ISOISS/embeddinggemma-300m-ONNX-sentencetransformers"
_ARQUIVO_ONNX = "onnx/model_quantized.onnx"
_ARQUIVOS_MODELO = ("tokenizer.json", _ARQUIVO_ONNX, f"{_ARQUIVO_ONNX}_data")
_URL_BASE = f"https://huggingface.co/{_REPO_MODELO}/resolve/main/"

# O dialeto de busca do embeddinggemma: consulta e documento levam prefixos
# DIFERENTES — é assim que o modelo foi treinado a aproximar pergunta de
# resposta, não frase de frase igual.
_PREFIXO_CONSULTA = "task: search result | query: "
_PREFIXO_NOTA = "title: {titulo} | text: {texto}"

RECEITA_MODELO = (
    "Confira a internet: a primeira busca depois de instalar baixa os pesos "
    f"(~300 MB, uma vez só) de {_REPO_MODELO} pra {PASTA_MODELO_PADRAO}."
)


class IndiceIndisponivel(RuntimeError):
    """O modelo de embeddings não carregou — sem ele não há busca por
    significado. NÃO é erro de memória perdida: as notas seguem no disco; só a
    porta de entrada está fechada."""


def _pasta_modelo() -> Path:
    return Path(os.environ.get("MISTER_MODELO_EMBUTIR", PASTA_MODELO_PADRAO))


def _arquivo() -> Path:
    return Path(os.environ.get("MISTER_INDICE", ARQUIVO_PADRAO))


# --- o modelo em processo ------------------------------------------------------

_sessao: ort.InferenceSession | None = None
_tokenizer: Tokenizer | None = None


def _baixar_pesos(pasta: Path) -> None:
    """Um arquivo por vez, direto da fonte (sem depender de mais nenhuma
    biblioteca) — pra um `.parcial` primeiro, e só vira o arquivo de verdade
    se baixou inteiro (download cortado no meio não engana a próxima
    chamada)."""
    for nome in _ARQUIVOS_MODELO:
        destino = pasta / nome
        if destino.exists():
            continue
        destino.parent.mkdir(parents=True, exist_ok=True)
        parcial = destino.with_name(destino.name + ".parcial")
        with urllib.request.urlopen(f"{_URL_BASE}{nome}", timeout=300) as resp:
            with open(parcial, "wb") as f:
                while bloco := resp.read(1 << 20):
                    f.write(bloco)
        parcial.rename(destino)


def _carregar_modelo() -> None:
    """Carregamento preguiçoso: só na primeira chamada de `embutir` — as
    chamadas seguintes reusam a sessão já de pé."""
    global _sessao, _tokenizer
    if _sessao is not None:
        return
    pasta = _pasta_modelo()
    _baixar_pesos(pasta)
    tokenizer = Tokenizer.from_file(str(pasta / "tokenizer.json"))
    tokenizer.enable_padding(pad_id=tokenizer.token_to_id("<pad>"), pad_token="<pad>")
    sessao = ort.InferenceSession(str(pasta / _ARQUIVO_ONNX), providers=["CPUExecutionProvider"])
    _tokenizer, _sessao = tokenizer, sessao


# --- o comparador -------------------------------------------------------------

def embutir(textos: list[str]) -> list[list[float]]:
    """Texto -> números, no embeddinggemma rodando dentro deste processo (um
    lote por chamada). Levanta `IndiceIndisponivel` (com a receita na
    mensagem) se o modelo não carregar ou a conta não sair."""
    try:
        _carregar_modelo()
        codificados = _tokenizer.encode_batch(textos)
        entradas = {
            "input_ids": np.array([c.ids for c in codificados], dtype=np.int64),
            "attention_mask": np.array([c.attention_mask for c in codificados], dtype=np.int64),
        }
        (vetores,) = _sessao.run(["sentence_embedding"], entradas)
    except Exception as e:
        # onnxruntime não expõe uma classe-base útil pras próprias exceções
        # (Fail, InvalidArgument, NoSuchFile... todas derivam só de Exception)
        # — e download/tokenização podem falhar de formas variadas também.
        # Qualquer tropeço aqui é o mesmo recado pra quem chama: sem memória
        # por significado agora, mas as notas continuam seguras no disco.
        raise IndiceIndisponivel(f"o modelo de embeddings não carregou. {RECEITA_MODELO}") from e
    return vetores.tolist()


def _cosseno(a: list[float], b: list[float]) -> float:
    escalar = sum(x * y for x, y in zip(a, b))
    normas = math.sqrt(sum(x * x for x in a)) * math.sqrt(sum(x * x for x in b))
    return escalar / normas if normas else 0.0


# --- o picadinho --------------------------------------------------------------

def picar(texto: str) -> list[str]:
    """As seções de uma nota, pra mirar melhor: cada título de markdown abre um
    pedaço novo (o que vem antes do primeiro título é pedaço também). Nota sem
    título nenhum é UM pedaço só. Pedaço em branco não vira número."""
    pedacos: list[list[str]] = [[]]
    for linha in texto.splitlines():
        if re.match(r"#{1,6} ", linha):
            pedacos.append([])
        pedacos[-1].append(linha)
    return [p for p in ("\n".join(linhas).strip() for linhas in pedacos) if p]


# --- o índice em disco --------------------------------------------------------

def _carregar() -> dict:
    """O índice salvo — ou um vazio se sumiu/corrompeu (aí re-embute tudo; é o
    best-effort de sempre: índice é derivado, as notas são a verdade)."""
    try:
        with open(_arquivo(), encoding="utf-8") as f:
            dados = json.load(f)
    except (OSError, ValueError):
        return {}
    return dados if isinstance(dados, dict) and dados.get("modelo") == MODELO else {}


def atualizar() -> dict:
    """Sincroniza o índice com a pasta do grafo e o devolve: nota nova/mudada
    (mtime+tamanho) é picada e re-embutida; nota apagada sai. Rotina sem
    mudança custa um stat por nota — nada de embutir.

    Devolve {"notas": {nome: {"secoes": [vetor, ...]}}} (só o que a busca usa).
    Levanta `IndiceIndisponivel` se HÁ mudança e o modelo não carrega."""
    velho = _carregar().get("notas", {})
    notas: dict[str, dict] = {}
    pendentes: list[tuple[str, list[str]]] = []  # (nome, seções por embutir)

    for caminho in memoria.listar():
        nome = caminho.stem
        try:
            stat = caminho.stat()
            marca = f"{stat.st_mtime_ns}:{stat.st_size}"
            guardada = velho.get(nome)
            if guardada and guardada.get("marca") == marca:
                notas[nome] = guardada
                continue
            secoes = picar(caminho.read_text(encoding="utf-8"))
        except OSError:
            continue  # sumiu no meio da varredura: fica de fora desta rodada
        if secoes:
            notas[nome] = {"marca": marca}
            pendentes.append((nome, secoes))

    if pendentes:
        # UM lote pro modelo com as seções de todas as notas mudadas — e cada
        # seção leva o prefixo de DOCUMENTO com o nome da nota como título.
        textos = [
            _PREFIXO_NOTA.format(titulo=nome, texto=secao)
            for nome, secoes in pendentes
            for secao in secoes
        ]
        vetores = embutir(textos)
        cursor = 0
        for nome, secoes in pendentes:
            notas[nome]["secoes"] = vetores[cursor : cursor + len(secoes)]
            cursor += len(secoes)
        indice = {"modelo": MODELO, "notas": notas}
        try:
            arquivo = _arquivo()
            arquivo.parent.mkdir(parents=True, exist_ok=True)
            with open(arquivo, "w", encoding="utf-8") as f:
                json.dump(indice, f)
        except OSError:
            pass  # índice é derivado: não conseguir salvar não trava a busca
        return indice
    return {"modelo": MODELO, "notas": notas}


def procurar(consulta: str) -> list[tuple[str, float]]:
    """Aponta as notas por parecença com a consulta: [(nome, score)], da mais
    parecida pra menos. O score da nota é o da MELHOR seção dela (o picadinho
    mira; a nota inteira é quem vai pro cérebro depois).

    Sincroniza o índice antes — é o "quando ele se atualiza". Levanta
    `IndiceIndisponivel` com o modelo indisponível (sempre precisa dele: no
    mínimo a consulta vira número)."""
    notas = atualizar().get("notas", {})
    if not notas:
        return []
    (vetor_consulta,) = embutir([f"{_PREFIXO_CONSULTA}{consulta}"])
    pontuadas = [
        (nome, max(_cosseno(vetor_consulta, v) for v in dados["secoes"]))
        for nome, dados in notas.items()
        if dados.get("secoes")
    ]
    return sorted(pontuadas, key=lambda par: par[1], reverse=True)
