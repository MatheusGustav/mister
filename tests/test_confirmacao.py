"""A peça da confirmação em si: o pedido de aval e a impressão digital dele.

O carimbo é o que faz "o dono aprovou ISTO" ser verificável em código. O que o
despachante faz com ele está no test_dispatcher; aqui é a peça sozinha.
"""
import os

from mister.confirmacao import (
    Pendente,
    PrecisaConfirmar,
    _comando_e_destrutivo,
    carimbar,
    pergunta_de_confirmacao,
)


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


# --- o critério de irreversibilidade ------------------------------------------
# apagar_nota e rodar_comando ainda não existem como tool (chegam nos itens 5 e
# 2) — aqui testa-se a REGRA pura, que o despachante vai consultar quando eles
# existirem.

def test_apagar_nota_sempre_pergunta():
    pergunta = pergunta_de_confirmacao("apagar_nota", {"nome": "bolo-de-cenoura"})
    assert pergunta is not None and "bolo-de-cenoura" in pergunta


def test_o_resto_das_tools_atuais_nao_pergunta():
    assert pergunta_de_confirmacao("puxar_do_celular", {"nome": "a.pdf"}) is None
    assert pergunta_de_confirmacao("guardar_regra", {"regra": "x"}) is None
    assert pergunta_de_confirmacao("enviar_para_celular", {"caminho": "x"}) is None


def test_comando_destrutivo_pergunta_e_o_resto_nao():
    for comando in ("rm -rf ~/notas", "shred arquivo", "dd if=/dev/zero of=/dev/sda",
                     "mkfs.ext4 /dev/sdb1", "truncate -s 0 log.txt",
                     "git reset --hard HEAD~3", "git clean -fd", "sudo rm -rf /tmp/x"):
        assert _comando_e_destrutivo(comando), comando

    for comando in ("ls -la", "cat notas.md", "git status", "git log", "mkdir pasta"):
        assert not _comando_e_destrutivo(comando), comando


def test_comando_destrutivo_no_meio_de_uma_cadeia_tambem_conta():
    assert _comando_e_destrutivo("cd /tmp && rm -rf lixo")
    assert _comando_e_destrutivo("ls; shred segredo.txt")


def test_redirecionar_por_cima_de_arquivo_que_existe_conta(tmp_path):
    alvo = tmp_path / "config.json"
    alvo.write_text("{}", encoding="utf-8")
    assert _comando_e_destrutivo(f"echo oi > {alvo}")


def test_redirecionar_pra_arquivo_novo_nao_conta(tmp_path):
    alvo = tmp_path / "novo.txt"
    assert not _comando_e_destrutivo(f"echo oi > {alvo}")


def test_rodar_comando_pergunta_so_quando_o_comando_e_destrutivo():
    assert pergunta_de_confirmacao("rodar_comando", {"comando": "rm -rf /"}) is not None
    assert pergunta_de_confirmacao("rodar_comando", {"comando": "ls -la"}) is None
