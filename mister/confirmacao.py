"""O mecanismo de confirmação.

Uma chamada que precisa de aval antes de agir levanta `PrecisaConfirmar`. O
despachante captura isso e devolve um `Pendente` em vez de uma resposta pronta;
o loop então pergunta ao usuário e, se ele topar, despacha de novo — agora com
o sinal de confirmado. Assim a decisão de "fazer mesmo assim" é SEMPRE do
usuário, nunca do modelo.

O CRITÉRIO é "dá pra desfazer?", não "é escrita?" — e quem julga é o
DESPACHANTE (`pergunta_de_confirmacao`, olhando intenção+params), não a tool.
A tool não decide mais nada; só executa.

CARIMBO ANTI-DRIFT: o `Pendente` nasce com a impressão digital do que o dono vai
aprovar — intenção, params exatos, pasta de trabalho e o conteúdo dos
arquivos-operando. Na execução, o despachante recalcula e NEGA se algo mudou
entre o "s" e o rodar (params trocados, arquivo substituído no meio do caminho).
O que o dono aprova é o que roda — garantido por código, não por confiança.
"""
from __future__ import annotations

import hashlib
import json
import os
import re
from dataclasses import dataclass, field
from pathlib import Path

# CAMPOS INTERNOS: existem no formulário da tool, mas são do SISTEMA, não do
# cérebro. Ele nem os enxerga (o prompt os esconde — ver prompts._schema_params)
# e, se um modelo enganado preenchê-los mesmo assim, o despachante os DESCARTA;
# só a decisão carimbada pelo laço, depois do "s" do dono, os mantém.
#
# A lista mora AQUI (a peça da confirmação) pra ser UMA só: prompt e despachante
# leem a mesma. Campo com default que NÃO está nesta lista é opção legítima da
# tool (ex.: a 'pasta' do celular) — o cérebro vê e pode preencher.
CAMPOS_INTERNOS = frozenset({"confirmado"})

# Teto do hash de arquivo-operando: acima disso, carimba só tamanho+mtime (a
# máquina é magra — nada de ler gigas pra RAM; hash é em streaming mesmo assim).
_TETO_HASH_BYTES = 64 * 1024 * 1024


def _digerir_operandos(params: dict) -> dict[str, str]:
    """Impressão digital dos ARQUIVOS que os params apontam (se apontarem).
    Qualquer param string que seja um arquivo existente entra: se o conteúdo
    for trocado entre a aprovação e a execução, o carimbo não bate mais."""
    digests: dict[str, str] = {}
    for valor in params.values():
        if not isinstance(valor, str) or not valor.strip():
            continue
        try:
            caminho = Path(valor).expanduser()
            if not caminho.is_file():
                continue
            stat = caminho.stat()
            if stat.st_size > _TETO_HASH_BYTES:
                # Grande demais pra hashear a cada vez: tamanho+mtime detectam troca.
                digests[str(caminho)] = f"grande:{stat.st_size}:{stat.st_mtime_ns}"
                continue
            h = hashlib.sha256()
            with caminho.open("rb") as f:
                for bloco in iter(lambda: f.read(1 << 20), b""):
                    h.update(bloco)
            digests[str(caminho)] = h.hexdigest()
        except OSError:
            continue  # sumiu/sem permissão: não entra; se EXISTIR na execução, muda o carimbo
    return digests


def carimbar(intencao: str, params: dict) -> str:
    """O CARIMBO: hash determinístico do que o dono aprova — intenção + params
    exatos + cwd + digest dos arquivos-operando. Mesmo material → mesmo carimbo;
    qualquer drift (param editado, arquivo trocado, cwd diferente) → carimbo novo."""
    material = {
        "intencao": intencao,
        "params": params,
        "cwd": os.getcwd(),
        "arquivos": _digerir_operandos(params),
    }
    canonico = json.dumps(material, sort_keys=True, ensure_ascii=False, default=str)
    return hashlib.sha256(canonico.encode("utf-8")).hexdigest()


class PrecisaConfirmar(Exception):
    """Levantada (pelo despachante, ver `pergunta_de_confirmacao`) quando uma
    ação não vai rodar sem o usuário confirmar."""

    def __init__(self, pergunta: str):
        super().__init__(pergunta)
        self.pergunta = pergunta


@dataclass
class Pendente:
    """O que o despachante devolve quando uma ação está à espera de confirmação.
    Carrega como repetir a mesma ação (mesma intenção e params), já marcada como
    confirmada, para o loop despachar de novo se o usuário topar.

    `carimbo` é a impressão digital (ver `carimbar`) do que o dono está
    aprovando — o despachante recalcula na execução e nega se não bater."""

    pergunta: str
    intencao: str
    params: dict = field(default_factory=dict)
    carimbo: str = ""

    def __post_init__(self) -> None:
        if not self.carimbo:
            self.carimbo = carimbar(self.intencao, self.params)


# --- o critério de irreversibilidade -----------------------------------------

# Lista NEGRA de propósito, e lista negra vaza — comando perigoso que não está
# aqui passa sem perguntar. Engordar conforme aparecer um fora dela. Não é
# tarefa deste código julgar se a ação saiu do que foi pedido: isso é
# entendimento do modelo, não regra mecânica.
_VERBOS_DESTRUTIVOS = frozenset({"rm", "shred", "dd", "mkfs", "truncate"})
_COMBOS_DESTRUTIVOS = ("git reset --hard", "git clean")


def _comando_e_destrutivo(comando: str) -> bool:
    """O comando bate na lista negra: um verbo destrutivo (rm, shred, dd,
    mkfs, truncate — olhando só a PRIMEIRA palavra de cada pedaço, sudo
    incluso), um combo do git, ou um '>' por cima de arquivo que já existe."""
    texto = comando.strip()
    if any(combo in texto for combo in _COMBOS_DESTRUTIVOS):
        return True
    for pedaco in re.split(r"&&|\|\||;|\|", texto):
        partes = pedaco.split()
        if not partes:
            continue
        verbo = partes[0]
        if verbo == "sudo" and len(partes) > 1:
            verbo = partes[1]
        # mkfs vem com sufixo do filesystem (mkfs.ext4, mkfs.vfat...).
        if verbo in _VERBOS_DESTRUTIVOS or verbo.startswith("mkfs."):
            return True
    for alvo in re.findall(r">\s*([^\s&|;>]+)", texto):
        if Path(alvo).expanduser().exists():
            return True
    return False


def pergunta_de_confirmacao(intencao: str, params: dict) -> str | None:
    """A pergunta que o DESPACHANTE faz antes de executar, se a chamada bater
    no critério de irreversibilidade — None quando não precisa perguntar.

    Só duas intenções pedem hoje: `apagar_nota` SEMPRE, e `rodar_comando`
    quando o comando bate na lista negra de destrutivos (`_comando_e_destrutivo`).
    O resto das tools não pergunta mais — quem grava sem apagar nada (puxar do
    celular, guardar regra, escrever arquivo) não é irreversível."""
    if intencao == "apagar_nota":
        nome = params.get("nome", "?")
        return f"Apago a nota '{nome}'? Não tem cópia de segurança, não dá pra desfazer."
    if intencao == "rodar_comando":
        comando = str(params.get("comando", ""))
        if _comando_e_destrutivo(comando):
            return f"Esse comando parece destrutivo — rodo mesmo assim? '{comando}'"
    return None
