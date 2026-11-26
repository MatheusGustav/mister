package embed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Every Mister open on the same home shares one llama-server. They agree
// through three files in the embedding folder:
//
//   - stateFile says which process is the server and on which port;
//   - useLock is held, shared, by each Mister while it uses the server;
//   - startLock is held, alone, by the Mister that is starting or stopping it.
//
// A Mister that stops using the server (idle clock or Close) tries to take
// useLock alone: it only gets it when nobody else uses the server, and then
// stops it. The kernel drops the locks of a Mister that dies, so a killed
// Mister never keeps the others waiting; the server it leaves behind is found
// and stopped by the next Mister.
const (
	stateFile = "servidor.json"
	useLock   = "servidor.uso.lock"
	startLock = "servidor.partida.lock"
	logFile   = "servidor.log"
)

// stopWait is how long the Mister that stops using the server waits for
// another one that is starting or stopping it.
const stopWait = 10 * time.Second

type serverState struct {
	PID  int `json:"pid"`
	Port int `json:"porta"`
}

func (s serverState) url() string { return "http://127.0.0.1:" + strconv.Itoa(s.Port) }

// attach makes this Mister a user of the server, starting one when none
// answers. Called with l.mu held.
func (l *Local) attach(ctx context.Context) error {
	gate, err := lockFile(ctx, filepath.Join(l.Dir, startLock), syscall.LOCK_EX)
	if err != nil {
		return fmt.Errorf("esperando a vez de subir o servidor de embedding: %w", err)
	}
	defer gate.Close()
	use, err := lockFile(ctx, filepath.Join(l.Dir, useLock), syscall.LOCK_SH)
	if err != nil {
		return fmt.Errorf("registrando o uso do servidor de embedding: %w", err)
	}

	state, known := l.readState()
	if known && alive(state.PID) && healthy(ctx, state.url()) {
		l.use, l.server, l.child = use, state, nil
		return nil
	}
	if known {
		// Left by a Mister that died, and no longer answering.
		stop(state, nil)
	}
	state, child, err := l.start(ctx)
	if err != nil {
		use.Close()
		return err
	}
	if err := l.writeState(state); err != nil {
		stop(state, child.done)
		use.Close()
		return err
	}
	l.use, l.server, l.child = use, state, child
	return nil
}

// detach stops using the server and brings it down when no other Mister uses
// it. Called with l.mu held.
func (l *Local) detach() {
	if l.use == nil {
		return
	}
	state, child := l.server, l.child
	ctx, cancel := context.WithTimeout(context.Background(), stopWait)
	defer cancel()
	gate, err := lockFile(ctx, filepath.Join(l.Dir, startLock), syscall.LOCK_EX)
	l.letGo()
	if err != nil {
		// Whoever holds the gate for this long decides about the server.
		return
	}
	defer gate.Close()

	path := filepath.Join(l.Dir, useLock)
	alone, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	defer alone.Close()
	if !lockAlone(alone) {
		return
	}
	var done chan struct{}
	if child != nil {
		done = child.done
	}
	stop(state, done)
	_ = os.Remove(filepath.Join(l.Dir, stateFile))
}

// letGo drops this Mister's use of the server without deciding its fate.
func (l *Local) letGo() {
	if l.use != nil {
		l.use.Close()
	}
	l.use, l.server, l.child = nil, serverState{}, nil
}

func (l *Local) readState() (serverState, bool) {
	var state serverState
	data, err := os.ReadFile(filepath.Join(l.Dir, stateFile))
	if err != nil || json.Unmarshal(data, &state) != nil || state.PID <= 0 || state.Port <= 0 {
		return serverState{}, false
	}
	return state, true
}

func (l *Local) writeState(state serverState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("gravando o estado do servidor de embedding: %w", err)
	}
	path := filepath.Join(l.Dir, stateFile)
	if err := os.WriteFile(path+".tmp", data, 0o644); err != nil {
		return fmt.Errorf("gravando o estado do servidor de embedding: %w", err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return fmt.Errorf("gravando o estado do servidor de embedding: %w", err)
	}
	return nil
}

// lockFile opens a lock file and takes the lock, waiting for whoever holds it
// until the context ends. Closing the file drops the lock.
func lockFile(ctx context.Context, path string, how int) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// lockAlone reports whether nobody else uses the server. A command this
// Mister is launching at the same instant holds a copy of every open file for
// a moment, the lock just dropped included, so one refusal is not an answer:
// a real user is still there after a few tries.
func lockAlone(f *os.File) bool {
	for range 10 {
		if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func alive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

func healthy(ctx context.Context, url string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// isServer confirms that the process is the server the state describes, and
// not another program that got the same number after the server died.
func isServer(state serverState) bool {
	cmdline, err := os.ReadFile("/proc/" + strconv.Itoa(state.PID) + "/cmdline")
	if err != nil {
		return false
	}
	return strings.Contains(string(cmdline), "--port\x00"+strconv.Itoa(state.Port)+"\x00")
}

// stop ends the server and waits for it to go. done, when not nil, closes
// when the process exits (the server is a child of this Mister).
func stop(state serverState, done <-chan struct{}) {
	gone := func() bool {
		if done != nil {
			select {
			case <-done:
				return true
			default:
				return false
			}
		}
		return !alive(state.PID)
	}
	if gone() || !isServer(state) {
		return
	}
	// A process that ignores the kill is left behind: waiting forever would
	// hold every other Mister at the gate.
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		_ = syscall.Kill(state.PID, sig)
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			if gone() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}
