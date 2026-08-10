"""A peça da confirmação em si: o pedido de aval e a impressão digital dele.

O carimbo é o que faz "o dono aprovou ISTO" ser verificável em código. O que o
despachante faz com ele está no test_dispatcher; aqui é a peça sozinha.
"""
import os

from mister.confirmacao import Pendente, PrecisaConfirmar, carimbar


def test_precisa_confirmar_carrega_a_pergunta():
    erro = PrecisaConfirmar("Posso puxar 'nota.txt' pro PC?")
    assert erro.pergunta == "Posso puxar 'nota.txt' pro PC?"
    assert "nota.txt" in str(erro)


def test_pendente_nasce_carimbado():
    p = Pendente(pergunta="pode?", intencao="puxar_do_celular", params={"nome": "x.txt"})
    assert p.carimbo == carimbar("puxar_do_celular", {"nome": "x.txt"})


def test_pendente_respeita_carimbo_dado():
    p = Pendente(pergunta="pode?", intencao="x", params={}, carimbo="ja-tinha")
    assert p.carimbo == "ja-tinha"


def test_carimbo_muda_com_a_intencao():
    assert carimbar("puxar_do_celular", {"n": 1}) != carimbar("enviar_para_celular", {"n": 1})


def test_carimbo_ignora_a_ordem_das_chaves():
    assert carimbar("x", {"a": 1, "b": 2}) == carimbar("x", {"b": 2, "a": 1})


def test_carimbo_muda_com_a_pasta_de_trabalho(tmp_path, monkeypatch):
    """'apaga isso aqui' num cwd não é a mesma ação em outro."""
    antes = carimbar("x", {"alvo": "nota.txt"})
    monkeypatch.chdir(tmp_path)
    assert carimbar("x", {"alvo": "nota.txt"}) != antes


def test_carimbo_enxerga_o_conteudo_do_arquivo(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    alvo = tmp_path / "nota.txt"
    alvo.write_text("um", encoding="utf-8")
    antes = carimbar("x", {"alvo": str(alvo)})
    alvo.write_text("outro", encoding="utf-8")
    assert carimbar("x", {"alvo": str(alvo)}) != antes


def test_carimbo_nao_explode_com_param_estranho(tmp_path):
    """Param que não é caminho (número, vazio, pasta, arquivo inexistente) não
    pode derrubar o carimbo — ele é o portão, tem que ser à prova de tudo."""
    material = {"n": 7, "vazio": "", "pasta": str(tmp_path), "sumido": "/nao/existe"}
    assert carimbar("x", material) == carimbar("x", material)


def test_arquivo_grande_carimba_por_tamanho_e_mtime(tmp_path, monkeypatch):
    """Acima do teto não se lê o arquivo inteiro (a máquina é magra): tamanho +
    mtime já denunciam a troca."""
    from mister import confirmacao

    monkeypatch.setattr(confirmacao, "_TETO_HASH_BYTES", 10)
    alvo = tmp_path / "grande.bin"
    alvo.write_bytes(b"a" * 50)
    antes = carimbar("x", {"alvo": str(alvo)})
    alvo.write_bytes(b"b" * 80)  # tamanho diferente
    assert carimbar("x", {"alvo": str(alvo)}) != antes


def test_arquivo_sem_permissao_nao_derruba_o_carimbo(tmp_path):
    alvo = tmp_path / "trancado.txt"
    alvo.write_text("segredo", encoding="utf-8")
    alvo.chmod(0o000)
    try:
        assert carimbar("x", {"alvo": str(alvo)})  # não estoura
    finally:
        alvo.chmod(0o600)


def test_carimbo_e_estavel_entre_processos(tmp_path, monkeypatch):
    """Determinístico de verdade: nada de id() nem hash aleatório do Python —
    o mesmo material tem que dar o mesmo carimbo sempre."""
    monkeypatch.chdir(tmp_path)
    material = {"alvo": "x", "quantos": 3, "lista": ["a", "b"]}
    esperado = carimbar("acao", material)
    assert len(esperado) == 64 and int(esperado, 16) >= 0  # sha256 em hex
    assert carimbar("acao", dict(material)) == esperado
    assert os.getcwd() == str(tmp_path)
