"""O ÍNDICE — os números que apontam ONDE a memória está.

A porta de entrada do grafo é POR SIGNIFICADO: um modelo pequeno (o
embeddinggemma, no Ollama local) transforma texto em números, e comparar
números acha a nota mais parecida com o que o dono falou — mesmo escrita com
outras palavras. Os números só apontam; quem vai pro cérebro é o TEXTO da
nota, inteiro (isso é da leitura automática, na etapa seguinte).

O PICADINHO: nota grande tratada como bloco único vira números borrados — uma
média de dez assuntos não casa bem com nenhum. Então o índice pica a nota por
dentro (por seção de markdown) só pra MIRAR melhor; o score da nota é o da
MELHOR seção. A nota no disco continua inteira — o picadinho é invisível,
existe só aqui.

Onde mora e quando atualiza (escolha de construção): um JSON em
`~/.mister/indice.json`, sincronizado ANTES de cada busca — só o que mudou
(mtime+tamanho) é re-embutido, então a varredura de rotina é um stat por nota.

A busca é LOCAL: custa tempo e espaço, não dinheiro — nada aqui vai à API do
cérebro. Mas o Ollama daqui é instalação de usuário, SEM serviço: ninguém sobe
ele no boot. Servidor fora do ar vira `IndiceIndisponivel` com a receita — quem
chama responde sem memória e avisa curto, no mesmo jeito ekodide.

Os PREFIXOS de tarefa ("task: search result | query:", "title: | text:") são o
dialeto de busca do embeddinggemma. Calibrado em português nesta máquina
(13/08/2026): com eles, consulta certa pontua 0.41–0.54 e a melhor errada
0.26; sem eles, a errada encosta em 0.42. É o que sustenta os limiares da
caminhada (ver a leitura automática).
"""
from __future__ import annotations

import json
import math
import os
import re
import urllib.error
import urllib.request
from pathlib import Path

from mister import memoria

ARQUIVO_PADRAO = str(Path.home() / ".mister" / "indice.json")
URL_PADRAO = "http://127.0.0.1:11434"
MODELO = "embeddinggemma"

# O dialeto de busca do embeddinggemma: consulta e documento levam prefixos
# DIFERENTES — é assim que o modelo foi treinado a aproximar pergunta de
# resposta, não frase de frase igual.
_PREFIXO_CONSULTA = "task: search result | query: "
_PREFIXO_NOTA = "title: {titulo} | text: {texto}"

RECEITA_OLLAMA = (
    "Suba o servidor local de embeddings: rode 'ollama serve' num terminal "
    "(ou 'systemd-run --user ollama serve'). O modelo é o embeddinggemma, "
    "que já está baixado."
)


class IndiceIndisponivel(RuntimeError):
    """O servidor local de embeddings não respondeu — sem ele não há busca por
    significado. NÃO é erro de memória perdida: as notas seguem no disco; só a
    porta de entrada está fechada."""


def _url() -> str:
    """Lê MISTER_OLLAMA A CADA chamada — o conftest aponta os testes pra uma
    porta morta, pra ninguém sair embutindo de verdade sem querer."""
    return os.environ.get("MISTER_OLLAMA", URL_PADRAO).rstrip("/")


def _arquivo() -> Path:
    return Path(os.environ.get("MISTER_INDICE", ARQUIVO_PADRAO))


# --- o comparador -------------------------------------------------------------

def embutir(textos: list[str]) -> list[list[float]]:
    """Texto -> números, no Ollama local (um lote por chamada). Levanta
    `IndiceIndisponivel` (com a receita na mensagem) se o servidor não estiver
    de pé ou o modelo não estiver lá."""
    corpo = json.dumps({"model": MODELO, "input": textos}).encode("utf-8")
    req = urllib.request.Request(
        f"{_url()}/api/embed",
        data=corpo,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        # 120s: a primeira chamada CARREGA o modelo (segundos); as seguintes
        # são rápidas. Timeout curto derrubaria justo a primeira lembrança.
        with urllib.request.urlopen(req, timeout=120) as resp:
            resposta = json.load(resp)
        vetores = resposta["embeddings"]
        if len(vetores) != len(textos):
            raise KeyError("lote fora do formato")
    except urllib.error.HTTPError as e:
        raise IndiceIndisponivel(
            f"o Ollama respondeu HTTP {e.code} — o modelo '{MODELO}' está lá? "
            f"(ollama pull {MODELO})"
        ) from e
    except (urllib.error.URLError, TimeoutError) as e:
        raise IndiceIndisponivel(f"o Ollama não respondeu. {RECEITA_OLLAMA}") from e
    except (KeyError, ValueError) as e:
        raise IndiceIndisponivel("o Ollama respondeu fora do formato esperado.") from e
    return vetores


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
    mudança custa um stat por nota — nada de Ollama.

    Devolve {"notas": {nome: {"secoes": [vetor, ...]}}} (só o que a busca usa).
    Levanta `IndiceIndisponivel` se HÁ mudança e o servidor está fora do ar."""
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
        # UM lote pro Ollama com as seções de todas as notas mudadas — e cada
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
    `IndiceIndisponivel` com o Ollama fora do ar (sempre precisa dele: no
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
