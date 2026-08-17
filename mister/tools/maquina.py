"""As tools de MÁQUINA — rodar comando, ler/escrever/procurar arquivo local.

Diferente do celular (que passa pelo ekodide, por outra máquina), aqui é
DIRETO no disco e no shell deste computador — por isso o cuidado fica todo do
lado da confirmação (`confirmacao.py`, item 3) e da trava de ler-antes
(`leituras.py`, item 5): `rodar_comando` só pergunta quando o comando bate na
lista negra de destrutivos, e `escrever_arquivo` recusa sobrescrever arquivo
que já existe e não foi lido nesta sessão — o resto roda direto.

`ler_arquivo` marca a leitura na trava, junto com `ler_nota` e a leitura
automática — é o mesmo registro compartilhado, não interessa se o que foi lido
é nota do grafo ou arquivo qualquer do disco.
"""
from __future__ import annotations

import os
import subprocess
from pathlib import Path

from mister import leituras
from mister.registry import Formulario, tool
from mister.resultado import Resultado

# --- rodar_comando -------------------------------------------------------------

TIMEOUT_COMANDO_S = 60
# Teto de caracteres da saída — comando espevitado (find na raiz, log gigante)
# não pode afogar o prompt do cérebro.
SAIDA_COMANDO_MAX = 20_000


class RodarComandoParams(Formulario):
    comando: str


@tool(
    "rodar_comando",
    RodarComandoParams,
    "RODA um comando no TERMINAL (shell) deste computador e devolve a saída "
    "(stdout+stderr). Use quando o usuário pedir pra rodar/instalar/testar "
    "algo, ver processos, mexer em git, etc. Comando DESTRUTIVO (rm, shred, "
    "dd, mkfs, truncate, 'git reset --hard', 'git clean', ou '>' por cima de "
    "arquivo que já existe) pede a confirmação do dono antes de rodar — o "
    f"resto roda direto. Corta depois de {TIMEOUT_COMANDO_S}s. 'comando' é a "
    "linha exata de shell, do jeito que se digitaria no terminal.",
)
def rodar_comando(params: RodarComandoParams) -> Resultado:
    comando = params.comando.strip()
    if not comando:
        return Resultado(False, "O comando veio vazio.")
    try:
        processo = subprocess.run(
            comando, shell=True, capture_output=True, text=True,
            errors="replace", timeout=TIMEOUT_COMANDO_S,
        )
    except subprocess.TimeoutExpired:
        return Resultado(
            False,
            f"O comando passou de {TIMEOUT_COMANDO_S}s rodando e foi cortado.",
            comando,
        )
    except OSError as erro:
        return Resultado(False, f"Não consegui rodar o comando: {erro}")

    saida = (processo.stdout or "") + (processo.stderr or "")
    cortado = len(saida) > SAIDA_COMANDO_MAX
    if cortado:
        saida = saida[:SAIDA_COMANDO_MAX]
    saida = saida.strip() or "(sem saída)"
    aviso = f"Saída cortada em {SAIDA_COMANDO_MAX} caracteres." if cortado else ""

    if processo.returncode == 0:
        return Resultado(True, saida, aviso)
    falha = f"O comando terminou com código {processo.returncode}."
    return Resultado(False, falha, f"{saida}\n{aviso}".strip() if aviso else saida)


# --- ler_arquivo -----------------------------------------------------------

class LerArquivoParams(Formulario):
    caminho: str


@tool(
    "ler_arquivo",
    LerArquivoParams,
    "LÊ o conteúdo INTEIRO de um arquivo local do disco — SEM limite de "
    "linhas, o arquivo vem inteiro. Use pra examinar um arquivo do computador "
    "do dono. 'caminho' é o caminho do arquivo ('~' expande pra pasta do "
    "usuário). Ler aqui é o que destrava escrever_arquivo depois, se o "
    "arquivo já existir.",
)
def ler_arquivo(params: LerArquivoParams) -> Resultado:
    caminho = Path(params.caminho).expanduser()
    try:
        texto = caminho.read_text(encoding="utf-8")
    except FileNotFoundError:
        return Resultado(False, f"Não achei o arquivo '{params.caminho}'.")
    except UnicodeDecodeError:
        return Resultado(
            False,
            f"'{params.caminho}' não é texto por dentro (bytes fora do UTF-8) — "
            "binário disfarçado.",
        )
    except OSError as erro:
        return Resultado(False, f"Não consegui ler '{params.caminho}': {erro}")
    leituras.marcar(caminho)
    return Resultado(True, texto)


# --- escrever_arquivo --------------------------------------------------------

class EscreverArquivoParams(Formulario):
    caminho: str
    conteudo: str


@tool(
    "escrever_arquivo",
    EscreverArquivoParams,
    "GRAVA/EDITA um arquivo local do disco — o conteúdo INTEIRO entra no lugar "
    "do arquivo (cria o arquivo, e as pastas no meio do caminho, se não "
    "existirem). Arquivo que JÁ EXISTE exige ter sido lido antes com "
    "ler_arquivo nesta conversa — senão recusa, pra nunca sobrescrever sem "
    "saber o que tinha. Arquivo NOVO não exige nada. Não pede confirmação do "
    "dono (a trava de ler-antes já garante que você sabe o que está "
    "sobrescrevendo).",
)
def escrever_arquivo(params: EscreverArquivoParams) -> Resultado:
    caminho = Path(params.caminho).expanduser()
    if caminho.exists() and not leituras.foi_lido(caminho):
        return Resultado(
            False,
            f"Preciso ler '{params.caminho}' antes de sobrescrever — ele já existe.",
            "Use ler_arquivo primeiro.",
        )
    try:
        caminho.parent.mkdir(parents=True, exist_ok=True)
        caminho.write_text(params.conteudo, encoding="utf-8")
    except OSError as erro:
        return Resultado(False, f"Não consegui gravar '{params.caminho}': {erro}")
    leituras.marcar(caminho)
    return Resultado(True, f"Gravei '{params.caminho}'.")


# --- procurar_arquivo ---------------------------------------------------------

# Pastas puladas na varredura: pesadas (dependências, caches) ou de controle de
# versão — achar arquivo AÍ dentro quase nunca é o que o dono quer, e olhar
# elas transformaria uma busca rápida numa varredura de minutos.
_PASTAS_IGNORADAS = {"node_modules", "__pycache__", ".venv", "venv", ".cache"}
RESULTADOS_MAX = 50
# Teto de arquivos OLHADOS (não só achados) — a máquina é magra, uma busca
# perdida numa pasta gigante não pode travar o Mister; para e avisa.
VARREDURA_MAX = 200_000


class ProcurarArquivoParams(Formulario):
    nome: str = ""
    conteudo: str = ""
    pasta: str = "~"


@tool(
    "procurar_arquivo",
    ProcurarArquivoParams,
    "PROCURA arquivo local por NOME e/ou por CONTEÚDO, varrendo uma pasta e "
    "suas subpastas. Use pra achar um arquivo cujo caminho exato você não "
    "sabe. 'nome' é um pedaço do NOME do arquivo (não diferencia maiúscula); "
    "'conteudo' é um texto que precisa estar DENTRO do arquivo (só arquivo de "
    "texto é olhado por dentro — binário é pulado calado); passe pelo menos "
    "um dos dois. 'pasta' é onde começar (default: a pasta do usuário). Pula "
    "pastas pesadas/ocultas (node_modules, .venv, .git...) e para nos "
    f"primeiros {RESULTADOS_MAX} achados.",
)
def procurar_arquivo(params: ProcurarArquivoParams) -> Resultado:
    nome_alvo = params.nome.strip().lower()
    conteudo_alvo = params.conteudo.strip()
    if not nome_alvo and not conteudo_alvo:
        return Resultado(False, "Preciso de 'nome' ou 'conteudo' pra procurar por alguma coisa.")
    raiz = Path(params.pasta or "~").expanduser()
    if not raiz.is_dir():
        return Resultado(False, f"'{params.pasta}' não é uma pasta que existe.")

    achados: list[str] = []
    varridos = 0
    parou_cedo = False
    for atual, subpastas, arquivos in os.walk(raiz):
        subpastas[:] = [
            p for p in subpastas if p not in _PASTAS_IGNORADAS and not p.startswith(".")
        ]
        for nome_arquivo in arquivos:
            if len(achados) >= RESULTADOS_MAX or varridos >= VARREDURA_MAX:
                parou_cedo = True
                break
            varridos += 1
            if nome_alvo and nome_alvo not in nome_arquivo.lower():
                continue
            caminho = Path(atual) / nome_arquivo
            if conteudo_alvo:
                try:
                    if conteudo_alvo not in caminho.read_text(encoding="utf-8"):
                        continue
                except (OSError, UnicodeDecodeError):
                    continue  # binário/sem permissão: pulado calado, não é erro
            achados.append(str(caminho))
        if parou_cedo:
            break

    if not achados:
        return Resultado(
            True, "Não achei nada.",
            "Confira a pasta ou tente um pedaço menor do nome/conteúdo.",
        )
    aviso = (
        f"Parei nos primeiros {len(achados)} — pode ter mais, refine a busca."
        if parou_cedo else ""
    )
    return Resultado(True, "\n".join(achados), aviso)
