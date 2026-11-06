package web

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

const (
	outputBufferSize   = 32 * 1024
	outputWriteTimeout = 10 * time.Second
	exitWriteTimeout   = 2 * time.Second
	terminateGrace     = 2 * time.Second
	terminateWait      = time.Second
	maxDimension       = 1000
)

// clientMessage is a JSON control frame from the browser. Terminal input is
// sent as {"type":"input","data":"..."}; window changes as
// {"type":"resize","cols":80,"rows":24}.
type clientMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// serverMessage is a JSON control frame to the browser. Terminal output is
// sent as raw binary frames; "exit" reports how the session ended and "error"
// reports a session that could not be started.
type serverMessage struct {
	Type    string `json:"type"`
	Code    *int   `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// session is one PTY-backed terminal attached to one WebSocket.
type session struct {
	conn *websocket.Conn
	cmd  *exec.Cmd
	ptmx *os.File

	exited   chan struct{}
	exitCode int

	closeOnce sync.Once
}

// runSession starts a Hash process in a pseudo-terminal and bridges it to the
// browser until either side goes away.
func (s *Server) runSession(conn *websocket.Conn) {
	defer func() {
		_ = conn.Close()
	}()

	cmd, ptmx, err := s.startSession()
	if err != nil {
		_ = conn.SetWriteDeadline(time.Now().Add(exitWriteTimeout))
		writeServerMessage(conn, serverMessage{Type: "error", Message: err.Error()})
		return
	}

	sess := &session{
		conn:   conn,
		cmd:    cmd,
		ptmx:   ptmx,
		exited: make(chan struct{}),
	}
	// Start the reaper first: close() waits for exited to be closed.
	go sess.wait()

	if !s.track(sess) {
		sess.close()
		return
	}
	defer s.untrack(sess)

	go sess.pumpOutput()
	sess.pumpInput()
	sess.close()
}

// startSession starts one shell process attached to a fresh pseudo-terminal.
func (s *Server) startSession() (*exec.Cmd, *os.File, error) {
	cmd := exec.Command(s.command[0], s.command[1:]...) //nolint:gosec // the operator chooses the shell
	cmd.Env = append(os.Environ(), s.env...)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: initialCols, Rows: initialRows})
	if err != nil {
		return nil, nil, fmt.Errorf("start terminal: %w", err)
	}
	return cmd, ptmx, nil
}

// wait reaps the shell process and records its exit code.
func (s *session) wait() {
	_ = s.cmd.Wait()
	if s.cmd.ProcessState != nil {
		s.exitCode = s.cmd.ProcessState.ExitCode()
	}
	close(s.exited)
}

// pumpOutput forwards PTY output to the browser. When the shell exits (or the
// browser stops reading), it reports the exit code and closes the socket so the
// input loop can finish.
func (s *session) pumpOutput() {
	defer func() {
		s.close()
		<-s.exited
		_ = s.conn.SetWriteDeadline(time.Now().Add(exitWriteTimeout))
		writeServerMessage(s.conn, serverMessage{Type: "exit", Code: &s.exitCode})
		_ = s.conn.Close()
	}()

	buffer := make([]byte, outputBufferSize)
	for {
		n, err := s.ptmx.Read(buffer)
		if n > 0 {
			_ = s.conn.SetWriteDeadline(time.Now().Add(outputWriteTimeout))
			if writeErr := s.conn.WriteMessage(websocket.BinaryMessage, buffer[:n]); writeErr != nil {
				s.close()
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// pumpInput forwards browser input and window changes to the PTY.
func (s *session) pumpInput() {
	for {
		_, frame, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		var msg clientMessage
		if err := json.Unmarshal(frame, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "input":
			if msg.Data == "" {
				continue
			}
			if _, err := s.ptmx.WriteString(msg.Data); err != nil {
				return
			}
		case "resize":
			s.resize(msg.Cols, msg.Rows)
		}
	}
}

// resize applies a browser window size to the PTY.
func (s *session) resize(cols, rows uint16) {
	if cols == 0 || rows == 0 || cols > maxDimension || rows > maxDimension {
		return
	}
	_ = pty.Setsize(s.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

// close terminates the session once. Closing the PTY hangs up the shell; the
// process group is killed if it does not exit promptly.
func (s *session) close() {
	s.closeOnce.Do(func() {
		_ = s.ptmx.Close()
		s.terminate()
	})
}

// terminate asks the shell to exit and forcefully kills its process group if
// it is still alive after a short grace period.
func (s *session) terminate() {
	if s.cmd.Process == nil {
		return
	}
	_ = s.cmd.Process.Signal(syscall.SIGHUP)

	select {
	case <-s.exited:
		return
	case <-time.After(terminateGrace):
	}

	// The shell was started with setsid, so it leads its own process group.
	if err := syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = s.cmd.Process.Kill()
	}
	select {
	case <-s.exited:
	case <-time.After(terminateWait):
	}
}

// writeServerMessage sends one JSON control frame.
func writeServerMessage(conn *websocket.Conn, msg serverMessage) {
	_ = conn.WriteJSON(msg)
}
