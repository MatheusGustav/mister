package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"
)

const promptText = "› "

// input hands the chat one message of the user at a time.
type input interface {
	// ReadMessage shows the prompt and waits for a message. At the end of the
	// input it returns io.EOF.
	ReadMessage() (string, error)
	// Close leaves the terminal as it was found.
	Close()
}

// newInput reads from the terminal key by key when both ends are a terminal.
// Anything else (a pipe, a file) is read line by line.
func newInput(in io.Reader, errOut io.Writer, out *printer) input {
	inFile, inOK := in.(*os.File)
	errFile, errOK := errOut.(*os.File)
	if inOK && errOK && term.IsTerminal(int(inFile.Fd())) && term.IsTerminal(int(errFile.Fd())) {
		return newTermInput(inFile, errFile, out)
	}
	return &pipeInput{reader: bufio.NewReader(in), out: out}
}

type pipeInput struct {
	reader *bufio.Reader
	out    *printer
}

func (p *pipeInput) ReadMessage() (string, error) {
	p.out.prompt()
	line, err := p.reader.ReadString('\n')
	if line != "" {
		return line, nil
	}
	return "", err
}

func (p *pipeInput) Close() {}

// termInput edits the message in the terminal itself. The terminal stays in
// raw mode only while a message is being typed: during a turn it is back to
// normal, so Ctrl+C still arrives as a signal.
type termInput struct {
	fd     int
	errOut *os.File
	term   *term.Terminal
	out    *printer

	mu    sync.Mutex
	saved *term.State
}

func newTermInput(in, errOut *os.File, out *printer) *termInput {
	rw := struct {
		io.Reader
		io.Writer
	}{&pasteReader{in: in}, errOut}
	return &termInput{fd: int(in.Fd()), errOut: errOut, term: term.NewTerminal(rw, promptText), out: out}
}

func (t *termInput) ReadMessage() (string, error) {
	state, err := term.MakeRaw(t.fd)
	if err != nil {
		return "", err
	}
	t.mu.Lock()
	t.saved = state
	t.mu.Unlock()
	// A terminal that does not tell its size keeps the editor's default.
	if width, height, err := term.GetSize(t.fd); err == nil && width > 0 && height > 0 {
		_ = t.term.SetSize(width, height)
	}
	t.term.SetBracketedPasteMode(true)
	// What the threads print meanwhile goes through the editor, which keeps the
	// line being typed in place.
	t.out.setLive(t.term)

	line, err := t.term.ReadLine()

	t.out.setLive(nil)
	t.Close()
	if errors.Is(err, term.ErrPasteIndicator) {
		err = nil
	}
	if err != nil {
		// Ctrl+C and Ctrl+D leave the cursor on the line of the prompt.
		fmt.Fprintln(t.errOut)
	}
	return strings.ReplaceAll(line, pasteBreak, "\n"), err
}

func (t *termInput) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.saved == nil {
		return
	}
	t.term.SetBracketedPasteMode(false)
	_ = term.Restore(t.fd, t.saved)
	t.saved = nil
}

// pasteBreak stands for a line break inside a pasted text while the message is
// being edited.
const pasteBreak = "↵"

var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

// pasteReader swaps the line breaks of a pasted text for pasteBreak, so the
// editor keeps the whole paste in one message instead of sending each line as
// soon as it arrives. The terminal marks where a paste starts and ends.
type pasteReader struct {
	in      io.Reader
	pasting bool
	// afterCR says the last byte was a pasted CR: an LF right after it is the
	// same line break.
	afterCR bool
	// held is the start of what may be a paste mark, waiting for the rest.
	held []byte
	out  []byte
	buf  [512]byte
}

func (r *pasteReader) Read(p []byte) (int, error) {
	for len(r.out) == 0 {
		n, err := r.in.Read(r.buf[:])
		r.feed(r.buf[:n])
		if err != nil {
			if len(r.out) == 0 {
				return 0, err
			}
			break
		}
	}
	n := copy(p, r.out)
	r.out = r.out[n:]
	return n, nil
}

func (r *pasteReader) feed(data []byte) {
	data = append(r.held, data...)
	r.held = nil
	for len(data) > 0 {
		switch {
		case bytes.HasPrefix(data, pasteStart), bytes.HasPrefix(data, pasteEnd):
			r.pasting = bytes.HasPrefix(data, pasteStart)
			r.afterCR = false
			r.out = append(r.out, data[:len(pasteStart)]...)
			data = data[len(pasteStart):]
		case len(data) < len(pasteStart) && (bytes.HasPrefix(pasteStart, data) || bytes.HasPrefix(pasteEnd, data)):
			r.held = append(r.held, data...)
			return
		case r.pasting && data[0] == '\n' && r.afterCR:
			r.afterCR = false
			data = data[1:]
		case r.pasting && (data[0] == '\r' || data[0] == '\n'):
			r.afterCR = data[0] == '\r'
			r.out = append(r.out, pasteBreak...)
			data = data[1:]
		default:
			r.afterCR = false
			r.out = append(r.out, data[0])
			data = data[1:]
		}
	}
}
