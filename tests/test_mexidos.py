"""Testes dos ARQUIVOS MEXIDOS — o registro que alimenta o painel da TUI."""
from __future__ import annotations

from mister import mexidos


def test_marca_e_lista_do_mais_novo_pro_mais_antigo(tmp_path):
    mexidos.marcar(tmp_path / "a.txt")
    mexidos.marcar(tmp_path / "b.txt")
    assert mexidos.listar() == [str(tmp_path / "b.txt"), str(tmp_path / "a.txt")]


def test_nao_repete_arquivo_gravado_varias_vezes(tmp_path):
    """Gravado três vezes aparece UMA — no lugar da gravação mais recente."""
    mexidos.marcar(tmp_path / "a.txt")
    mexidos.marcar(tmp_path / "b.txt")
    mexidos.marcar(tmp_path / "a.txt")
    assert mexidos.listar() == [str(tmp_path / "a.txt"), str(tmp_path / "b.txt")]


def test_guarda_o_caminho_expandido(monkeypatch, tmp_path):
    """O '~' vira caminho de verdade na hora de marcar — o painel não pode
    mostrar dois nomes pro mesmo arquivo."""
    monkeypatch.setenv("HOME", str(tmp_path))
    mexidos.marcar("~/nota.md")
    assert mexidos.listar() == [str(tmp_path / "nota.md")]


def test_limpar_zera(tmp_path):
    mexidos.marcar(tmp_path / "a.txt")
    mexidos.limpar()
    assert mexidos.listar() == []


def test_encurtar_troca_a_pasta_do_dono_por_til(monkeypatch, tmp_path):
    monkeypatch.setenv("HOME", str(tmp_path))
    assert mexidos.encurtar(str(tmp_path / "notas" / "hoje.md")) == "~/notas/hoje.md"
    assert mexidos.encurtar(str(tmp_path)) == "~"


def test_encurtar_deixa_caminho_de_fora_inteiro(monkeypatch, tmp_path):
    """Fora da pasta do dono volta como veio — encurtar esconderia de onde é."""
    monkeypatch.setenv("HOME", str(tmp_path / "casa"))
    assert mexidos.encurtar("/etc/hosts") == "/etc/hosts"


def test_pasta_parecida_com_a_casa_nao_vira_til(monkeypatch, tmp_path):
    """'/home/matheus2' NÃO é '/home/matheus' — o corte é no separador."""
    monkeypatch.setenv("HOME", str(tmp_path / "casa"))
    vizinha = str(tmp_path / "casa2" / "x.md")
    assert mexidos.encurtar(vizinha) == vizinha


def test_encurtar_com_largura_corta_pela_frente(monkeypatch, tmp_path):
    """O fim é o nome do arquivo, que é o que o dono procura na lista — o corte
    come as pastas da esquerda, nunca o nome."""
    monkeypatch.setenv("HOME", str(tmp_path))
    longo = str(tmp_path / "Documentos" / "projetos" / "mister" / "mister" / "tui.py")
    curto = mexidos.encurtar(longo, largura=24)
    assert len(curto) <= 24
    assert curto.startswith("…/")
    assert curto.endswith("/tui.py")


def test_encurtar_sem_largura_nao_corta(monkeypatch, tmp_path):
    monkeypatch.setenv("HOME", str(tmp_path))
    longo = str(tmp_path / "a" / "b" / "c" / "d" / "arquivo-de-nome-comprido.md")
    assert mexidos.encurtar(longo) == longo.replace(str(tmp_path), "~", 1)


def test_encurtar_com_nome_maior_que_a_largura(monkeypatch, tmp_path):
    """Nem o nome do arquivo cabe: corta a cabeça dele também, mas respeita a
    largura (senão a linha quebra e é justamente isso que se quer evitar)."""
    monkeypatch.setenv("HOME", str(tmp_path))
    alvo = str(tmp_path / "a" / "nome-absurdamente-comprido-de-arquivo.md")
    curto = mexidos.encurtar(alvo, largura=12)
    assert len(curto) <= 12
    assert curto.endswith(".md")


def test_arquivo_que_sumiu_continua_na_lista(tmp_path):
    """Gravou e o arquivo sumiu logo depois: a lista é do que o Mister MEXEU,
    não do que existe agora."""
    alvo = tmp_path / "some.txt"
    alvo.write_text("x", encoding="utf-8")
    mexidos.marcar(alvo)
    alvo.unlink()
    assert mexidos.listar() == [str(alvo)]


def test_escrever_arquivo_marca_e_gravacao_que_falhou_nao(tmp_path):
    """A ponte com a tool: gravou, entra na lista; recusou (arquivo que existe
    e não foi lido), não entra."""
    from mister.tools.maquina import EscreverArquivoParams, escrever_arquivo

    novo = tmp_path / "novo.md"
    assert escrever_arquivo(EscreverArquivoParams(caminho=str(novo), conteudo="oi")).ok
    assert mexidos.listar() == [str(novo)]

    existente = tmp_path / "existente.md"
    existente.write_text("já era", encoding="utf-8")
    recusa = escrever_arquivo(
        EscreverArquivoParams(caminho=str(existente), conteudo="por cima")
    )
    assert not recusa.ok
    assert str(existente) not in mexidos.listar()
