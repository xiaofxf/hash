package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const (
	testToken   = "test-token"
	testTimeout = 10 * time.Second
)

func newTestServer(t *testing.T, command ...string) (*httptest.Server, *Server) {
	t.Helper()

	server, err := New(Config{Token: testToken, Command: command})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer, server
}

func terminalURL(httpServer *httptest.Server) string {
	return "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws?token=" + testToken
}

func TestIndexRequiresToken(t *testing.T) {
	httpServer, _ := newTestServer(t, "/bin/sh")

	response, err := http.Get(httpServer.URL + "/")
	if err != nil {
		t.Fatalf("GET / error: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET / status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}

	response, err = http.Get(httpServer.URL + "/?token=wrong")
	if err != nil {
		t.Fatalf("GET / with wrong token error: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET / with wrong token status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
}

func TestIndexServesTerminalPage(t *testing.T) {
	httpServer, _ := newTestServer(t, "/bin/sh")

	response, err := http.Get(httpServer.URL + "/?token=" + testToken)
	if err != nil {
		t.Fatalf("GET / error: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", contentType)
	}
	if contentType := response.Header.Get("Content-Security-Policy"); contentType == "" {
		t.Error("Content-Security-Policy header is missing")
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	page := string(body)
	for _, want := range []string{"/assets/vendor/xterm.js", "/assets/app.js", "id=\"terminal\""} {
		if !strings.Contains(page, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
}

func TestAssetsAreServed(t *testing.T) {
	httpServer, _ := newTestServer(t, "/bin/sh")

	for _, path := range []string{"/assets/app.js", "/assets/vendor/xterm.js", "/assets/vendor/xterm.css"} {
		response, err := http.Get(httpServer.URL + path)
		if err != nil {
			t.Fatalf("GET %s error: %v", path, err)
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want %d", path, response.StatusCode, http.StatusOK)
		}
		if len(body) == 0 {
			t.Errorf("GET %s returned an empty body", path)
		}
	}

	response, err := http.Get(httpServer.URL + "/assets/")
	if err != nil {
		t.Fatalf("GET /assets/ error: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("GET /assets/ status = %d, want %d", response.StatusCode, http.StatusNotFound)
	}
}

func TestWebSocketRequiresToken(t *testing.T) {
	httpServer, _ := newTestServer(t, "/bin/sh")
	url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws?token=wrong"

	conn, response, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		_ = conn.Close()
		t.Fatal("dial with a wrong token succeeded")
	}
	if response == nil {
		t.Fatalf("dial returned no HTTP response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
}

func TestWebSocketRejectsCrossOrigin(t *testing.T) {
	httpServer, _ := newTestServer(t, "/bin/sh")
	header := http.Header{}
	header.Set("Origin", "https://example.com")

	conn, response, err := websocket.DefaultDialer.Dial(terminalURL(httpServer), header)
	if err == nil {
		_ = conn.Close()
		t.Fatal("cross-origin dial succeeded")
	}
	if response == nil {
		t.Fatalf("dial returned no HTTP response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
}

func TestWebSocketAcceptsSameOrigin(t *testing.T) {
	httpServer, _ := newTestServer(t, "/bin/sh", "-c", "printf 'ready\\n'; exec cat")
	header := http.Header{}
	header.Set("Origin", httpServer.URL)

	conn, response, err := websocket.DefaultDialer.Dial(terminalURL(httpServer), header)
	if err != nil {
		t.Fatalf("same-origin dial error: %v", err)
	}
	if response != nil {
		_ = response.Body.Close()
	}
	defer func() { _ = conn.Close() }()

	readOutputUntil(t, conn, "ready")
}

func TestSessionStreamsInputAndOutput(t *testing.T) {
	httpServer, _ := newTestServer(t, "/bin/sh", "-c", "printf 'ready\\n'; exec cat")

	conn, response, err := websocket.DefaultDialer.Dial(terminalURL(httpServer), nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	if response != nil {
		_ = response.Body.Close()
	}
	defer func() { _ = conn.Close() }()

	readOutputUntil(t, conn, "ready")

	if err := conn.WriteJSON(clientMessage{Type: "input", Data: "echo hello\n"}); err != nil {
		t.Fatalf("write input: %v", err)
	}
	readOutputUntil(t, conn, "hello")

	if err := conn.WriteJSON(clientMessage{Type: "resize", Cols: 120, Rows: 40}); err != nil {
		t.Fatalf("write resize: %v", err)
	}
	if err := conn.WriteJSON(clientMessage{Type: "input", Data: "still here\n"}); err != nil {
		t.Fatalf("write input after resize: %v", err)
	}
	readOutputUntil(t, conn, "still here")
}

func TestSessionReportsExitCode(t *testing.T) {
	httpServer, _ := newTestServer(t, "/bin/sh", "-c", "printf 'bye\\n'; exit 7")

	conn, response, err := websocket.DefaultDialer.Dial(terminalURL(httpServer), nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	if response != nil {
		_ = response.Body.Close()
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetReadDeadline(time.Now().Add(testTimeout))
	sawOutput := false
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read error before exit message: %v", err)
		}
		if messageType == websocket.BinaryMessage {
			if bytes.Contains(data, []byte("bye")) {
				sawOutput = true
			}
			continue
		}

		var message serverMessage
		if err := json.Unmarshal(data, &message); err != nil {
			t.Fatalf("decode control frame %q: %v", data, err)
		}
		if message.Type != "exit" {
			continue
		}
		if message.Code == nil {
			t.Fatal("exit message has no code")
		}
		if *message.Code != 7 {
			t.Errorf("exit code = %d, want 7", *message.Code)
		}
		if !sawOutput {
			t.Error("no terminal output arrived before the exit message")
		}
		return
	}
}

func TestSessionReportsStartError(t *testing.T) {
	httpServer, _ := newTestServer(t, "/definitely/not/a/shell")

	conn, response, err := websocket.DefaultDialer.Dial(terminalURL(httpServer), nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	if response != nil {
		_ = response.Body.Close()
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetReadDeadline(time.Now().Add(testTimeout))
	messageType, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if messageType != websocket.TextMessage {
		t.Fatalf("first message type = %d, want text", messageType)
	}

	var message serverMessage
	if err := json.Unmarshal(data, &message); err != nil {
		t.Fatalf("decode control frame %q: %v", data, err)
	}
	if message.Type != "error" || message.Message == "" {
		t.Errorf("got %+v, want an error message", message)
	}
}

func TestCloseTerminatesSessions(t *testing.T) {
	httpServer, server := newTestServer(t, "/bin/sh", "-c", "printf 'ready\\n'; exec cat")

	conn, response, err := websocket.DefaultDialer.Dial(terminalURL(httpServer), nil)
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	if response != nil {
		_ = response.Body.Close()
	}
	defer func() { _ = conn.Close() }()

	readOutputUntil(t, conn, "ready")
	server.Close()

	_ = conn.SetReadDeadline(time.Now().Add(testTimeout))
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			// The socket may close before the exit frame is readable; either
			// outcome means the session was terminated.
			return
		}
		if messageType == websocket.TextMessage {
			var message serverMessage
			if json.Unmarshal(data, &message) == nil && message.Type == "exit" {
				return
			}
		}
	}
}

// readOutputUntil reads binary output frames until want appears or the test
// times out.
func readOutputUntil(t *testing.T, conn *websocket.Conn, want string) {
	t.Helper()

	_ = conn.SetReadDeadline(time.Now().Add(testTimeout))
	var output bytes.Buffer
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read error before %q: %v (output %q)", want, err, output.String())
		}
		if messageType != websocket.BinaryMessage {
			continue
		}
		output.Write(data)
		if strings.Contains(output.String(), want) {
			return
		}
	}
}
