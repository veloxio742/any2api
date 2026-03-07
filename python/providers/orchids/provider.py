from __future__ import annotations

import json
import os
import threading
import time
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

from gateway_types import ModelInfo, ProviderCapabilities, UnifiedRequest
from ..common import (
    DEFAULT_TIMEOUT_SECONDS,
    ProviderRequestError,
    SnapshotGetter,
    StoreBackedProvider,
    _content_text,
    _env_int,
    _nested_map,
    _normalize_messages,
    _read_http_error,
    _string,
    _text_value,
    _trim_cookie_value,
)

DEFAULT_ORCHIDS_API_URL = "https://orchids-server.calmstone-6964e08a.westeurope.azurecontainerapps.io/agent/coding-agent"
DEFAULT_ORCHIDS_CLERK_URL = "https://clerk.orchids.app"
DEFAULT_ORCHIDS_PROJECT_ID = "280b7bae-cd29-41e4-a0a6-7f603c43b607"
DEFAULT_ORCHIDS_AGENT_MODE = "claude-opus-4.5"
ORCHIDS_CLERK_QUERY_SUFFIX = "?__clerk_api_version=2025-11-10&_clerk_js_version=5.117.0"
ORCHIDS_SYSTEM_PRESET = "你是 AI 编程助手，通过代理服务与用户交互。仅依赖当前工具和历史上下文，保持回复简洁专业。"
ORCHIDS_TOKEN_TTL_SECONDS = 50 * 60


class OrchidsProvider(StoreBackedProvider):
    def __init__(self, snapshot_getter: SnapshotGetter | None):
        super().__init__(
            "orchids",
            ProviderCapabilities(openai_compatible=True, anthropic_compatible=True, tools=True, multi_account=True),
            [ModelInfo("orchids", "claude-sonnet-4.5", "claude-sonnet-4-5", "orchids")],
            snapshot_getter,
        )
        self._lock = threading.Lock()
        self._cached_token = ""
        self._cached_token_until = 0.0
        self._cached_token_key = ""

    def build_upstream_preview(self, req: UnifiedRequest) -> dict:
        config = self._config()
        return {
            "url": config["apiUrl"],
            "auth": "clerk session cookie -> jwt bearer",
            "live_enabled": True,
            "configured": bool(config["clientCookie"]),
            "clerk_url": config["clerkUrl"],
            "mapped_model": self._map_model(req.model),
            "message_count": len(req.messages),
            "prompt_strategy": "messages -> orchids markdown prompt",
        }

    def generate_reply(self, req: UnifiedRequest) -> str:
        config = self._config()
        if not config["clientCookie"]:
            raise ProviderRequestError("orchids client cookie is not configured")
        account = self._resolve_account(config)
        token = self._get_token(config, account)
        request = Request(
            config["apiUrl"],
            data=json.dumps(self._build_agent_request(req, config, account)).encode("utf-8"),
            headers={
                "Accept": "text/event-stream",
                "Authorization": f"Bearer {token}",
                "Content-Type": "application/json",
                "X-Orchids-Api-Version": "2",
            },
            method="POST",
        )
        try:
            with urlopen(request, timeout=_env_int("NEWPLATFORM2API_ORCHIDS_TIMEOUT", DEFAULT_TIMEOUT_SECONDS)) as response:
                return self._collect_text(response.read().decode("utf-8", errors="replace"))
        except HTTPError as exc:
            if exc.code == 401:
                self._invalidate_token()
            raise ProviderRequestError(f"orchids upstream error: status={exc.code} body={_read_http_error(exc)}") from exc
        except URLError as exc:
            raise ProviderRequestError(f"orchids upstream request failed: {exc.reason}") from exc

    def _config(self) -> dict[str, str]:
        providers = self._snapshot().get("providers", {})
        raw = providers.get("orchidsConfig") if isinstance(providers, dict) and isinstance(providers.get("orchidsConfig"), dict) else {}
        return {
            "apiUrl": _string(raw.get("apiUrl")) or os.getenv("NEWPLATFORM2API_ORCHIDS_API_URL", DEFAULT_ORCHIDS_API_URL).strip() or DEFAULT_ORCHIDS_API_URL,
            "clerkUrl": (_string(raw.get("clerkUrl")) or os.getenv("NEWPLATFORM2API_ORCHIDS_CLERK_URL", DEFAULT_ORCHIDS_CLERK_URL)).rstrip("/"),
            "clientCookie": _trim_cookie_value(raw.get("clientCookie"), "__client="),
            "clientUat": _trim_cookie_value(raw.get("clientUat"), "__client_uat="),
            "sessionId": _string(raw.get("sessionId")),
            "projectId": _string(raw.get("projectId")) or DEFAULT_ORCHIDS_PROJECT_ID,
            "userId": _string(raw.get("userId")),
            "email": _string(raw.get("email")),
            "agentMode": _string(raw.get("agentMode")) or DEFAULT_ORCHIDS_AGENT_MODE,
        }

    def _resolve_account(self, config: dict[str, str]) -> dict[str, str]:
        account = dict(config)
        if not account["clientUat"]:
            account["clientUat"] = str(int(time.time()))
        if account["sessionId"] and account["userId"] and account["email"]:
            return account
        resolved = self._fetch_account_info(config["clerkUrl"], account["clientCookie"])
        account["sessionId"] = account["sessionId"] or resolved["sessionId"]
        account["userId"] = account["userId"] or resolved["userId"]
        account["email"] = account["email"] or resolved["email"]
        if not account["sessionId"] or not account["userId"] or not account["email"]:
            raise ProviderRequestError("orchids account identity is incomplete")
        return account

    def _fetch_account_info(self, clerk_url: str, client_cookie: str) -> dict[str, str]:
        request = Request(
            f"{clerk_url}/v1/client{ORCHIDS_CLERK_QUERY_SUFFIX}",
            headers={"User-Agent": "Mozilla/5.0", "Accept-Language": "zh-CN", "Cookie": f"__client={client_cookie}"},
            method="GET",
        )
        try:
            with urlopen(request, timeout=_env_int("NEWPLATFORM2API_ORCHIDS_TIMEOUT", DEFAULT_TIMEOUT_SECONDS)) as response:
                payload = json.loads(response.read().decode("utf-8"))
        except HTTPError as exc:
            raise ProviderRequestError(f"orchids account info error: status={exc.code} body={_read_http_error(exc)}") from exc
        except URLError as exc:
            raise ProviderRequestError(f"fetch orchids account info: {exc.reason}") from exc
        sessions = _nested_map(payload, "response")
        if sessions is None:
            raise ProviderRequestError("orchids account info missing active session")
        session_items = sessions.get("sessions") if isinstance(sessions.get("sessions"), list) else []
        last_active_session_id = _string(sessions.get("last_active_session_id"))
        if not session_items or not last_active_session_id:
            raise ProviderRequestError("orchids account info missing active session")
        user = session_items[0].get("user") if isinstance(session_items[0], dict) else {}
        email_addresses = user.get("email_addresses") if isinstance(user, dict) and isinstance(user.get("email_addresses"), list) else []
        email = _string(email_addresses[0].get("email_address")) if email_addresses and isinstance(email_addresses[0], dict) else ""
        user_id = _string(user.get("id")) if isinstance(user, dict) else ""
        if not email or not user_id:
            raise ProviderRequestError("orchids account info missing user identity")
        return {"sessionId": last_active_session_id, "userId": user_id, "email": email}

    def _get_token(self, config: dict[str, str], account: dict[str, str]) -> str:
        cache_key = f"{account['sessionId']}:{account['clientCookie']}:{account['clientUat']}"
        with self._lock:
            if self._cached_token and self._cached_token_key == cache_key and time.time() < self._cached_token_until:
                return self._cached_token
        request = Request(
            f"{config['clerkUrl']}/v1/client/sessions/{account['sessionId']}/tokens{ORCHIDS_CLERK_QUERY_SUFFIX}",
            data=b"organization_id=",
            headers={
                "Content-Type": "application/x-www-form-urlencoded",
                "Cookie": f"__client={account['clientCookie']}; __client_uat={account['clientUat']}",
            },
            method="POST",
        )
        try:
            with urlopen(request, timeout=_env_int("NEWPLATFORM2API_ORCHIDS_TIMEOUT", DEFAULT_TIMEOUT_SECONDS)) as response:
                payload = json.loads(response.read().decode("utf-8"))
        except HTTPError as exc:
            self._invalidate_token()
            raise ProviderRequestError(f"orchids token request failed: status={exc.code} body={_read_http_error(exc)}") from exc
        except URLError as exc:
            raise ProviderRequestError(f"fetch orchids token: {exc.reason}") from exc
        token = _string(payload.get("jwt"))
        if not token:
            raise ProviderRequestError("orchids token response missing jwt")
        with self._lock:
            self._cached_token = token
            self._cached_token_key = cache_key
            self._cached_token_until = time.time() + ORCHIDS_TOKEN_TTL_SECONDS
        return token

    def _invalidate_token(self) -> None:
        with self._lock:
            self._cached_token = ""
            self._cached_token_key = ""
            self._cached_token_until = 0.0

    def _build_agent_request(self, req: UnifiedRequest, config: dict[str, str], account: dict[str, str]) -> dict[str, Any]:
        mapped_model = self._map_model(req.model)
        agent_mode = config["agentMode"] or mapped_model
        return {
            "prompt": self._build_prompt(_normalize_messages(req.messages)),
            "chatHistory": [],
            "projectId": account["projectId"],
            "currentPage": {},
            "agentMode": agent_mode,
            "mode": "agent",
            "gitRepoUrl": "",
            "email": account["email"],
            "chatSessionId": int(time.time() * 1000) % 90000000 + 10000000,
            "userId": account["userId"],
            "apiVersion": 2,
            "model": mapped_model,
        }

    def _collect_text(self, raw: str) -> str:
        parts: list[str] = []
        for chunk in raw.split("\n\n"):
            for line in chunk.splitlines():
                if not line.startswith("data: "):
                    continue
                data = line.removeprefix("data: ").strip()
                if not data:
                    continue
                try:
                    message = json.loads(data)
                except json.JSONDecodeError:
                    continue
                if _string(message.get("type")) != "model":
                    continue
                event = message.get("event") if isinstance(message.get("event"), dict) else {}
                if _string(event.get("type")) != "text-delta":
                    continue
                delta = _text_value(event.get("delta"))
                if delta != "":
                    parts.append(delta)
        return "".join(parts).strip()

    def _map_model(self, model: str) -> str:
        lower = _string(model).lower()
        if "opus" in lower:
            return "claude-opus-4.5"
        if "haiku" in lower:
            return "gemini-3-flash"
        if not lower:
            return self.models()[0].upstream_model
        return "claude-sonnet-4-5"

    def _build_prompt(self, messages: list[dict[str, str]]) -> str:
        systems: list[str] = []
        dialogue: list[dict[str, str]] = []
        for message in messages:
            text = _content_text(message.get("content"))
            if not text:
                continue
            role = _string(message.get("role")).lower()
            if role == "system":
                systems.append(text)
                continue
            dialogue.append({"role": role, "content": text})
        sections: list[str] = []
        if systems:
            sections.append("<client_system>\n" + "\n\n".join(systems) + "\n</client_system>")
        sections.append(f"<proxy_instructions>\n{ORCHIDS_SYSTEM_PRESET}\n</proxy_instructions>")
        history = self._format_history(dialogue)
        if history:
            sections.append(f"<conversation_history>\n{history}\n</conversation_history>")
        current_request = "继续"
        if dialogue and dialogue[-1]["role"] == "user" and dialogue[-1]["content"]:
            current_request = dialogue[-1]["content"]
        sections.append(f"<user_request>\n{current_request}\n</user_request>")
        return "\n\n".join(sections)

    def _format_history(self, messages: list[dict[str, str]]) -> str:
        history = messages[:-1] if messages and messages[-1]["role"] == "user" else messages
        parts: list[str] = []
        turn_index = 1
        for message in history:
            role = message["role"]
            if role not in {"user", "assistant"} or not message["content"]:
                continue
            parts.append(f'<turn index="{turn_index}" role="{role}">\n{message["content"]}\n</turn>')
            turn_index += 1
        return "\n\n".join(parts)