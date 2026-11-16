/**
 * Synk relay: splices the two WebSockets that join the same room.
 *
 * Peers are meant to run their mutually authenticated TLS 1.3 session end to
 * end through the splice, so the relay only ever forwards ciphertext. Room
 * names are 256-bit values derived from a secret both devices share. A room
 * name is a rendezvous secret, not an authentication factor: whoever learns it
 * can occupy a slot, but cannot complete the pinned TLS handshake.
 *
 * Uses the Durable Object WebSocket hibernation API so an idle pair costs
 * nothing while it stays connected.
 */
import { DurableObject } from "cloudflare:workers";

const ROOM_PATH = /^\/v1\/rooms\/([0-9a-f]{64})$/;

// Synk's own frames are at most 1 MiB plus framing and TLS record overhead.
const MAX_MESSAGE_BYTES = (1 << 20) + 64 * 1024;

export default {
  async fetch(request, env): Promise<Response> {
    const url = new URL(request.url);
    if (url.pathname === "/health") {
      return Response.json({ ok: true, name: "synk-relay" });
    }
    const match = ROOM_PATH.exec(url.pathname);
    if (!match) {
      return new Response("not found", { status: 404 });
    }
    if (request.method !== "GET" || request.headers.get("Upgrade") !== "websocket") {
      return new Response("expected websocket upgrade", { status: 426 });
    }
    return env.ROOM.getByName(match[1]).fetch(request);
  },
} satisfies ExportedHandler<Env>;

export class Room extends DurableObject<Env> {
  async fetch(_request: Request): Promise<Response> {
    if (this.ctx.getWebSockets().length >= 2) {
      return new Response("room full", { status: 409 });
    }
    const [client, server] = Object.values(new WebSocketPair());
    this.ctx.acceptWebSocket(server);
    log("join", { sockets: this.ctx.getWebSockets().length });
    return new Response(null, { status: 101, webSocket: client });
  }

  async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer) {
    if (typeof message === "string") {
      log("rejected", { reason: "text frame" });
      this.closeAll(1003, "binary frames only");
      return;
    }
    if (message.byteLength > MAX_MESSAGE_BYTES) {
      log("rejected", { reason: "oversize", size: message.byteLength });
      this.closeAll(1009, "message too large");
      return;
    }
    for (const peer of this.ctx.getWebSockets()) {
      if (peer !== ws) {
        try {
          peer.send(message);
        } catch {
          // The other side is already gone; its close event tears the room down.
        }
      }
    }
  }

  async webSocketClose(_ws: WebSocket, code: number, reason: string) {
    this.closeAll(sendableCode(code), reason);
  }

  async webSocketError(_ws: WebSocket, error: unknown) {
    log("error", { error: String(error) });
    this.closeAll(1011, "peer error");
  }

  // A splice with one end missing is useless, so either side leaving closes both.
  private closeAll(code: number, reason: string) {
    for (const ws of this.ctx.getWebSockets()) {
      try {
        ws.close(code, reason);
      } catch {
        // Already closed.
      }
    }
    log("leave", { code });
  }
}

// 1005 and 1006 are reported to handlers but may not be sent in a Close frame.
function sendableCode(code: number): number {
  if (code === 1000 || (code >= 1001 && code <= 1014 && code !== 1005 && code !== 1006) || (code >= 3000 && code <= 4999)) {
    return code;
  }
  return 1000;
}

function log(event: string, fields: Record<string, unknown>) {
  console.log(JSON.stringify({ event, ...fields }));
}
