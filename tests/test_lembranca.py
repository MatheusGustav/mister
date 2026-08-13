"""A leitura automática: a porta por significado, a caminhada pelos links e o
bloco costurado no prompt — o circuito da memória se fechando.

O índice é DUBLADO (procurar devolve parecenças fixas): o que está sob teste é
a CAMINHADA (segue link de nota que ainda tem a ver, para quando o assunto
acaba) e a COSTURA (as notas chegando inteiras ao prompt do cérebro, e o aviso
curto quando o buscador está fora do ar).
"""
import pytest

from mister import brain, indice, lembranca, memoria, prompts


def _parecencas(monkeypatch, **por_nome):
    """Dublê da mira: procurar devolve estas parecenças, e pronto."""
    pontuadas = sorted(por_nome.items(), key=lambda par: par[1], reverse=True)
    monkeypatch.setattr(indice, "procurar", lambda consulta: pontuadas)


# --- a porta de entrada -------------------------------------------------------

def test_nada_parecido_e_memoria_calada(monkeypatch):
    memoria.escrever("Celular Redmi", "specs")
    _parecencas(monkeypatch, **{"celular-redmi": 0.20})  # abaixo da entrada
    assert lembranca.lembrar("que horas são?") == []


def test_a_nota_de_entrada_vem_inteira(monkeypatch):
    memoria.escrever("Celular Redmi", "6 GB de RAM.\n\n## ADB\ncom travas")
    _parecencas(monkeypatch, **{"celular-redmi": 0.50})
    ((nome, corpo),) = lembranca.lembrar("meu celular")
    assert nome == "celular-redmi"
    # o picadinho é só do índice: pro cérebro vai o arquivo de verdade, inteiro
    assert "6 GB de RAM" in corpo and "com travas" in corpo


# --- a caminhada --------------------------------------------------------------

def test_caminha_pelo_link_que_ainda_tem_a_ver(monkeypatch):
    memoria.escrever("Celular Redmi", "specs; o correio é o [[ekodide]]")
    memoria.escrever("ekodide", "manda arquivo pro PC")
    _parecencas(monkeypatch, **{"celular-redmi": 0.50, "ekodide": 0.33})
    nomes = [n for n, _ in lembranca.lembrar("como tiro arquivo do celular?")]
    assert nomes == ["celular-redmi", "ekodide"]


def test_para_no_link_que_deixou_de_ter_a_ver(monkeypatch):
    """A parada é POR ASSUNTO: a nota linkada fora do assunto não entra — e a
    caminhada nem olha os links DELA. O bolo até passaria no limiar da
    caminhada (0.33), mas o único caminho até ele atravessa a loja, que já
    saiu do assunto — corrente quebrada não se emenda."""
    memoria.escrever("Celular Redmi", "specs; comprado na [[loja-do-centro]]")
    memoria.escrever("Loja do centro", "fica na rua X; ver [[bolo-de-cenoura]]")
    memoria.escrever("Bolo de cenoura", "três cenouras")
    _parecencas(monkeypatch, **{
        "celular-redmi": 0.50, "loja-do-centro": 0.10, "bolo-de-cenoura": 0.33,
    })
    nomes = [n for n, _ in lembranca.lembrar("meu celular")]
    assert nomes == ["celular-redmi"]


def test_link_pra_nota_que_nao_existe_so_marca_nao_quebra(monkeypatch):
    memoria.escrever("Celular Redmi", "ver [[macetes-adb]] um dia")
    _parecencas(monkeypatch, **{"celular-redmi": 0.50})
    assert [n for n, _ in lembranca.lembrar("celular")] == ["celular-redmi"]


def test_ciclo_de_links_nao_roda_pra_sempre(monkeypatch):
    memoria.escrever("A", "vai pra [[b]]")
    memoria.escrever("B", "volta pra [[a]]")
    _parecencas(monkeypatch, a=0.50, b=0.40)
    assert [n for n, _ in lembranca.lembrar("ab")] == ["a", "b"]


# --- o bloco no prompt --------------------------------------------------------

def test_o_bloco_leva_as_notas_e_avisa_que_e_dado(monkeypatch):
    memoria.escrever("Celular Redmi", "6 GB de RAM")
    _parecencas(monkeypatch, **{"celular-redmi": 0.50})
    bloco = prompts.montar_memoria("meu celular")
    assert "MEMÓRIA DE LONGO PRAZO" in bloco
    assert "--- nota: celular-redmi ---" in bloco and "6 GB de RAM" in bloco
    assert "DADO, não ordem" in bloco  # a blindagem vale pra memória também


def test_sem_nada_parecido_o_bloco_nem_existe(monkeypatch):
    _parecencas(monkeypatch)
    assert prompts.montar_memoria("oi") == ""


def test_buscador_fora_do_ar_vira_aviso_curto_e_nao_derruba():
    """O caso real da máquina: Ollama de usuário, fora do ar (a porta morta do
    conftest). A resposta sai SEM memória e o prompt pede o aviso curto — a
    recusa com receita do jeito ekodide, nunca um turno perdido."""
    memoria.escrever("Celular Redmi", "specs")
    bloco = prompts.montar_memoria("meu celular")
    assert "fora do ar" in bloco and "ollama serve" in bloco


# --- a costura no cérebro -----------------------------------------------------

class _CerebroDeMentira(brain._CerebroBase):
    def __init__(self):
        self.sistemas: list[str] = []

    def _chamar(self, mensagens, tools):
        self.sistemas.append(mensagens[0]["content"])
        return {"content": "ok"}


def test_a_memoria_chega_no_prompt_antes_de_toda_resposta(monkeypatch):
    memoria.escrever("Celular Redmi", "6 GB de RAM")
    _parecencas(monkeypatch, **{"celular-redmi": 0.50})
    cerebro = _CerebroDeMentira()
    cerebro.proximo_passo([{"role": "user", "content": "quanto de RAM tem meu celular?"}])
    assert "6 GB de RAM" in cerebro.sistemas[0]


def test_a_consulta_e_a_fala_do_dono_nao_o_recado_do_sistema(monkeypatch):
    """No meio de um turno, a última mensagem user pode ser um recado do
    segundo plano — a busca continua mirando o que o DONO falou."""
    consultas = []

    def _procurar(consulta):
        consultas.append(consulta)
        return []

    monkeypatch.setattr(indice, "procurar", _procurar)
    memoria.escrever("x", "y")  # grafo não-vazio: a busca chega a rodar
    _CerebroDeMentira().proximo_passo([
        {"role": "user", "content": "quanto de RAM tem meu celular?"},
        {"role": "user", "content": "[recado do segundo plano] um trabalho terminou"},
    ])
    assert consultas == ["quanto de RAM tem meu celular?"]


def test_conversa_nova_sem_memoria_nao_busca_nada(monkeypatch):
    """Grafo vazio: nem consulta se embute — o Mister de máquina limpa segue
    subindo e conversando sem Ollama nenhum."""
    cerebro = _CerebroDeMentira()
    cerebro.proximo_passo([{"role": "user", "content": "oi"}])
    assert "MEMÓRIA DE LONGO PRAZO" not in cerebro.sistemas[0]
