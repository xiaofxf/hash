(() => {
  "use strict";

  const token = new URLSearchParams(window.location.search).get("token") || "";
  const socketProtocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  const socketURL = `${socketProtocol}//${window.location.host}/ws?token=${encodeURIComponent(token)}`;

  const terminalElement = document.getElementById("terminal");
  const statusElement = document.getElementById("status");
  const statusText = document.getElementById("status-text");
  const overlayElement = document.getElementById("overlay");
  const overlayTitle = document.getElementById("overlay-title");
  const overlayBody = document.getElementById("overlay-body");
  const restartButton = document.getElementById("restart");

  const terminal = new window.Terminal({
    cursorBlink: true,
    fontFamily:
      'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace',
    fontSize: window.innerWidth < 700 ? 13 : 14,
    lineHeight: 1.15,
    scrollback: 10000,
    theme: {
      background: "#0b0f0e",
      foreground: "#d7e0dd",
      cursor: "#4ee0a8",
      cursorAccent: "#0b0f0e",
      selectionBackground: "#2b4a3d",
      black: "#1a1f1d",
      red: "#ff6b6b",
      green: "#4ee0a8",
      yellow: "#f5d76e",
      blue: "#6ea8fe",
      magenta: "#c792ea",
      cyan: "#56d4dd",
      white: "#d7e0dd",
      brightBlack: "#5b6661",
      brightRed: "#ff8787",
      brightGreen: "#7bf1c0",
      brightYellow: "#ffe599",
      brightBlue: "#94bfff",
      brightMagenta: "#dbb0f5",
      brightCyan: "#7ee8ef",
      brightWhite: "#f4f8f6",
    },
  });

  const fitAddon = new window.FitAddon.FitAddon();
  terminal.loadAddon(fitAddon);
  terminal.open(terminalElement);

  let socket = null;
  let connected = false;
  let ended = false;

  function setStatus(state, text) {
    statusElement.dataset.state = state;
    statusText.textContent = text;
  }

  function send(message) {
    if (!socket || socket.readyState !== window.WebSocket.OPEN) {
      return false;
    }
    socket.send(JSON.stringify(message));
    return true;
  }

  function fit() {
    if (!terminalElement.clientWidth || !terminalElement.clientHeight) {
      return;
    }
    try {
      fitAddon.fit();
    } catch {
      // The terminal is not rendered yet; the next resize retries.
    }
  }

  function showOverlay(title, body) {
    ended = true;
    overlayTitle.textContent = title;
    overlayBody.textContent = body;
    overlayElement.hidden = false;
    restartButton.focus();
  }

  function connect() {
    setStatus("connecting", "connecting");
    socket = new window.WebSocket(socketURL);
    socket.binaryType = "arraybuffer";

    socket.addEventListener("open", () => {
      connected = true;
      setStatus("connected", "connected");
      fit();
      send({ type: "resize", cols: terminal.cols, rows: terminal.rows });
      terminal.focus();
    });

    socket.addEventListener("message", (event) => {
      if (typeof event.data === "string") {
        let message;
        try {
          message = JSON.parse(event.data);
        } catch {
          return;
        }
        if (message.type === "exit") {
          const code = typeof message.code === "number" ? message.code : 0;
          setStatus("closed", `exited ${code}`);
          showOverlay("Session ended", `Hash exited with code ${code}. Start a new session to continue.`);
        } else if (message.type === "error") {
          setStatus("error", "error");
          showOverlay("Could not start session", message.message || "The terminal could not be started.");
        }
        return;
      }
      terminal.write(new Uint8Array(event.data));
    });

    socket.addEventListener("close", () => {
      connected = false;
      if (ended) {
        return;
      }
      setStatus("disconnected", "disconnected");
      showOverlay("Connection lost", "The connection to the Hash terminal closed. Start a new session to reconnect.");
    });

    socket.addEventListener("error", () => {
      setStatus("error", "connection error");
    });
  }

  terminal.onData((data) => {
    send({ type: "input", data });
  });

  terminal.onResize(({ cols, rows }) => {
    send({ type: "resize", cols, rows });
  });

  // Hash binds many Ctrl chords that browsers also use. Keep the browser from
  // stealing them once the terminal has focus; Ctrl+C and Ctrl+V stay native so
  // copy and paste keep working.
  const shellShortcuts = new Set(["a", "d", "e", "h", "k", "l", "o", "p", "r", "u", "y", "z"]);
  terminal.attachCustomKeyEventHandler((event) => {
    if (event.type !== "keydown" || !event.ctrlKey || event.shiftKey || event.altKey || event.metaKey) {
      return true;
    }
    if (shellShortcuts.has(event.key.toLowerCase())) {
      event.preventDefault();
    }
    return true;
  });

  terminalElement.addEventListener("click", () => terminal.focus());
  restartButton.addEventListener("click", () => window.location.reload());

  const refit = () => {
    if (connected) {
      fit();
    }
  };
  window.addEventListener("resize", refit);
  if (window.visualViewport) {
    window.visualViewport.addEventListener("resize", refit);
  }
  if (window.ResizeObserver) {
    new window.ResizeObserver(refit).observe(terminalElement);
  }

  fit();
  terminal.focus();
  connect();
})();