// Package ipc provides the local-socket channel between the main presentation
// window (master) and the presenter-view subprocess (slave).
package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
)

// PresenterState is the snapshot the master sends to the slave each frame.
type PresenterState struct {
	Page           int     // 0-indexed current page
	ListIndex      int     // 0-indexed position within the active play list
	Total          int     // total pages in play list
	Fraction       float64 // auto-advance progress 0..1 (0 when no auto-advance)
	Paused         bool
	NextPage       int   // 0-indexed next page, or -1 if on the last page
	ElapsedSeconds int64 // wall-clock seconds since presentation start
}

// PresenterCommand is sent by the presenter window when it has keyboard focus.
// The master window applies the command so presentation state stays centralised.
type PresenterCommand struct {
	Name string `json:"name"`
	Arg  int    `json:"arg,omitempty"`
}

// SocketPath returns the per-process socket path.
func SocketPath(masterPID int) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("boozle-presenter-%d.sock", masterPID))
}

// ─── Master side ─────────────────────────────────────────────────────────────

// Server is the master-side IPC endpoint. It accepts exactly one slave
// connection at a time and forwards PresenterState to it.
type Server struct {
	ln   net.Listener
	path string

	mu       sync.Mutex
	conn     net.Conn
	commands chan PresenterCommand
}

// Listen creates the local socket and returns a ready Server.
// Any stale socket from a prior crash is removed first.
func Listen(socketPath string) (*Server, error) {
	_ = os.Remove(socketPath) // tolerate ENOENT
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("ipc listen %s: %w", socketPath, err)
	}
	return &Server{ln: ln, path: socketPath, commands: make(chan PresenterCommand, 64)}, nil
}

// AcceptLoop blocks accepting connections. Run it in a goroutine.
// Each new connection replaces the previous one (only one slave at a time).
func (s *Server) AcceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener was closed
		}
		s.mu.Lock()
		if s.conn != nil {
			_ = s.conn.Close()
		}
		s.conn = conn
		s.mu.Unlock()
		go s.readCommands(conn)
	}
}

// Commands returns the stream of keyboard commands sent by the presenter
// window. The channel is intentionally buffered; if the master falls behind,
// the newest state frame will still catch up on the next update.
func (s *Server) Commands() <-chan PresenterCommand {
	return s.commands
}

// Send encodes st as a JSON line and writes it to the connected slave.
// Silently drops the message if no slave is connected or the write fails.
func (s *Server) Send(st PresenterState) {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return
	}
	line, err := json.Marshal(st)
	if err != nil {
		return
	}
	line = append(line, '\n')
	if _, err := conn.Write(line); err != nil {
		s.mu.Lock()
		if s.conn == conn {
			_ = s.conn.Close()
			s.conn = nil
		}
		s.mu.Unlock()
	}
}

// Close shuts down the listener and removes the socket file.
func (s *Server) Close() error {
	err := s.ln.Close()
	_ = os.Remove(s.path)
	s.mu.Lock()
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
	s.mu.Unlock()
	return err
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
	s.mu.Lock()
	if s.conn == conn {
		_ = s.conn.Close()
		s.conn = nil
	}
	s.mu.Unlock()
}

// ─── Slave side ──────────────────────────────────────────────────────────────

// Receiver is the slave-side IPC endpoint. It keeps the latest PresenterState
// received from the master in a mutex-protected field.
type Receiver struct {
	mu    sync.Mutex
	state PresenterState
	conn  net.Conn
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

// Latest returns a copy of the most recently received state.
func (r *Receiver) Latest() PresenterState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// SendCommand forwards a keyboard command from the presenter window to the
// master. It returns false when the socket write fails.
func (r *Receiver) SendCommand(cmd PresenterCommand) bool {
	line, err := json.Marshal(cmd)
	if err != nil {
		return false
	}
	line = append(line, '\n')
	_, err = r.conn.Write(line)
	return err == nil
}

func (r *Receiver) readLoop(conn net.Conn, onDisconnect func()) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		var st PresenterState
		if err := json.Unmarshal(sc.Bytes(), &st); err != nil {
			continue // skip malformed lines
		}
		r.mu.Lock()
		r.state = st
		r.mu.Unlock()
	}
	onDisconnect()
}
