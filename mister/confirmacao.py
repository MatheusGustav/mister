"""O mecanismo de confirmação.

Uma tool que precisa de aval antes de agir levanta `PrecisaConfirmar`. O
despachante captura isso e devolve um `Pendente` em vez de uma resposta pronta;
o loop então pergunta ao usuário e, se ele topar, despacha de novo — agora com
o sinal de confirmado. Assim a decisão de "fazer mesmo assim" é SEMPRE do
usuário, nunca do modelo.

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
from dataclasses import dataclass, field
from pathlib import Path

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
    """Levantada por uma tool que não vai agir sem o usuário confirmar."""

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
