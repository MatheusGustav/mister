package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestPasteReader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"texto digitado passa igual", "oi\rtudo bem\r", "oi\rtudo bem\r"},
		{"quebra colada vira marca", "\x1b[200~um\rdois\ntrês\x1b[201~\r", "\x1b[200~um↵dois↵três\x1b[201~\r"},
		{"CRLF colado é uma quebra só", "\x1b[200~um\r\ndois\r\n\x1b[201~", "\x1b[200~um↵dois↵\x1b[201~"},
		{"linha em branco colada fica", "\x1b[200~um\n\ndois\x1b[201~", "\x1b[200~um↵↵dois\x1b[201~"},
		{"depois da colagem o Enter volta a valer", "\x1b[200~um\x1b[201~\rdois\r", "\x1b[200~um\x1b[201~\rdois\r"},
		{"seta não é confundida com a marca", "a\x1b[Db\x1b[2~c", "a\x1b[Db\x1b[2~c"},
		{"ESC sozinho no fim sai junto do que vier", "a\x1bb", "a\x1bb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for name, in := range map[string]io.Reader{
				"de uma vez":    strings.NewReader(tt.input),
				"byte por byte": iotest.OneByteReader(strings.NewReader(tt.input)),
			} {
				got, err := io.ReadAll(&pasteReader{in: in})
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if string(got) != tt.want {
					t.Errorf("%s: saiu %q, quero %q", name, got, tt.want)
				}
			}
		})
	}
}

// A paste larger than the terminal's own line limit arrives whole.
func TestPasteReader_LongPaste(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("palavra ", 2000)
	got, err := io.ReadAll(&pasteReader{in: strings.NewReader("\x1b[200~" + body + "\x1b[201~")})
	if err != nil {
		t.Fatal(err)
	}
	if want := "\x1b[200~" + body + "\x1b[201~"; string(got) != want {
		t.Errorf("a colagem longa mudou: %d bytes, quero %d", len(got), len(want))
	}
}

func TestPipeInput(t *testing.T) {
	t.Parallel()
	var errOut bytes.Buffer
	in := newInput(strings.NewReader("primeira\nsegunda"), &errOut, &printer{out: io.Discard, err: &errOut})
	defer in.Close()
	for _, want := range []string{"primeira\n", "segunda"} {
		got, err := in.ReadMessage()
		if err != nil || got != want {
			t.Errorf("ReadMessage = %q, %v; quero %q", got, err, want)
		}
	}
	if _, err := in.ReadMessage(); err != io.EOF {
		t.Errorf("no fim da entrada quero io.EOF, veio %v", err)
	}
	if got := errOut.String(); got != "› › › " {
		t.Errorf("um prompt por leitura: %q", got)
	}
}
