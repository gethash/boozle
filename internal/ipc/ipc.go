// Package ipc provides the local-socket channel between the main presentation
// window (master) and the presenter-view subprocess (slave).
//
// The protocol is newline-delimited JSON. On connect the master sends one
// Hello with everything that never changes during a run (play list, notes,
// start time), followed by the current PresenterState. After that, states
// are only sent when something visible changes; the presenter extrapolates
// clocks and auto-advance progress locally.
package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Hello carries the static presentation data, sent once per connection.
type Hello struct {
	PageList  []int          `json:"page_list"`  // 0-indexed page numbers in playback order
	Notes     map[int]string `json:"notes"`      // keyed by 1-indexed page, as in sidecars
	StartedAt time.Time      `json:"started_at"` // presentation start (for the elapsed clock)
	Loop      bool           `json:"loop"`       // --loop: navigation wraps around
}

// PresenterState is the dynamic part of the presentation.
type PresenterState struct {
	Page      int  `json:"page"`       // 0-indexed current page
	ListIndex int  `json:"list_index"` // position within Hello.PageList
	NextPage  int  `json:"next_page"`  // 0-indexed next page, or -1 on the last page
	Paused    bool `json:"paused"`

	// Auto-advance progress at send time and its rate of change, so the
	// presenter can animate the bar without a message per frame.
	Fraction       float64 `json:"fraction"`
	FractionPerSec float64 `json:"fraction_per_sec"`

	Overview PresenterOverviewState `json:"overview"`
}

// FractionAt extrapolates the auto-advance fraction to now, given the time
// the state was received.
func (s PresenterState) FractionAt(received, now time.Time) float64 {
	f := s.Fraction + now.Sub(received).Seconds()*s.FractionPerSec
	return min(1, max(0, f))
}

// PresenterOverviewState mirrors the master's overview session so the
// presenter process can render it without exposing it on the audience display.
type PresenterOverviewState struct {
	Active        bool    `json:"active"`
	Phase         int     `json:"phase"`
	Anim          float64 `json:"anim"`
	FromIndex     int     `json:"from_index"`
	SelectedIndex int     `json:"selected_index"`
	ExitToIndex   int     `json:"exit_to_index"`
}

// PresenterCommand is sent by the presenter window when it has keyboard focus.
// The master window applies the command so presentation state stays centralised.
type PresenterCommand struct {
	Name string `json:"name"`
	Arg  int    `json:"arg,omitempty"`
}

// message is the wire envelope; exactly one field is set.
type message struct {
	Hello *Hello          `json:"hello,omitempty"`
	State *PresenterState `json:"state,omitempty"`
}

// ─── Master side ─────────────────────────────────────────────────────────────

// Server is the master-side IPC endpoint. It accepts exactly one presenter
// connection on a socket inside a private (0700) temporary directory.
type Server struct {
	ln   net.Listener
	dir  string
	path string

	mu        sync.Mutex
	conn      net.Conn
	hello     Hello
	lastState *PresenterState
	commands  chan PresenterCommand
}

// Listen creates a socket in a fresh private directory and returns a ready
// Server. hello is sent to the presenter as soon as it connects.
func Listen(hello Hello) (*Server, error) {
	dir, err := os.MkdirTemp("", "boozle-")
	if err != nil {
		return nil, fmt.Errorf("ipc: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("ipc: %w", err)
	}
	path := filepath.Join(dir, "presenter.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("ipc listen %s: %w", path, err)
	}
	return &Server{ln: ln, dir: dir, path: path, hello: hello, commands: make(chan PresenterCommand, 64)}, nil
}

// Path returns the socket path to hand to the presenter process.
func (s *Server) Path() string { return s.path }

// Accept waits for the presenter to connect, sends it Hello and the latest
// state, and stops listening. Run it in a goroutine.
func (s *Server) Accept() {
	conn, err := s.ln.Accept()
	_ = s.ln.Close() // one presenter per run
	if err != nil {
		return
	}
	s.mu.Lock()
	s.conn = conn
	hello := s.hello
	last := s.lastState
	s.mu.Unlock()

	s.write(conn, message{Hello: &hello})
	if last != nil {
		s.write(conn, message{State: last})
	}
	go s.readCommands(conn)
}

// Commands returns the stream of keyboard commands sent by the presenter
// window. The channel is buffered; bursts beyond its capacity are dropped.
func (s *Server) Commands() <-chan PresenterCommand {
	return s.commands
}

// Send writes st to the presenter (or keeps it for when it connects).
func (s *Server) Send(st PresenterState) {
	s.mu.Lock()
	s.lastState = &st
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		s.write(conn, message{State: &st})
	}
}

func (s *Server) write(conn net.Conn, m message) {
	line, err := json.Marshal(m)
	if err != nil {
		return
	}
	line = append(line, '\n')
	if _, err := conn.Write(line); err != nil {
		s.dropConn(conn)
	}
}

func (s *Server) dropConn(conn net.Conn) {
	s.mu.Lock()
	if s.conn == conn {
		_ = s.conn.Close()
		s.conn = nil
	}
	s.mu.Unlock()
}

// Close shuts down the listener and connection and removes the socket
// directory.
func (s *Server) Close() error {
	err := s.ln.Close()
	s.mu.Lock()
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
	s.mu.Unlock()
	_ = os.RemoveAll(s.dir)
	if err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

func (s *Server) readCommands(conn net.Conn) {
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		var cmd PresenterCommand
		if err := json.Unmarshal(sc.Bytes(), &cmd); err != nil || cmd.Name == "" {
			continue
		}
		select {
		case s.commands <- cmd:
		default:
			// Drop stale input bursts rather than blocking the IPC reader.
		}
	}
	s.dropConn(conn)
}

// ─── Slave side ──────────────────────────────────────────────────────────────

// Receiver is the slave-side IPC endpoint. It keeps the Hello and the latest
// PresenterState received from the master.
type Receiver struct {
	mu       sync.Mutex
	hello    Hello
	hasHello bool
	state    PresenterState
	received time.Time
	seq      uint64
	conn     net.Conn
}

// Connect dials the master socket and starts a background read loop.
// onDisconnect is called when the connection is lost (master quit).
func Connect(socketPath string, onDisconnect func()) (*Receiver, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("ipc connect %s: %w", socketPath, err)
	}
	r := &Receiver{conn: conn}
	go r.readLoop(conn, onDisconnect)
	return r, nil
}

// Hello returns the static presentation data, and false until it arrived.
func (r *Receiver) Hello() (Hello, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hello, r.hasHello
}

// Latest returns the most recent state, when it was received, and a sequence
// number that increments with every message (cheap change detection).
func (r *Receiver) Latest() (PresenterState, time.Time, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state, r.received, r.seq
}

// SendCommand forwards a keyboard command from the presenter window to the
// master. Write errors are ignored: a dead master ends the read loop, which
// shuts the presenter down.
func (r *Receiver) SendCommand(cmd PresenterCommand) {
	line, err := json.Marshal(cmd)
	if err != nil {
		return
	}
	_, _ = r.conn.Write(append(line, '\n'))
}

func (r *Receiver) readLoop(conn net.Conn, onDisconnect func()) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20) // Hello carries all notes
	for sc.Scan() {
		var m message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue // skip malformed lines
		}
		r.mu.Lock()
		if m.Hello != nil {
			r.hello = *m.Hello
			r.hasHello = true
		}
		if m.State != nil {
			r.state = *m.State
			r.received = time.Now()
		}
		r.seq++
		r.mu.Unlock()
	}
	onDisconnect()
}
