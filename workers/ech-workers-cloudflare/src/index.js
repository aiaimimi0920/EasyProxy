import { connect } from "cloudflare:sockets";

const encoder = new TextEncoder();

export default {
  async fetch(request, env) {
    try {
      const expectedToken = String(env.ECH_TOKEN || "").trim();
      if (!expectedToken) {
        return new Response("ECH_TOKEN is not configured", { status: 503 });
      }

      const upgradeHeader = request.headers.get("Upgrade");
      if (!upgradeHeader || upgradeHeader.trim().toLowerCase() !== "websocket") {
        return new URL(request.url).pathname === "/"
          ? new Response("WebSocket Proxy Server", { status: 200 })
          : new Response("Expected WebSocket", { status: 426 });
      }

      if (expectedToken && request.headers.get("Sec-WebSocket-Protocol") !== expectedToken) {
        return new Response("Unauthorized", { status: 401 });
      }

      const [client, server] = Object.values(new WebSocketPair());
      server.accept();
      handleSession(server).catch(() => safeCloseWebSocket(server));

      const responseInit = {
        status: 101,
        webSocket: client
      };
      if (expectedToken) {
        responseInit.headers = { "Sec-WebSocket-Protocol": expectedToken };
      }
      return new Response(null, responseInit);
    } catch (err) {
      return new Response(String(err), { status: 500 });
    }
  }
};

async function handleSession(webSocket) {
  let remoteSocket;
  let remoteWriter;
  let remoteReader;
  let isClosed = false;
  let isConnecting = false;
  let connectionAttempts = 0;

  const cleanup = () => {
    if (isClosed) {
      return;
    }
    isClosed = true;
    isConnecting = false;
    try {
      remoteWriter?.releaseLock();
    } catch {}
    try {
      remoteReader?.releaseLock();
    } catch {}
    try {
      remoteSocket?.close();
    } catch {}
    remoteWriter = null;
    remoteReader = null;
    remoteSocket = null;
    safeCloseWebSocket(webSocket);
  };

  const pumpRemoteToWebSocket = async () => {
    try {
      while (!isClosed && remoteReader) {
        const { done, value } = await remoteReader.read();
        if (done || webSocket.readyState !== 1) {
          break;
        }
        if (value?.byteLength > 0) {
          webSocket.send(value);
        }
      }
    } catch (err) {
      console.error("Remote to WebSocket pump error:", err);
      if (!isClosed && connectionAttempts < 3) {
        connectionAttempts += 1;
        try {
          await new Promise((resolve) => setTimeout(resolve, 1000 * connectionAttempts));
          if (remoteSocket?.readable) {
            remoteReader = remoteSocket.readable.getReader();
            pumpRemoteToWebSocket();
            return;
          }
        } catch {}
      }
    }

    if (!isClosed) {
      try {
        webSocket.send("CLOSE");
      } catch {}
      cleanup();
    }
  };

  const parseAddress = (addr) => {
    let host;
    let port;
    if (addr.startsWith("[") && addr.includes("]")) {
      const closeBracketIndex = addr.indexOf("]");
      host = addr.substring(0, closeBracketIndex + 1);
      const afterBracket = addr.substring(closeBracketIndex + 1);
      port = afterBracket.startsWith(":") ? afterBracket.substring(1) : "443";
    } else {
      [host, port = "443"] = addr.split(/[:,;]/);
    }

    const parsedPort = Number.parseInt(port, 10);
    return {
      host,
      port: Number.isNaN(parsedPort) ? 443 : parsedPort
    };
  };

  const base64ToBytes = (input) => {
    if (!input) {
      return new Uint8Array();
    }
    const binary = atob(input);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i += 1) {
      bytes[i] = binary.charCodeAt(i);
    }
    return bytes;
  };

  const isCFError = (err) => {
    const msg = String(err?.message || "").toLowerCase();
    return msg.includes("proxy request") || msg.includes("cannot connect") || msg.includes("cloudflare");
  };

  const connectToRemote = async (targetAddr, firstFrameData, proxyIP) => {
    if (isConnecting || remoteSocket) {
      throw new Error("connection already exists or is connecting");
    }

    isConnecting = true;
    connectionAttempts = 0;

    try {
      const original = parseAddress(targetAddr);
      const fallbackIPs = [];
      if (proxyIP) {
        fallbackIPs.push(proxyIP);
      }
      const attempts = [null, ...fallbackIPs];

      for (let i = 0; i < attempts.length; i += 1) {
        let attemptHost = original.host;
        let attemptPort = original.port;

        if (attempts[i] !== null) {
          const parsed = parseAddress(attempts[i]);
          attemptHost = parsed.host;
          attemptPort = parsed.port;
        }

        try {
          remoteSocket = connect({
            hostname: attemptHost,
            port: attemptPort
          });
          if (remoteSocket.opened) {
            await remoteSocket.opened;
          }

          remoteWriter = remoteSocket.writable.getWriter();
          remoteReader = remoteSocket.readable.getReader();

          if (firstFrameData?.byteLength > 0) {
            await remoteWriter.write(firstFrameData);
          }

          isConnecting = false;
          webSocket.send("CONNECTED");
          pumpRemoteToWebSocket();
          return;
        } catch (err) {
          try {
            remoteWriter?.releaseLock();
          } catch {}
          try {
            remoteReader?.releaseLock();
          } catch {}
          try {
            remoteSocket?.close();
          } catch {}
          remoteWriter = null;
          remoteReader = null;
          remoteSocket = null;

          if (!isCFError(err) || i === attempts.length - 1) {
            throw err;
          }
        }
      }
    } catch (err) {
      isConnecting = false;
      throw err;
    }
  };

  webSocket.addEventListener("message", async (event) => {
    if (isClosed) {
      return;
    }

    try {
      const data = event.data;
      if (typeof data === "string") {
          if (data.startsWith("CONNECT:")) {
            const parts = data.split("|");
            const targetAddr = parts[0].substring(8);
            // Decode base64-encoded first frame data
            const rawFirstFrame = parts[1] || "";
            const firstFrameData = rawFirstFrame ? base64ToBytes(rawFirstFrame) : new Uint8Array();
            const proxyIP = parts[2] || "";
            if (!targetAddr) {
              throw new Error("invalid target address");
          }
          await connectToRemote(targetAddr, firstFrameData, proxyIP);
        } else if (data.startsWith("DATA:")) {
          if (remoteWriter) {
            await remoteWriter.write(encoder.encode(data.substring(5)));
          }
        } else if (data === "CLOSE") {
          cleanup();
        }
      } else if (data instanceof ArrayBuffer && remoteWriter) {
        await remoteWriter.write(new Uint8Array(data));
      }
    } catch (err) {
      try {
        webSocket.send("ERROR:" + err.message);
      } catch {}
      cleanup();
    }
  });

  webSocket.addEventListener("close", cleanup);
  webSocket.addEventListener("error", cleanup);
}

function safeCloseWebSocket(ws) {
  try {
    if (ws.readyState === 1 || ws.readyState === 2) {
      ws.close(1000, "Server closed");
    }
  } catch {}
}
