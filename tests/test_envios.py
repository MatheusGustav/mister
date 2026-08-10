"""O registro dos envios em voo: dispara, não bloqueia, e nunca some calado."""
import threading

from mister import envios


def _limpar():
    envios.aguardar(timeout=2)
    envios.colher_prontos()


def test_dispara_e_colhe_a_fala_de_conclusao():
    _limpar()
    envios.disparar("'a.pdf' → celular", lambda: "Mandei 'a.pdf' pro celular.")
    envios.aguardar(timeout=2)
    assert envios.colher_prontos() == ["Mandei 'a.pdf' pro celular."]


def test_colher_duas_vezes_nao_repete():
    _limpar()
    envios.disparar("x", lambda: "pronto")
    envios.aguardar(timeout=2)
    assert envios.colher_prontos() == ["pronto"]
    assert envios.colher_prontos() == []


def test_em_voo_mostra_quem_ainda_nao_terminou():
    _limpar()
    solta = threading.Event()
    envios.disparar("'grande.iso' → celular", lambda: (solta.wait(2), "foi")[1])
    assert envios.em_voo() == ["'grande.iso' → celular"]
    assert envios.colher_prontos() == []   # não colhe quem ainda voa
    solta.set()
    envios.aguardar(timeout=2)
    assert envios.em_voo() == []
    assert envios.colher_prontos() == ["foi"]


def test_colher_nunca_bloqueia():
    """A regra do laço: colher é válvula, nunca barreira. Um envio preso não
    pode segurar o turno."""
    _limpar()
    solta = threading.Event()
    envios.disparar("preso", lambda: (solta.wait(5), "enfim")[1])
    assert envios.colher_prontos() == []   # devolveu na hora, sem esperar
    solta.set()
    envios.aguardar(timeout=6)
    _limpar()


def test_quebra_na_thread_vira_fala_em_vez_de_sumir():
    _limpar()

    def _quebra():
        raise RuntimeError("a rede caiu no meio")

    envios.disparar("'a.pdf' → celular", _quebra)
    envios.aguardar(timeout=2)
    (fala,) = envios.colher_prontos()
    assert "quebrou no meio" in fala and "a rede caiu no meio" in fala


def test_varios_envios_convivem():
    _limpar()
    for i in range(5):
        envios.disparar(f"envio {i}", lambda i=i: f"terminou {i}")
    envios.aguardar(timeout=3)
    assert sorted(envios.colher_prontos()) == [f"terminou {i}" for i in range(5)]
