package main

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWebCommandServesTerminalPage(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "hash_test")

	build := exec.Command("go", "build", "-o", binPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("failed to build hash: %v\n%s", err, output)
	}

	cmd := exec.Command(binPath, "web", "--listen", "127.0.0.1:0")
	cmd.Env = append(os.Environ(), "HASH_CONFIG_DIR="+tmpDir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start hash web: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	pageURL := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "Hash web terminal: ") {
				pageURL <- strings.TrimPrefix(line, "Hash web terminal: ")
				return
			}
		}
		pageURL <- ""
	}()

	select {
	case url := <-pageURL:
		if url == "" {
			t.Fatalf("hash web exited before printing its URL: %s", stderr.String())
		}

		response, err := http.Get(url)
		if err != nil {
			t.Fatalf("GET terminal page: %v", err)
		}
		defer func() { _ = response.Body.Close() }()

		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("read terminal page: %v", err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("terminal page status = %d, want %d", response.StatusCode, http.StatusOK)
		}
		if !strings.Contains(string(body), "xterm.js") {
			t.Errorf("terminal page does not load xterm.js: %s", body)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for hash web: %s", stderr.String())
	}
}
