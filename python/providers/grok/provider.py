from __future__ import annotations

import json
import os
import re
import uuid
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
    _normalize_incremental_chunk,
    _normalize_messages,
    _pick_active_item,
    _read_http_error,
    _string,
    _text_value,
)

DEFAULT_GROK_API_URL = "https://grok.com/rest/app-chat/conversations/new"
DEFAULT_GROK_USER_AGENT = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
DEFAULT_GROK_ORIGIN = "https://grok.com"
DEFAULT_GROK_REFERER = "https://grok.com/"

_GROK_TOOL_USAGE_CARD_RE = re.compile(r"<xai:tool_usage_card[^>]*>.*?</xai:tool_usage_card>", re.DOTALL)
_GROK_TOOL_NAME_RE = re.compile(r"<xai:tool_name>(.*?)</xai:tool_name>", re.DOTALL)
_GROK_TOOL_ARGS_RE = re.compile(r"<xai:tool_args>(.*?)</xai:tool_args>", re.DOTALL)
_GROK_CDATA_RE = re.compile(r"<!\[CDATA\[(.*?)\]\]>", re.DOTALL)
_GROK_ROLLOUT_RE = re.compile(r"<rolloutId>.*?</rolloutId>", re.DOTALL)
_GROK_SPECIAL_TAG_RE = re.compile(r"</?xai:[^>]+>")


class GrokStreamFilter:
    def __init__(self) -> None:
        self.tool_card_open = False
        self.buffer = ""

    def filter(self, token: str) -> str:
        if not token:
            return ""
        start_tag = "<xai:tool_usage_card"
        end_tag = "</xai:tool_usage_card>"
        output: list[str] = []
        remaining = token
        while remaining:
            if self.tool_card_open:
                end_index = remaining.find(end_tag)
                if end_index == -1:
                    self.buffer += remaining
                    return "".join(output)
                end_pos = end_index + len(end_tag)
                self.buffer += remaining[:end_pos]
                summary = _summarize_grok_tool_card(self.buffer)
                if summary:
                    output.append(summary)
                    if not summary.endswith("\n"):
                        output.append("\n")
                self.buffer = ""
                self.tool_card_open = False
                remaining = remaining[end_pos:]
                continue
            start_index = remaining.find(start_tag)
            if start_index == -1:
                output.append(remaining)
                break
            if start_index > 0:
                output.append(remaining[:start_index])
            end_index = remaining[start_index:].find(end_tag)
            if end_index == -1:
                self.tool_card_open = True
                self.buffer += remaining[start_index:]
                break
            end_pos = start_index + end_index + len(end_tag)
            summary = _summarize_grok_tool_card(remaining[start_index:end_pos])
            if summary:
                output.append(summary)
                if not summary.endswith("\n"):
                    output.append("\n")
            remaining = remaining[end_pos:]
        return "".join(output)


class GrokProvider(StoreBackedProvider):
    def __init__(self, snapshot_getter: SnapshotGetter | None):
        super().__init__(
            "grok",
            ProviderCapabilities(openai_compatible=True, tools=True, images=True, multi_account=True),
            [ModelInfo("grok", "grok-4", "grok-4", "xai")],
            snapshot_getter,
        )

    def build_upstream_preview(self, req: UnifiedRequest) -> dict:
        payload = self._build_payload(req)
        token = self._token()
        return {
            "url": self._api_url(),
            "auth": "grok sso cookie token",
            "live_enabled": True,
            "cookie_configured": bool(_string(token.get("cookieToken"))),
            "payload": {"model": payload["modelName"], "message_len": len(payload["message"]), "message_count": len(req.messages)},
        }

    def generate_reply(self, req: UnifiedRequest) -> str:
        token = self._token()
        cookie_token = _string(token.get("cookieToken"))
        if not cookie_token:
            raise ProviderRequestError("grok cookie token is not configured")
        request = Request(self._api_url(), data=json.dumps(self._build_payload(req)).encode("utf-8"), headers=self._headers(cookie_token), method="POST")
        try:
            with urlopen(request, timeout=_env_int("NEWPLATFORM2API_GROK_TIMEOUT", DEFAULT_TIMEOUT_SECONDS)) as response:
                return self._collect_text(response.read().decode("utf-8", errors="replace"))
        except HTTPError as exc:
            raise ProviderRequestError(f"grok upstream error: status={exc.code} body={_read_http_error(exc)}") from exc
        except URLError as exc:
            raise ProviderRequestError(f"grok upstream request failed: {exc.reason}") from exc

    def _token(self) -> dict[str, Any]:
        providers = self._snapshot().get("providers", {})
        tokens = providers.get("grokTokens") if isinstance(providers, dict) else []
        return _pick_active_item(tokens, "cookieToken")

    def _api_url(self) -> str:
        return os.getenv("NEWPLATFORM2API_GROK_API_URL", DEFAULT_GROK_API_URL).strip() or DEFAULT_GROK_API_URL

    def _headers(self, cookie_token: str) -> dict[str, str]:
        return {
            "Accept": "*/*",
            "Content-Type": "application/json",
            "Cookie": _build_grok_cookie_header(cookie_token),
            "Origin": os.getenv("NEWPLATFORM2API_GROK_ORIGIN", DEFAULT_GROK_ORIGIN).strip() or DEFAULT_GROK_ORIGIN,
            "Referer": os.getenv("NEWPLATFORM2API_GROK_REFERER", DEFAULT_GROK_REFERER).strip() or DEFAULT_GROK_REFERER,
            "User-Agent": os.getenv("NEWPLATFORM2API_GROK_USER_AGENT", DEFAULT_GROK_USER_AGENT).strip() or DEFAULT_GROK_USER_AGENT,
            "X-Statsig-Id": uuid.uuid4().hex[:16],
            "X-XAI-Request-Id": uuid.uuid4().hex,
            "X-Requested-With": "XMLHttpRequest",
        }

    def _build_payload(self, req: UnifiedRequest) -> dict[str, Any]:
        return {
            "deviceEnvInfo": {
                "darkModeEnabled": False,
                "devicePixelRatio": 2,
                "screenWidth": 2056,
                "screenHeight": 1329,
                "viewportWidth": 2056,
                "viewportHeight": 1083,
            },
            "disableMemory": False,
            "disableSearch": False,
            "disableSelfHarmShortCircuit": False,
            "disableTextFollowUps": False,
            "enableImageGeneration": True,
            "enableImageStreaming": True,
            "enableSideBySide": True,
            "fileAttachments": [],
            "forceConcise": False,
            "forceSideBySide": False,
            "imageAttachments": [],
            "imageGenerationCount": 2,
            "isAsyncChat": False,
            "isReasoning": False,
            "message": self._flatten_messages(req),
            "modelName": _string(req.model) or "grok-4",
            "responseMetadata": {"requestModelDetails": {"modelId": _string(req.model) or "grok-4"}},
            "returnImageBytes": False,
            "returnRawGrokInXaiRequest": False,
            "sendFinalMetadata": True,
            "temporary": False,
            "toolOverrides": {},
        }

    def _flatten_messages(self, req: UnifiedRequest) -> str:
        normalized = _normalize_messages(req.messages)
        parts: list[dict[str, str]] = []
        for message in normalized:
            text = _content_text(message.get("content"))
            if not text:
                continue
            role = _string(message.get("role")).lower() or "user"
            parts.append({"role": role, "text": text})
        if not parts:
            return "."
        last_user_index = -1
        for index in range(len(parts) - 1, -1, -1):
            if parts[index]["role"] == "user":
                last_user_index = index
                break
        output: list[str] = []
        for index, part in enumerate(parts):
            output.append(part["text"] if index == last_user_index else f"{part['role']}: {part['text']}")
        return "\n\n".join(output)

    def _collect_text(self, raw: str) -> str:
        filter_state = GrokStreamFilter()
        last_message = ""
        token_seen = False
        parts: list[str] = []
        for line in raw.splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                payload = json.loads(line)
            except json.JSONDecodeError:
                continue
            response = _nested_map(payload, "result", "response")
            if response is None:
                continue
            token = _text_value(response.get("token"))
            if token != "":
                token_seen = True
                filtered = _strip_grok_artifacts(filter_state.filter(token))
                if filtered:
                    parts.append(filtered)
                continue
            if token_seen:
                continue
            model_response = response.get("modelResponse")
            if not isinstance(model_response, dict):
                continue
            message = _text_value(model_response.get("message"))
            if message == "":
                continue
            filtered = _strip_grok_artifacts(message)
            delta = _normalize_incremental_chunk(filtered, last_message)
            if delta:
                last_message = filtered
                parts.append(delta)
        return "".join(parts).strip()


def _build_grok_cookie_header(token: str) -> str:
    trimmed = _string(token)
    if not trimmed:
        return ""
    if ";" in trimmed:
        return trimmed
    trimmed = trimmed.removeprefix("sso=")
    return f"sso={trimmed}; sso-rw={trimmed}"


def _summarize_grok_tool_card(raw: str) -> str:
    name_match = _GROK_TOOL_NAME_RE.search(raw)
    args_match = _GROK_TOOL_ARGS_RE.search(raw)
    name = _GROK_CDATA_RE.sub(r"\1", name_match.group(1)).strip() if name_match else ""
    args = _GROK_CDATA_RE.sub(r"\1", args_match.group(1)).strip() if args_match else ""
    if not name and not args:
        return ""
    if not args:
        return f"[{name}]"
    return f"[{name}] {args}"


def _strip_grok_artifacts(text: str) -> str:
    if not text:
        return ""
    cleaned = _GROK_TOOL_USAGE_CARD_RE.sub(lambda match: _summarize_grok_tool_card(match.group(0)), text)
    cleaned = _GROK_ROLLOUT_RE.sub("", cleaned)
    cleaned = _GROK_SPECIAL_TAG_RE.sub("", cleaned)
    return cleaned