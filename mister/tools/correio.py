"""As tools do CELULAR — cascas finas sobre o EKODIDE.

O Mister não tem correio dentro dele. Quem manda e busca arquivo entre o PC e o
celular é o **ekodide**: projeto separado (repo e pacote PyPI próprios), com o
maquinário todo lá dentro — lacre, cofre, carteiro, buscador, vizinhança — e o
APK do lado do telefone. Aqui só se ACIONA e se TRADUZ o resultado neutro dele
(`EnvioResultado`, a tupla do puxar) pra fala do Mister (`Resultado`).

O que isso implica, e que não deve ser desfeito por conveniência:

  - O Mister **não conhece** o protocolo, nem as portas, nem a cifra. Se algo do
    transporte precisa mudar, muda no repo do ekodide — nunca aqui dentro.
  - A config do correio é **do ekodide** (`~/.config/ekodide/config.json` +
    `EKODIDE_SEGREDO`), e o pareamento com o celular acontece FORA do Mister
    (`ekodide pair`). Aqui a gente lê, nunca gerencia.
  - O ekodide é um extra **opcional**: sem ele instalado, estas tools recusam
    com a receita de instalar em vez de estourar. Import duro no topo derrubaria
    o app inteiro na largada — por isso o try/except e o `_disponivel()` único.
"""
from __future__ import annotations

import difflib
from pathlib import Path

from pydantic import BaseModel

from mister import envios
from mister.registry import tool
from mister.resultado import Resultado

try:  # extra OPCIONAL: o Mister tem que subir e rodar sem ele
    import ekodide
    from ekodide import config as ekodide_config
    from ekodide import vizinhanca as ekodide_vizinhanca
except ImportError:
    ekodide = None
    ekodide_config = None
    ekodide_vizinhanca = None

# A VERSÃO importa: o `espiar` (que o olhar_no_celular usa) só existe a partir
# da 0.1.1 — a 0.1.0 é de antes dele. Por isso a receita crava a versão, e o
# extra `celular` do pyproject.toml pede `ekodide>=0.1.1`.
RECEITA_EKODIDE = (
    "Instale o ekodide: pip install 'ekodide>=0.1.1' — depois pareie com o "
    "celular (ekodide pair) e cadastre o destino "
    "(ekodide config destino celular http://IP:8778)."
)

# O nome do celular na config do ekodide. É o mesmo apelido que o `ekodide send
# --para celular` usa — trocar aqui sem trocar lá quebra os dois.
DESTINO_CELULAR = "celular"


# --- o seam do extra opcional ------------------------------------------------

def _disponivel() -> bool:
    """O ekodide está instalado? UM lugar só responde isso — as cinco tools
    passam por aqui antes de qualquer coisa."""
    return ekodide is not None


def _sem_ekodide() -> Resultado:
    return Resultado(
        False,
        "Essa ferramenta precisa do 'ekodide' (o correio entre PC e celular) e "
        "ele não está instalado.",
        RECEITA_EKODIDE,
    )


def _sem_correio(erro: Exception) -> Resultado:
    """Config incompleta (sem segredo, sem destino) vira recusa com a receita."""
    return Resultado(
        False,
        f"O correio não está pronto: {erro}",
        "Pareie com 'ekodide pair' e cadastre o destino com "
        "'ekodide config destino celular http://IP:8778'.",
    )


def _endereco(nome: str) -> str:
    """URL do destino: a config vence (rápido); se o nome não está lá, procura o
    aparelho na rede pelo nome (funciona mesmo com o IP trocado pelo DHCP).
    Levanta ErroConfig se não achar de jeito nenhum. Mesma regra do CLI do
    ekodide, só que CALADA — aqui é o cérebro acionando, não um humano."""
    try:
        return ekodide_config.url_do_destino(nome)
    except ekodide_config.ErroConfig:
        pass  # não cadastrado — tenta a descoberta na rede
    for aparelho in ekodide_vizinhanca.procurar():
        if aparelho.get("nome") == nome:
            return ekodide_vizinhanca.url_de(aparelho)
    raise ekodide_config.ErroConfig(f"Não achei '{nome}' (nem na config, nem na rede).")


def _linha_do_celular() -> tuple[str, str]:
    """(url, segredo) do celular — o par que toda tool de rede precisa."""
    return _endereco(DESTINO_CELULAR), ekodide_config.segredo()


# --- tradutores (dado cru -> fala do Mister) ---------------------------------

def _tam_humano(n: int) -> str:
    """Tamanho legível: 2.0 KB, 14.8 MB… (pra listagem da pasta do celular)."""
    tam = float(n)
    for unidade in ("B", "KB", "MB", "GB", "TB"):
        if tam < 1024 or unidade == "TB":
            return f"{int(tam)} {unidade}" if unidade == "B" else f"{tam:.1f} {unidade}"
        tam /= 1024
    return f"{n} B"


def _sugerir_parecidos(alvo: Path) -> str:
    """Se o arquivo não existe, sugere nomes parecidos na mesma pasta (o
    'corrigir' do envio — só vale pra arquivo do disco, então fica no Mister)."""
    pasta = alvo.parent
    if not pasta.is_dir():
        return ""
    try:
        vizinhos = [x.name for x in pasta.iterdir()]
    except OSError:
        return ""
    parecidos = difflib.get_close_matches(alvo.name, vizinhos, n=3, cutoff=0.5)
    return ("Parecidos: " + ", ".join(f"'{n}'" for n in parecidos) + ".") if parecidos else ""


def _fala_do_envio(r, origem: Path, alvo: str) -> str:
    """Traduz o `EnvioResultado` neutro do ekodide pra fala de CONCLUSÃO do
    Mister (uma frase só — é o que o laço injeta no histórico quando o envio em
    segundo plano termina)."""
    if r.is_pasta and r.total == 0:
        return f"A pasta '{origem.name}' está vazia — nada foi enviado."
    if r.ok:
        feito = (
            f"Mandei {r.enviados} de {r.total} arquivo(s) da pasta '{origem.name}' pro {alvo}."
            if r.is_pasta else f"Mandei '{origem.name}' pro {alvo}."
        )
        if r.falhas:
            return f"{feito} Falharam {len(r.falhas)}: " + "; ".join(r.falhas[:3])
        return f"{feito} Chegou em: {r.destino}" if r.destino else feito
    motivo = "; ".join(r.falhas[:3]) if r.falhas else (
        "O celular está com o app do ekodide aberto e pareado, na mesma rede?"
    )
    return f"Não consegui enviar '{origem.name}' pro {alvo}. {motivo}"


def _caminho_remoto(pasta: str, nome: str) -> str:
    """O nome que o ekodide entende: ele lista a pasta compartilhada INTEIRA, de
    forma recursiva, com caminho relativo ('Fotos/sub/img.png'). Então 'pasta' +
    'nome' viram um caminho relativo só."""
    pasta = (pasta or "").strip().strip("/")
    return f"{pasta}/{nome}" if pasta else nome


# --- formulários (Pydantic) --------------------------------------------------

class SemParametros(BaseModel):
    """A tool não precisa de nada."""


class EnviarParaCelularParams(BaseModel):
    caminho: str


class OlharPastaCelularParams(BaseModel):
    pasta: str = ""


class PuxarDoCelularParams(BaseModel):
    nome: str
    pasta: str = ""


class OlharNoCelularParams(BaseModel):
    nome: str
    pasta: str = ""


# --- as tools ----------------------------------------------------------------

@tool(
    "enviar_para_celular",
    EnviarParaCelularParams,
    "ENVIA pro CELULAR (telefone), pela rede, um ARQUIVO ou uma PASTA INTEIRA do "
    "computador. Use quando o usuário disser 'manda esse arquivo pro celular', "
    "'passa o PDF pro telefone', 'joga essa pasta pro celular'. O parâmetro "
    "'caminho' é o arquivo OU a pasta AQUI NO PC (se for pasta, vai tudo, "
    "preservando as subpastas). O envio roda em segundo plano: a resposta volta "
    "na hora e o desfecho chega depois.",
)
def enviar_para_celular(params: EnviarParaCelularParams) -> Resultado:
    if not _disponivel():
        return _sem_ekodide()
    origem = Path(params.caminho).expanduser()
    # O que dá pra conferir SEM rede é síncrono: erro de digitação merece
    # resposta imediata, não um "tá indo" seguido de fracasso.
    if not origem.exists():
        return Resultado(False, f"Não achei: {origem}", _sugerir_parecidos(origem))
    try:
        url, segredo = _linha_do_celular()
    except ekodide_config.ErroConfig as erro:
        return _sem_correio(erro)

    descricao = f"'{origem.name}' → celular"
    envios.disparar(descricao, lambda: _fala_do_envio(
        ekodide.enviar(origem, url, segredo), origem, "celular"
    ))
    return Resultado(
        True,
        f"Comecei a enviar '{origem.name}' pro celular em segundo plano.",
        "Sigo livre enquanto vai; aviso quando terminar (andamento_envios mostra como está).",
    )


@tool(
    "olhar_pasta_celular",
    OlharPastaCelularParams,
    "OLHA uma pasta DO CELULAR daqui do PC, pela rede: lista os arquivos e as "
    "subpastas daquele nível, como um 'ls' remoto. Use quando o usuário quiser "
    "saber o que tem no celular ('que fotos tem na câmera?', 'lista os prints', "
    "'o que tem em Download do celular?') ou pra ACHAR o nome do arquivo antes "
    "de puxar com puxar_do_celular. O parâmetro 'pasta' é o caminho dentro do "
    "que o celular compartilhou (ex.: 'DCIM/Camera', 'Download'); vazio = a "
    "raiz, pra descobrir que pastas existem.",
)
def olhar_pasta_celular(params: OlharPastaCelularParams) -> Resultado:
    if not _disponivel():
        return _sem_ekodide()
    try:
        url, segredo = _linha_do_celular()
    except ekodide_config.ErroConfig as erro:
        return _sem_correio(erro)
    try:
        itens = ekodide.listar_remoto(url, segredo)
    except ekodide.ErroPuxar as erro:
        return Resultado(
            False,
            "Não consegui olhar o celular.",
            f"{erro}. O celular está ligado, pareado, na mesma rede e com uma "
            "pasta compartilhada no app?",
        )

    # O ekodide devolve a pasta compartilhada INTEIRA, recursiva, com caminho
    # relativo. A vista de UM nível é recorte local — nada disso vira ida extra
    # à rede, e o protocolo fica intocado.
    prefixo = (params.pasta or "").strip().strip("/")
    prefixo = f"{prefixo}/" if prefixo else ""
    arquivos, subpastas = [], set()
    for item in itens:
        nome = str(item.get("nome", ""))
        if not nome.startswith(prefixo):
            continue
        resto = nome[len(prefixo):]
        if "/" in resto:
            subpastas.add(resto.split("/", 1)[0])
        else:
            arquivos.append((resto, int(item.get("tamanho", 0))))

    if not arquivos and not subpastas:
        onde = f"'{params.pasta}'" if params.pasta else "a pasta compartilhada"
        return Resultado(
            True,
            f"Não vi nada em {onde} do celular.",
            "Confira o nome da pasta (olhe a raiz com o parâmetro vazio) ou o "
            "que o app está compartilhando.",
        )
    linhas = [f"[pasta] {p}/" for p in sorted(subpastas)]
    linhas += [f"{_tam_humano(tam):>9}  {nome}" for nome, tam in sorted(arquivos)]
    return Resultado(True, "\n".join(linhas))


@tool(
    "puxar_do_celular",
    PuxarDoCelularParams,
    "PUXA (baixa) pro PC um arquivo DO CELULAR, pela rede — GRAVA o arquivo aqui. "
    "Use quando o usuário quiser trazer algo do telefone ('pega a última foto da "
    "câmera', 'traz o print', 'baixa aquele PDF do celular'). O parâmetro 'nome' "
    "é o nome do arquivo como aparece no olhar_pasta_celular, e 'pasta' é a "
    "pasta do celular onde ele está (vazio = a raiz do compartilhado). Se não "
    "souber o nome exato, olhe a pasta primeiro.",
)
def puxar_do_celular(params: PuxarDoCelularParams) -> Resultado:
    if not _disponivel():
        return _sem_ekodide()
    alvo = _caminho_remoto(params.pasta, params.nome)
    try:
        url, segredo = _linha_do_celular()
    except ekodide_config.ErroConfig as erro:
        return _sem_correio(erro)
    # Onde cai o que chega é decisão do EKODIDE (a config dele), não do Mister.
    receber = ekodide_config.carregar().get("receber") or {}
    base = Path(receber.get("dir") or "~/Downloads").expanduser()
    ok, info = ekodide.puxar(alvo, url, segredo, base)
    if ok:
        return Resultado(True, f"Puxei '{alvo}' do celular.", f"Salvo em: {info}")
    return Resultado(
        False,
        f"Não consegui puxar '{alvo}' do celular.",
        f"{info}. O nome está certo? Olhe a pasta com olhar_pasta_celular primeiro.",
    )


# --- os óculos: a espiada (olhar ≠ puxar) ------------------------------------

# A espiada mostra os primeiros 100 KB e avisa que cortou — espiar não é ler
# livro. O `limite` do ekodide corta na origem: o resto nem viaja.
ESPIADA_MAX = 100 * 1024

# O funil dos óculos: por ora só a língua nativa (texto). Os tipos das próximas
# etapas recusam com jeito, dizendo o que ainda não se enxerga — melhor que
# despejar bytes ilegíveis na conversa.
_OCULOS_TEXTO = {
    ".txt", ".md", ".markdown", ".json", ".jsonl", ".log", ".csv", ".tsv",
    ".xml", ".html", ".htm", ".css", ".ini", ".cfg", ".conf", ".toml",
    ".yaml", ".yml", ".py", ".js", ".ts", ".sh", ".kt", ".java", ".c", ".h",
    ".cpp", ".sql",
}
_OCULOS_FUTUROS = {
    ".jpg": "foto", ".jpeg": "foto", ".png": "foto", ".webp": "foto",
    ".gif": "foto", ".bmp": "foto", ".heic": "foto",
    ".pdf": "PDF",
    ".mp4": "vídeo", ".3gp": "vídeo", ".mkv": "vídeo", ".webm": "vídeo",
    ".mp3": "áudio", ".ogg": "áudio", ".opus": "áudio", ".m4a": "áudio",
    ".wav": "áudio", ".aac": "áudio",
}


@tool(
    "olhar_no_celular",
    OlharNoCelularParams,
    "LÊ (espia) o CONTEÚDO de um arquivo de TEXTO do CELULAR daqui do PC, sem "
    "baixar nada: o conteúdo vem cifrado pela rede, fica só na memória e NENHUM "
    "arquivo é salvo no PC. Use quando o usuário quiser saber o que tem DENTRO "
    "de um arquivo do telefone ('lê aquele txt', 'o que diz esse json', 'me "
    "mostra essa nota') sem pedir pra trazer. Por enquanto só TEXTO (txt, md, "
    "json, log, csv, código) — foto, PDF, vídeo e áudio ainda não. O parâmetro "
    "'nome' é o nome do arquivo como aparece no olhar_pasta_celular, e 'pasta' "
    "é a pasta do celular onde ele está. Pra GUARDAR uma cópia no PC o certo é "
    "puxar_do_celular — olhar e puxar são atos diferentes.",
)
def olhar_no_celular(params: OlharNoCelularParams) -> Resultado:
    if not _disponivel():
        return _sem_ekodide()
    alvo = _caminho_remoto(params.pasta, params.nome)
    # O funil ANTES da viagem: recusar aqui poupa a rede e explica melhor.
    ext = Path(params.nome).suffix.lower()
    if ext in _OCULOS_FUTUROS:
        return Resultado(
            False,
            f"Ainda não tenho óculos pra {_OCULOS_FUTUROS[ext]} — por enquanto a "
            "espiada só lê TEXTO.",
            "Se precisar do arquivo no PC, use puxar_do_celular (aí é cópia de verdade).",
        )
    if ext not in _OCULOS_TEXTO:
        return Resultado(
            False,
            f"Não reconheço '{ext or params.nome}' como tipo de texto — a espiada "
            "só lê texto por enquanto.",
            "Se precisar do arquivo no PC, use puxar_do_celular.",
        )
    try:
        url, segredo = _linha_do_celular()
    except ekodide_config.ErroConfig as erro:
        return _sem_correio(erro)

    # NADA é gravado: o ekodide devolve os bytes na mão e eles morrem aqui. É
    # por isso que esta tool não pede confirmação, e é a diferença inteira entre
    # ela e o puxar_do_celular.
    ok, carga, tamanho = ekodide.espiar(alvo, url, segredo, limite=ESPIADA_MAX)
    if not ok:
        return Resultado(
            False,
            f"Não consegui espiar '{alvo}' no celular.",
            f"{carga}. O nome está certo? Olhe a pasta com olhar_pasta_celular primeiro.",
        )

    cortado = tamanho > len(carga)
    try:
        texto = carga.decode("utf-8")
    except UnicodeDecodeError as erro:
        # Corte no meio de um caractere multibyte não é binário — apara o rabo
        # quebrado. Se o erro for longe do fim, aí é binário disfarçado mesmo.
        if not (cortado and erro.start >= len(carga) - 3):
            return Resultado(
                False,
                f"'{params.nome}' não é texto por dentro (bytes fora do UTF-8) — "
                "binário disfarçado.",
                "Se quiser mesmo, puxar_do_celular traz o arquivo inteiro pro PC.",
            )
        texto = carga[: erro.start].decode("utf-8")

    aviso = (
        f" O arquivo é maior que a espiada: mostro só os primeiros "
        f"{_tam_humano(ESPIADA_MAX)} (cortei aqui)." if cortado else ""
    )
    cabecalho = (
        f"Olhei '{alvo}' ({_tam_humano(tamanho)}) do celular — só de passagem, "
        f"nada foi salvo no PC.{aviso}"
    )
    if not texto:
        return Resultado(True, f"{cabecalho} O arquivo está vazio.")
    return Resultado(True, f"{cabecalho}\n\n{texto}")


@tool(
    "andamento_envios",
    SemParametros,
    "Consulta o ANDAMENTO dos ENVIOS DE ARQUIVO que rodam em segundo plano (os "
    "disparados por enviar_para_celular). Use quando o usuário perguntar 'como "
    "tá o envio?', 'já foi o arquivo?', 'terminou de mandar?'. Não espera "
    "ninguém — só olha e conta. Envio que TERMINOU você fica sabendo sozinho (o "
    "desfecho chega no histórico).",
)
def andamento_envios(_: SemParametros) -> Resultado:
    voando = envios.em_voo()
    if not voando:
        return Resultado(True, "Nenhum envio em andamento agora.")
    linhas = "\n".join(f"  - {d}" for d in voando)
    return Resultado(True, f"Ainda enviando ({len(voando)}):\n{linhas}")
