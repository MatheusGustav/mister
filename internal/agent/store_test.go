package agent

import (
	"os"
	"testing"
	"time"

	"github.com/MatheusGustav/mister/internal/provider"
)

func TestStore_HistoryRoundTrip(t *testing.T) {
	t.Parallel()
	store, err := OpenStore(t.TempDir(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if history, err := store.Load("host"); err != nil || history != nil {
		t.Fatalf("agente sem histórico: %v, %v", history, err)
	}
	first := provider.UserText("um")
	second := use(toolUse{id: "t", name: "eco", input: map[string]any{"a": 1}}).Message
	for _, msg := range []provider.Message{first, second} {
		if err := store.Append("host", msg); err != nil {
			t.Fatal(err)
		}
	}
	history, err := store.Load("host")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Text() != "um" || history[1].Blocks[0].ToolUseID != "t" || string(history[1].Blocks[0].Input) != `{"a":1}` {
		t.Errorf("histórico relido diferente do gravado: %+v", history)
	}

	if err := store.Reset("host", []provider.Message{provider.UserText("resumo")}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append("host", provider.UserText("depois")); err != nil {
		t.Fatal(err)
	}
	history, err = store.Load("host")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Text() != "resumo" || history[1].Text() != "depois" {
		t.Errorf("depois da compactação quero [resumo, depois], veio %+v", history)
	}
}

func TestStore_Load_IgnoresLineCutByCrash(t *testing.T) {
	t.Parallel()
	store, err := OpenStore(t.TempDir(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append("host", provider.UserText("inteira")); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(store.historyPath("host"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"t":"msg","msg":{"role":"user","blo`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	history, err := store.Load("host")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Text() != "inteira" {
		t.Errorf("a linha cortada deveria ser ignorada: %+v", history)
	}
}

func TestListSessions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if sessions, err := ListSessions(dir + "/nao-existe"); err != nil || sessions != nil {
		t.Fatalf("pasta que não existe: %v, %v", sessions, err)
	}
	for _, id := range []string{"antiga", "nova"} {
		store, err := OpenStore(dir, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveMeta(SessionMeta{ID: id, Project: "/p", Created: time.Now()}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := os.Mkdir(dir+"/quebrada", 0o755); err != nil {
		t.Fatal(err)
	}
	sessions, err := ListSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[0].ID != "nova" || sessions[1].ID != "antiga" {
		t.Errorf("quero [nova, antiga], veio %+v", sessions)
	}
}
