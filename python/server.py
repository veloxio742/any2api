from __future__ import annotations

import json
import os
import secrets
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from datetime import datetime, timedelta, timezone
from urllib.parse import parse_qs, urlparse

from admin_store import AdminStore, default_admin_store_path
from gateway_types import UnifiedRequest
from providers import ProviderRegistry, ProviderRequestError, default_registry


ADMIN_SESSION_COOKIE = "newplatform2api_admin_session"
ADMIN_AUTH_MODE = "session_cookie"
ADMIN_BACKEND_VERSION = "0.1.0"


class AdminSessionStore:
    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._sessions: dict[str, datetime] = {}

    def create(self) -> tuple[str, datetime]:
        token = secrets.token_hex(24)
        expires_at = datetime.now(timezone.utc) + timedelta(hours=24)
        with self._lock:
            self._sessions[token] = expires_at
        return token, expires_at

    def expires_at(self, token: str) -> datetime | None:
        token = token.strip()
        if not token:
            return None
        with self._lock:
            expires_at = self._sessions.get(token)
            if expires_at is None:
                return None
            if datetime.now(timezone.utc) >= expires_at:
                self._sessions.pop(token, None)
                return None
            return expires_at

    def delete(self, token: str) -> None:
        token = token.strip()
        if not token:
            return
        with self._lock:
            self._sessions.pop(token, None)


def shared_admin_features() -> dict[str, bool]:
    return {
        "providers": True,
        "credentials": True,
        "providerState": True,
        "stats": False,
        "logs": False,
        "users": False,
        "configImportExport": False,
    }


def current_admin_password() -> str:
    return os.getenv("NEWPLATFORM2API_ADMIN_PASSWORD", "changeme").strip() or "changeme"


def current_api_key() -> str:
    return os.getenv("NEWPLATFORM2API_API_KEY", "").strip()


class AppHandler(BaseHTTPRequestHandler):
    registry: ProviderRegistry = default_registry()
    sessions = AdminSessionStore()
    store: AdminStore | None = None

    def log_message(self, format: str, *args: object) -> None:  # noqa: A003
        return

    def do_OPTIONS(self) -> None:  # noqa: N802
        parsed = urlparse(self.path)
        if self._is_admin_api_path(parsed.path):
            return self._empty(204, self._admin_cors_headers())
        self._empty(404)

    def do_GET(self) -> None:  # noqa: N802
        parsed = urlparse(self.path)
        if parsed.path == "/health":
            return self._json(200, {"status": "ok", "project": "any2api-python"})
        if parsed.path == "/api/admin/meta":
            return self._admin_meta()
        if parsed.path == "/api/admin/auth/session":
            return self._admin_session()
        if parsed.path == "/admin/api/status":
            if not self._require_admin():
                return
            return self._admin_status()
        if parsed.path == "/admin/api/settings":
            if not self._require_admin():
                return
            return self._admin_settings()
        if parsed.path == "/admin/api/providers/cursor/config":
            if not self._require_admin():
                return
            return self._admin_cursor_config()
        if parsed.path == "/admin/api/providers/kiro/accounts":
            if not self._require_admin():
                return
            return self._admin_kiro_accounts()
        if parsed.path == "/admin/api/providers/grok/tokens":
            if not self._require_admin():
                return
            return self._admin_grok_tokens()
        if parsed.path == "/admin/api/providers/orchids/config":
            if not self._require_admin():
                return
            return self._admin_orchids_config()
        if parsed.path == "/v1/models":
            provider = parse_qs(parsed.query).get("provider", [None])[0]
            models = self.registry.models(provider)
            data = [{"id": item["public_model"], "object": "model", "owned_by": item["owned_by"], "provider": item["provider"], "upstream_model": item["upstream_model"]} for item in models]
            return self._json(200, {"object": "list", "data": data})
        self._json(404, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802
        parsed = urlparse(self.path)
        if parsed.path == "/api/admin/auth/logout":
            return self._admin_logout()

        payload = self._read_json_object()
        if payload is None:
            return

        if parsed.path == "/api/admin/auth/login":
            return self._admin_login(payload)

        if parsed.path == "/v1/chat/completions":
            return self._openai_chat(payload)
        if parsed.path == "/v1/messages":
            return self._anthropic_messages(payload)
        self._json(404, {"error": "not found"})

    def do_PUT(self) -> None:  # noqa: N802
        parsed = urlparse(self.path)
        if parsed.path == "/admin/api/settings":
            if not self._require_admin():
                return
            payload = self._read_json_object()
            if payload is None:
                return
            return self._admin_settings(payload)
        if parsed.path == "/admin/api/providers/cursor/config":
            if not self._require_admin():
                return
            payload = self._read_json_object()
            if payload is None:
                return
            return self._admin_cursor_config(payload)
        if parsed.path == "/admin/api/providers/kiro/accounts":
            if not self._require_admin():
                return
            payload = self._read_json_object()
            if payload is None:
                return
            return self._admin_kiro_accounts(payload)
        if parsed.path == "/admin/api/providers/grok/tokens":
            if not self._require_admin():
                return
            payload = self._read_json_object()
            if payload is None:
                return
            return self._admin_grok_tokens(payload)
        if parsed.path == "/admin/api/providers/orchids/config":
            if not self._require_admin():
                return
            payload = self._read_json_object()
            if payload is None:
                return
            return self._admin_orchids_config(payload)
        self._json(404, {"error": "not found"})

    def _admin_meta(self) -> None:
        self._json(200, {
            "backend": {"language": "python", "version": ADMIN_BACKEND_VERSION},
            "auth": {"mode": ADMIN_AUTH_MODE},
            "features": shared_admin_features(),
        })

    def _admin_login(self, payload: dict) -> None:
        if str(payload.get("password", "")).strip() != self._store().admin_password():
            return self._json(401, {"error": "invalid admin password"})
        token, _ = self.sessions.create()
        self._json(200, {"ok": True, "token": token}, extra_headers={
            "Set-Cookie": f"{ADMIN_SESSION_COOKIE}={token}; Path=/; HttpOnly; SameSite=Lax; Max-Age=86400",
        })

    def _admin_session(self) -> None:
        token = self._admin_session_token()
        expires_at = self.sessions.expires_at(token)
        if expires_at is None:
            return self._json(401, {"error": "admin login required"})
        self._json(200, {
            "authenticated": True,
            "user": {"id": "local-admin", "name": "Admin", "role": "admin"},
            "expiresAt": expires_at.isoformat().replace("+00:00", "Z"),
        })

    def _admin_logout(self) -> None:
        self.sessions.delete(self._admin_session_token())
        self._json(200, {"ok": True}, extra_headers={
            "Set-Cookie": f"{ADMIN_SESSION_COOKIE}=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0",
        })

    def _admin_status(self) -> None:
        data = self._store().snapshot()
        settings = self._admin_settings_body(data)
        providers = data["providers"]
        cursor_config = providers["cursorConfig"]
        orchids_config = providers["orchidsConfig"]
        kiro_accounts = providers["kiroAccounts"]
        grok_tokens = providers["grokTokens"]
        self._json(200, {
            "project": "any2api-python",
            "settings": settings,
            "providers": {
                "cursor": self._single_provider_status(bool(cursor_config.get("cookie"))),
                "kiro": self._multi_provider_status(kiro_accounts, "accessToken"),
                "grok": self._multi_provider_status(grok_tokens, "cookieToken"),
                "orchids": self._single_provider_status(bool(orchids_config.get("clientCookie"))),
            },
        })

    def _admin_settings(self, payload: dict | None = None) -> None:
        if payload is None:
            return self._json(200, self._admin_settings_body(self._store().snapshot()))
        data = self._store().update_settings(
            payload.get("apiKey", ""),
            payload.get("defaultProvider", ""),
            payload.get("adminPassword", ""),
        )
        self.registry.default_provider = data["settings"]["defaultProvider"]
        self._json(200, self._admin_settings_body(data))

    def _admin_cursor_config(self, payload: dict | None = None) -> None:
        if payload is None:
            return self._json(200, {"config": self._store().snapshot()["providers"]["cursorConfig"]})
        data = self._store().replace_cursor_config(payload.get("config", {}))
        self._json(200, {"config": data["providers"]["cursorConfig"]})

    def _admin_kiro_accounts(self, payload: dict | None = None) -> None:
        if payload is None:
            return self._json(200, {"accounts": self._store().snapshot()["providers"]["kiroAccounts"]})
        data = self._store().replace_kiro_accounts(payload.get("accounts", []))
        self._json(200, {"accounts": data["providers"]["kiroAccounts"]})

    def _admin_grok_tokens(self, payload: dict | None = None) -> None:
        if payload is None:
            return self._json(200, {"tokens": self._store().snapshot()["providers"]["grokTokens"]})
        data = self._store().replace_grok_tokens(payload.get("tokens", []))
        self._json(200, {"tokens": data["providers"]["grokTokens"]})

    def _admin_orchids_config(self, payload: dict | None = None) -> None:
        if payload is None:
            return self._json(200, {"config": self._store().snapshot()["providers"]["orchidsConfig"]})
        data = self._store().replace_orchids_config(payload.get("config", {}))
        self._json(200, {"config": data["providers"]["orchidsConfig"]})

    def _admin_session_token(self) -> str:
        authorization = self.headers.get("Authorization", "").strip()
        if authorization.startswith("Bearer "):
            token = authorization.removeprefix("Bearer ").strip()
            if token:
                return token
        cookie_header = self.headers.get("Cookie", "")
        for chunk in cookie_header.split(";"):
            name, _, value = chunk.strip().partition("=")
            if name == ADMIN_SESSION_COOKIE and value.strip():
                return value.strip()
        return ""

    def _admin_cors_headers(self) -> dict[str, str]:
        origin = self.headers.get("Origin", "*") or "*"
        headers = {
            "Access-Control-Allow-Origin": origin,
            "Access-Control-Allow-Headers": "Content-Type, Authorization",
            "Access-Control-Allow-Methods": "GET, POST, PUT, OPTIONS",
        }
        if origin != "*":
            headers["Access-Control-Allow-Credentials"] = "true"
        return headers

    def _require_admin(self) -> bool:
        token = self._admin_session_token()
        if self.sessions.expires_at(token) is None:
            self._json(401, {"error": "admin login required"})
            return False
        return True

    def _is_admin_api_path(self, path: str) -> bool:
        return path.startswith("/api/admin/") or path.startswith("/admin/api/")

    def _read_json_object(self) -> dict | None:
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length) if length else b"{}"
        try:
            payload = json.loads(raw or b"{}")
        except json.JSONDecodeError:
            self._json(400, {"error": "invalid json"})
            return None
        if not isinstance(payload, dict):
            self._json(400, {"error": "invalid json"})
            return None
        return payload

    def _store(self) -> AdminStore:
        if self.store is None:
            raise RuntimeError("admin store not initialized")
        return self.store

    def _admin_settings_body(self, data: dict) -> dict[str, object]:
        settings = data["settings"]
        return {
            "apiKey": settings["apiKey"],
            "defaultProvider": settings["defaultProvider"],
            "adminPasswordConfigured": bool(str(settings.get("adminPassword", "")).strip()),
        }

    def _single_provider_status(self, configured: bool) -> dict[str, object]:
        return {"count": 1 if configured else 0, "configured": configured, "active": "default" if configured else ""}

    def _multi_provider_status(self, items: list[dict], secret_field: str) -> dict[str, object]:
        active = ""
        configured = False
        for item in items:
            if str(item.get(secret_field, "")).strip():
                configured = True
            if item.get("active"):
                active = str(item.get("id", "")).strip()
        return {"count": len(items), "configured": configured, "active": active}

    def _openai_chat(self, payload: dict) -> None:
        provider = self._resolve(payload.get("provider"))
        if provider is None:
            return
        req = UnifiedRequest(
            provider_hint=payload.get("provider", ""),
            protocol="openai",
            model=payload.get("model", ""),
            messages=payload.get("messages", []),
            tools=payload.get("tools", []),
            stream=bool(payload.get("stream")),
        )
        text = self._generate_reply(provider, req)
        if text is None:
            return
        completion_id = f"chatcmpl_{secrets.token_hex(8)}"
        if req.stream:
            return self._sse_openai(text, provider.provider_id(), completion_id)
        self._json(200, {"id": completion_id, "object": "chat.completion", "model": req.model, "choices": [{"index": 0, "message": {"role": "assistant", "content": text}, "finish_reason": "stop"}]}, extra_headers={"X-Newplatform2API-Provider": provider.provider_id()})

    def _anthropic_messages(self, payload: dict) -> None:
        provider = self._resolve(payload.get("provider"))
        if provider is None:
            return
        req = UnifiedRequest(
            provider_hint=payload.get("provider", ""),
            protocol="anthropic",
            model=payload.get("model", ""),
            messages=payload.get("messages", []),
            system=payload.get("system"),
            tools=payload.get("tools", []),
            stream=bool(payload.get("stream")),
        )
        text = self._generate_reply(provider, req)
        if text is None:
            return
        message_id = f"msg_{secrets.token_hex(8)}"
        if req.stream:
            return self._sse_anthropic(text, provider.provider_id(), message_id)
        self._json(200, {"id": message_id, "type": "message", "role": "assistant", "model": req.model, "content": [{"type": "text", "text": text}], "stop_reason": "end_turn"}, extra_headers={"X-Newplatform2API-Provider": provider.provider_id()})

    def _generate_reply(self, provider, req: UnifiedRequest) -> str | None:
        try:
            return provider.generate_reply(req)
        except ProviderRequestError as exc:
            self._json(502, {"error": str(exc)}, extra_headers={"X-Newplatform2API-Provider": provider.provider_id()})
            return None

    def _resolve(self, provider_id: str | None):
        try:
            return self.registry.resolve(provider_id)
        except KeyError as exc:
            self._json(400, {"error": str(exc)})
            return None

    def _json(self, status: int, body: dict, extra_headers: dict[str, str] | None = None) -> None:
        encoded = json.dumps(body).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(encoded)))
        headers = dict(extra_headers or {})
        if self._is_admin_api_path(urlparse(self.path).path):
            headers = {**self._admin_cors_headers(), **headers}
        for key, value in headers.items():
            self.send_header(key, value)
        self.end_headers()
        self.wfile.write(encoded)

    def _empty(self, status: int, extra_headers: dict[str, str] | None = None) -> None:
        self.send_response(status)
        headers = extra_headers or {}
        for key, value in headers.items():
            self.send_header(key, value)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def _sse_openai(self, text: str, provider_id: str, completion_id: str) -> None:
        body = f'data: {{"id":"{completion_id}","object":"chat.completion.chunk","choices":[{{"index":0,"delta":{{"role":"assistant","content":{json.dumps(text)}}},"finish_reason":null}}]}}\n\ndata: [DONE]\n\n'.encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("X-Newplatform2API-Provider", provider_id)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _sse_anthropic(self, text: str, provider_id: str, message_id: str) -> None:
        chunks = (
            f"event: message_start\ndata: {{\"type\":\"message_start\",\"message\":{{\"id\":\"{message_id}\"}}}}\n\n"
            f"event: content_block_delta\ndata: {{\"type\":\"content_block_delta\",\"delta\":{{\"type\":\"text_delta\",\"text\":{json.dumps(text)}}}}}\n\n"
            "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
        ).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("X-Newplatform2API-Provider", provider_id)
        self.send_header("Content-Length", str(len(chunks)))
        self.end_headers()
        self.wfile.write(chunks)


def create_server(host: str = "127.0.0.1", port: int = 8100) -> ThreadingHTTPServer:
    store = AdminStore(
        default_admin_store_path(),
        os.getenv("NEWPLATFORM2API_DEFAULT_PROVIDER", "cursor"),
        current_admin_password(),
        current_api_key(),
    )
    AppHandler.store = store
    AppHandler.registry = default_registry(store.snapshot()["settings"]["defaultProvider"], snapshot_getter=store.snapshot)
    AppHandler.sessions = AdminSessionStore()
    return ThreadingHTTPServer((host, port), AppHandler)


if __name__ == "__main__":
    server = create_server()
    print("any2api-python listening on http://127.0.0.1:8100")
    server.serve_forever()
