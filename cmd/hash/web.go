package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/tfcace/hash/internal/web"
)

const (
	defaultWebListen  = "127.0.0.1:7681"
	webShutdownGrace  = 5 * time.Second
	webReadHeaderTime = 10 * time.Second
)

// runWeb serves the xterm.js terminal page until interrupted.
func runWeb(args []string) int {
	flags := flag.NewFlagSet("hash web", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	listenAddr := flags.String("listen", defaultWebListen, "address to listen on")
	openPage := flags.Bool("open", false, "open the terminal page in the default browser")
	flags.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: hash web [OPTIONS]

Serve a browser terminal backed by a local Hash session.

Options:
`)
		flags.PrintDefaults()
		fmt.Fprint(os.Stderr, `
The page is protected by a random token printed at startup. Each browser tab
gets its own Hash session. Listen on a non-loopback address only when you
understand the exposure.

`)
	}

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "hash web: unexpected argument %q\n", flags.Arg(0))
		flags.Usage()
		return 2
	}

	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hash web: locate executable: %v\n", err)
		return 1
	}

	token, err := web.NewToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hash web: %v\n", err)
		return 1
	}

	terminal, err := web.New(web.Config{
		Token:   token,
		Command: []string{executable},
		Env: []string{
			"TERM=xterm-256color",
			"COLORTERM=truecolor",
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "hash web: %v\n", err)
		return 1
	}

	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hash web: listen on %s: %v\n", *listenAddr, err)
		return 1
	}
	defer func() {
		_ = listener.Close()
	}()

	pageURL := fmt.Sprintf("http://%s/?token=%s", listenAddress(listener.Addr()), token)
	fmt.Printf("Hash web terminal: %s\n", pageURL)
	fmt.Printf("Listening on %s — press Ctrl+C to stop\n", listener.Addr())
	warnIfExposed(listener.Addr())

	if *openPage {
		if err := openInBrowser(pageURL); err != nil {
			fmt.Fprintf(os.Stderr, "hash web: could not open browser: %v\n", err)
		}
	}

	server := &http.Server{
		Handler:           terminal.Handler(),
		ReadHeaderTimeout: webReadHeaderTime,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		terminal.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), webShutdownGrace)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		close(shutdownDone)
	}()

	serveErr := server.Serve(listener)
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "hash web: %v\n", serveErr)
		stop()
		<-shutdownDone
		return 1
	}

	<-shutdownDone
	fmt.Println("Hash web terminal stopped.")
	return 0
}

// listenAddress returns the host:port to show for a listener, preferring a
// loopback address when the listener covers every interface.
func listenAddress(addr net.Addr) string {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return addr.String()
	}
	host := tcp.IP.String()
	if tcp.IP.IsUnspecified() {
		if tcp.IP.To4() == nil {
			host = "::1"
		} else {
			host = "127.0.0.1"
		}
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", tcp.Port))
}

// warnIfExposed warns when the terminal is reachable from other machines.
func warnIfExposed(addr net.Addr) {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp.IP.IsLoopback() {
		return
	}
	fmt.Fprintln(os.Stderr, "Warning: this address is reachable from other machines. Keep the URL and token private.")
}

// openInBrowser asks the operating system to open url.
func openInBrowser(url string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	if err := command.Start(); err != nil {
		return err
	}
	go func() {
		_ = command.Wait()
	}()
	return nil
}
