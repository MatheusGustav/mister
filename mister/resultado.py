"""O que uma tool devolve: um resultado estruturado, não um texto solto.

Antes, o sistema adivinhava o sucesso farejando a frase ("tem a palavra
'falhou'?"). Agora ele SABE: `ok` é um sinal, não um chute. Isso fecha o
"observar" do ciclo agir->observar->corrigir, e a `sugestao` carrega a saída
óbvia quando algo dá errado (o "corrigir", decidido em código, não pelo modelo).
"""
from __future__ import annotations

from dataclasses import dataclass


@dataclass
class Resultado:
    ok: bool          # deu certo? (sinal, não frase)
    mensagem: str     # o texto principal para o usuário
    sugestao: str = ""  # quando deu errado, a saída óbvia (nomes parecidos, etc.)

    def texto(self) -> str:
        """Como mostrar ao usuário: mensagem + sugestão, se houver."""
        return f"{self.mensagem}\n{self.sugestao}" if self.sugestao else self.mensagem
