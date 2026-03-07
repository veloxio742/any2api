from __future__ import annotations

import json
import os
from typing import Any, Callable, Protocol
from urllib.error import HTTPError

from gateway_types import ModelInfo, ProviderCapabilities, UnifiedRequest

DEFAULT_TIMEOUT_SECONDS = 60
DEFAULT_MAX_INPUT_LENGTH = 200000


class ProviderRequestError(RuntimeError):
    pass


SnapshotGetter = Callable[[], dict[str, Any]]


class Provider(Protocol):
    def provider_id(self) -> str: ...
    def capabilities(self) -> ProviderCapabilities: ...
    def models(self) -> list[ModelInfo]: ...
    def build_upstream_preview(self, req: UnifiedRequest) -> dict: ...
    def generate_reply(self, req: UnifiedRequest) -> str: ...


class SimpleProvider:
    def __init__(self, provider_id: str, capabilities: ProviderCapabilities, models: list[ModelInfo], auth: str, url: str, note: str):
        self._provider_id = provider_id
        self._capabilities = capabilities
        self._models = models
        self._auth = auth
        self._url = url
        self._note = note

    def provider_id(self) -> str:
        return self._provider_id

    def capabilities(self) -> ProviderCapabilities:
        return self._capabilities

    def models(self) -> list[ModelInfo]:
        return self._models

    def build_upstream_preview(self, req: UnifiedRequest) -> dict:
        return {
            "url": self._url,
            "auth": self._auth,
            "note": self._note,
            "protocol": req.protocol,
            "message_count": len(req.messages),
        }

    def generate_reply(self, req: UnifiedRequest) -> str:
        return f"[{self._provider_id} skeleton] protocol={req.protocol} model={req.model or 'auto'}"


class StoreBackedProvider:
    def __init__(self, provider_id: str, capabilities: ProviderCapabilities, models: list[ModelInfo], snapshot_getter: SnapshotGetter | None):
        self._provider_id = provider_id
        self._capabilities = capabilities
        self._models = models
        self._snapshot_getter = snapshot_getter

    def provider_id(self) -> str:
        return self._provider_id

    def capabilities(self) -> ProviderCapabilities:
        return self._capabilities

    def models(self) -> list[ModelInfo]:
        return self._models

    def _snapshot(self) -> dict[str, Any]:
        if self._snapshot_getter is None:
            return {"settings": {}, "providers": {}}
        snapshot = self._snapshot_getter()
        return snapshot if isinstance(snapshot, dict) else {"settings": {}, "providers": {}}


def _string(value: Any) -> str:
    if value is None:
        return ""
    return str(value).strip()


def _text_value(value: Any) -> str:
    if value is None:
        return ""
    return str(value)


def _trim_cookie_value(value: Any, prefix: str) -> str:
    return _string(value).removeprefix(prefix)


def _content_text(content: Any) -> str:
    if content is None:
        return ""
    if isinstance(content, str):
        return content.strip()
    if isinstance(content, list):
        parts: list[str] = []
        for block in content:
            if not isinstance(block, dict):
                continue
            block_type = _string(block.get("type"))
            if block_type == "text":
                text = _string(block.get("text"))
                if text:
                    parts.append(text)
            elif block_type == "image":
                source = block.get("source") if isinstance(block.get("source"), dict) else {}
                media_type = _string(source.get("media_type"))
                parts.append(f"[Image: {media_type or 'unknown'}]")
            elif block_type == "tool_use":
                name = _string(block.get("name"))
                input_value = block.get("input", {})
                parts.append(f"<tool_use name=\"{name}\">{json.dumps(input_value, ensure_ascii=False)}</tool_use>")
            elif block_type == "tool_result":
                tool_use_id = _string(block.get("tool_use_id"))
                parts.append(f"<tool_result tool_use_id=\"{tool_use_id}\">{_tool_result_text(block.get('content'))}</tool_result>")
        return "\n".join(part for part in parts if part).strip()
    if isinstance(content, dict):
        return _string(content.get("text")) or json.dumps(content, ensure_ascii=False)
    return _string(content)


def _tool_result_text(content: Any) -> str:
    if isinstance(content, str):
        return content.strip()
    if isinstance(content, list):
        texts: list[str] = []
        for item in content:
            if isinstance(item, dict):
                text = _string(item.get("text"))
                if text:
                    texts.append(text)
        if texts:
            return "\n".join(texts)
    return json.dumps(content, ensure_ascii=False)


def _normalize_messages(messages: list[dict[str, Any]], max_input_length: int = DEFAULT_MAX_INPUT_LENGTH) -> list[dict[str, str]]:
    normalized: list[dict[str, str]] = []
    for message in messages:
        if not isinstance(message, dict):
            continue
        role = _string(message.get("role")).lower() or "user"
        text = _content_text(message.get("content"))
        if not text:
            continue
        normalized.append({"role": role, "content": text})
    if max_input_length <= 0:
        return normalized
    kept: list[dict[str, str]] = []
    remaining = max_input_length
    for message in reversed(normalized):
        text = message["content"]
        if len(text) <= remaining:
            kept.append(message)
            remaining -= len(text)
            continue
        if not kept:
            kept.append({"role": message["role"], "content": text[-max_input_length:]})
        break
    return list(reversed(kept))


def _split_system_messages(messages: list[dict[str, str]]) -> tuple[str, list[dict[str, str]]]:
    system_parts: list[str] = []
    non_system: list[dict[str, str]] = []
    for message in messages:
        if message.get("role") == "system":
            text = _string(message.get("content"))
            if text:
                system_parts.append(text)
            continue
        non_system.append(message)
    return "\n\n".join(system_parts), non_system


def _pick_active_item(items: Any, required_field: str) -> dict[str, Any]:
    if not isinstance(items, list):
        return {}
    for item in items:
        if isinstance(item, dict) and item.get("active") and _string(item.get(required_field)):
            return item
    for item in items:
        if isinstance(item, dict) and _string(item.get(required_field)):
            return item
    return {}


def _normalize_incremental_chunk(chunk: str, previous: str) -> str:
    if not chunk:
        return ""
    if not previous:
        return chunk
    if chunk == previous or previous.startswith(chunk):
        return ""
    if chunk.startswith(previous):
        return chunk[len(previous):]
    max_overlap = 0
    max_len = min(len(previous), len(chunk))
    for size in range(max_len, 0, -1):
        if previous.endswith(chunk[:size]):
            max_overlap = size
            break
    return chunk[max_overlap:] if max_overlap > 0 else chunk


def _nested_map(root: Any, *keys: str) -> dict[str, Any] | None:
    current = root
    for key in keys:
        if not isinstance(current, dict):
            return None
        current = current.get(key)
    return current if isinstance(current, dict) else None


def _read_http_error(exc: HTTPError) -> str:
    try:
        body = exc.read(4096)
    except Exception:
        return ""
    return body.decode("utf-8", errors="replace").strip()


def _env_int(name: str, default: int) -> int:
    raw = os.getenv(name, "").strip()
    if not raw:
        return default
    try:
        value = int(raw)
    except ValueError:
        return default
    return value if value > 0 else default