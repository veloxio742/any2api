from __future__ import annotations

import json
import os
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
    _env_int,
    _normalize_incremental_chunk,
    _normalize_messages,
    _pick_active_item,
    _read_http_error,
    _split_system_messages,
    _string,
    _text_value,
)

DEFAULT_KIRO_CODEWHISPERER_URL = "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse"
DEFAULT_KIRO_AMAZONQ_URL = "https://q.us-east-1.amazonaws.com/generateAssistantResponse"
KIRO_VERSION = "0.7.45"


class KiroProvider(StoreBackedProvider):
    def __init__(self, snapshot_getter: SnapshotGetter | None):
        super().__init__(
            "kiro",
            ProviderCapabilities(openai_compatible=True, anthropic_compatible=True, tools=True, images=True, multi_account=True),
            [ModelInfo("kiro", "claude-sonnet-4.6", "claude-sonnet-4.6", "amazonq/kiro")],
            snapshot_getter,
        )

    def build_upstream_preview(self, req: UnifiedRequest) -> dict:
        account = self._account()
        endpoint = self._sorted_endpoints(account)[0]
        payload = self._build_request(req, endpoint["origin"])
        return {
            "url": endpoint["url"],
            "auth": "bearer access token + x-amz-user-agent",
            "live_enabled": True,
            "token_configured": bool(_string(account.get("accessToken"))),
            "preferred_endpoint": _string(account.get("preferredEndpoint")),
            "payload": {
                "protocol": req.protocol,
                "model": payload["conversationState"]["currentMessage"]["userInputMessage"].get("modelId", ""),
                "history_count": len(payload["conversationState"].get("history", [])),
            },
        }

    def generate_reply(self, req: UnifiedRequest) -> str:
        account = self._account()
        access_token = _string(account.get("accessToken"))
        if not access_token:
            raise ProviderRequestError("kiro access token is not configured")

        machine_id = _string(account.get("machineId")) or str(uuid.uuid4())
        last_error: Exception | None = None
        for endpoint in self._sorted_endpoints(account):
            payload = self._build_request(req, endpoint["origin"])
            headers = self._headers(access_token, machine_id, endpoint["amz_target"])
            request = Request(endpoint["url"], data=json.dumps(payload).encode("utf-8"), headers=headers, method="POST")
            try:
                with urlopen(request, timeout=_env_int("NEWPLATFORM2API_KIRO_TIMEOUT", DEFAULT_TIMEOUT_SECONDS)) as response:
                    return self._collect_text(response.read())
            except HTTPError as exc:
                body = _read_http_error(exc)
                if exc.code == 429:
                    last_error = ProviderRequestError(f"kiro endpoint {endpoint['name']} returned 429")
                    continue
                last_error = ProviderRequestError(f"kiro upstream error: status={exc.code} body={body}")
                if exc.code in (401, 403):
                    raise last_error
            except URLError as exc:
                last_error = ProviderRequestError(f"kiro upstream request failed: {exc.reason}")
        if last_error is not None:
            raise last_error
        raise ProviderRequestError("kiro upstream request failed")

    def _account(self) -> dict[str, Any]:
        providers = self._snapshot().get("providers", {})
        accounts = providers.get("kiroAccounts") if isinstance(providers, dict) else []
        return _pick_active_item(accounts, "accessToken")

    def _sorted_endpoints(self, account: dict[str, Any]) -> list[dict[str, str]]:
        codewhisperer = {
            "name": "codewhisperer",
            "url": os.getenv("NEWPLATFORM2API_KIRO_CODEWHISPERER_URL", DEFAULT_KIRO_CODEWHISPERER_URL).strip() or DEFAULT_KIRO_CODEWHISPERER_URL,
            "origin": "AI_EDITOR",
            "amz_target": "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
        }
        amazonq = {
            "name": "amazonq",
            "url": os.getenv("NEWPLATFORM2API_KIRO_AMAZONQ_URL", DEFAULT_KIRO_AMAZONQ_URL).strip() or DEFAULT_KIRO_AMAZONQ_URL,
            "origin": "CLI",
            "amz_target": "AmazonQDeveloperStreamingService.SendMessage",
        }
        preferred = (_string(account.get("preferredEndpoint")) or os.getenv("NEWPLATFORM2API_KIRO_PREFERRED_ENDPOINT", "")).lower()
        if preferred == "amazonq":
            return [amazonq, codewhisperer]
        return [codewhisperer, amazonq]

    def _headers(self, access_token: str, machine_id: str, amz_target: str) -> dict[str, str]:
        user_agent, amz_user_agent = self._user_agents(machine_id)
        return {
            "Content-Type": "application/json",
            "Accept": "*/*",
            "Authorization": f"Bearer {access_token}",
            "X-Amz-Target": amz_target,
            "User-Agent": user_agent,
            "X-Amz-User-Agent": amz_user_agent,
            "x-amzn-kiro-agent-mode": "vibe",
            "x-amzn-codewhisperer-optout": "true",
            "Amz-Sdk-Request": "attempt=1; max=2",
            "Amz-Sdk-Invocation-Id": uuid.uuid4().hex,
        }

    def _user_agents(self, machine_id: str) -> tuple[str, str]:
        if not machine_id:
            return (
                f"aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.21.1 api/codewhispererstreaming#1.0.27 m/E KiroIDE-{KIRO_VERSION}",
                f"aws-sdk-js/1.0.27 KiroIDE {KIRO_VERSION}",
            )
        return (
            f"aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.21.1 api/codewhispererstreaming#1.0.27 m/E KiroIDE-{KIRO_VERSION}-{machine_id}",
            f"aws-sdk-js/1.0.27 KiroIDE {KIRO_VERSION} {machine_id}",
        )

    def _build_request(self, req: UnifiedRequest, origin: str) -> dict[str, Any]:
        normalized = _normalize_messages(req.messages)
        model_id = _map_kiro_model(req.model)
        system_prompt, non_system = _split_system_messages(normalized)
        if not non_system:
            non_system = [{"role": "user", "content": "."}]

        history: list[dict[str, Any]] = []
        current_content = ""
        system_merged = False
        for index, message in enumerate(non_system):
            text = _string(message.get("content"))
            role = _string(message.get("role")).lower() or "user"
            is_last = index == len(non_system) - 1
            if role == "assistant":
                if text:
                    history.append({"assistantResponseMessage": {"content": text}})
                continue
            if role != "user" and text:
                text = f"{role}: {text}"
            if not system_merged and system_prompt:
                text = system_prompt if not text else f"{system_prompt}\n\n{text}"
                system_merged = True
            text = text or "."
            entry = {"content": text, "modelId": model_id, "origin": origin}
            if is_last:
                current_content = text
            else:
                history.append({"userInputMessage": entry})
        if not current_content:
            current_content = "."
            if not system_merged and system_prompt:
                current_content = f"{system_prompt}\n\n{current_content}"
        return {
            "conversationState": {
                "chatTriggerType": "MANUAL",
                "conversationId": uuid.uuid4().hex,
                "currentMessage": {"userInputMessage": {"content": current_content, "modelId": model_id, "origin": origin}},
                "history": history,
            },
        }

    def _collect_text(self, raw: bytes) -> str:
        offset = 0
        last_assistant = ""
        last_reasoning = ""
        parts: list[str] = []
        while offset + 12 <= len(raw):
            prelude = raw[offset: offset + 12]
            total_length = int.from_bytes(prelude[:4], "big")
            headers_length = int.from_bytes(prelude[4:8], "big")
            if total_length < 16 or offset + total_length > len(raw):
                break
            message = raw[offset + 12: offset + total_length]
            offset += total_length
            if headers_length > len(message) - 4:
                continue
            event_type = _extract_kiro_event_type(message[:headers_length])
            payload_bytes = message[headers_length:-4]
            if not payload_bytes:
                continue
            try:
                event = json.loads(payload_bytes.decode("utf-8"))
            except json.JSONDecodeError:
                continue
            delta = ""
            if event_type == "assistantResponseEvent":
                content = _text_value(event.get("content"))
                delta = _normalize_incremental_chunk(content, last_assistant)
                if delta:
                    last_assistant = content
            elif event_type == "reasoningContentEvent":
                reasoning = _text_value(event.get("text"))
                delta = _normalize_incremental_chunk(reasoning, last_reasoning)
                if delta:
                    last_reasoning = reasoning
            if delta:
                parts.append(delta)
        return "".join(parts).strip() or ""


def _map_kiro_model(model: str) -> str:
    lower = _string(model).lower()
    if not lower or "claude-sonnet-4.6" in lower or "claude-sonnet-4-6" in lower:
        return "claude-sonnet-4.6"
    if "claude-sonnet-4.5" in lower or "claude-sonnet-4-5" in lower or "claude-3-5-sonnet" in lower or "gpt-4o" in lower or "gpt-4" in lower:
        return "claude-sonnet-4.5"
    if lower.startswith("claude-"):
        return model
    return "claude-sonnet-4.6"


def _extract_kiro_event_type(headers: bytes) -> str:
    offset = 0
    while offset < len(headers):
        name_len = headers[offset]
        offset += 1
        if offset + name_len > len(headers):
            break
        name = headers[offset: offset + name_len].decode("utf-8", errors="ignore")
        offset += name_len
        if offset >= len(headers):
            break
        value_type = headers[offset]
        offset += 1
        if value_type == 7:
            if offset + 2 > len(headers):
                break
            value_len = (headers[offset] << 8) | headers[offset + 1]
            offset += 2
            if offset + value_len > len(headers):
                break
            value = headers[offset: offset + value_len].decode("utf-8", errors="ignore")
            offset += value_len
            if name == ":event-type":
                return value
            continue
        if value_type == 6:
            if offset + 2 > len(headers):
                break
            value_len = (headers[offset] << 8) | headers[offset + 1]
            offset += 2 + value_len
            continue
        skip_sizes = {0: 0, 1: 0, 2: 1, 3: 2, 4: 4, 5: 8, 8: 8, 9: 16}
        skip = skip_sizes.get(value_type)
        if skip is None:
            break
        offset += skip
    return ""