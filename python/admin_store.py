from __future__ import annotations

import json
import os
import threading
from copy import deepcopy
from pathlib import Path
from typing import Any


CURSOR_FIELDS = ("apiUrl", "scriptUrl", "cookie", "xIsHuman", "userAgent", "referer", "webglVendor", "webglRenderer")
ORCHIDS_FIELDS = ("apiUrl", "clerkUrl", "clientCookie", "clientUat", "sessionId", "projectId", "userId", "email", "agentMode")


def default_admin_store_path() -> Path:
    override = os.getenv("NEWPLATFORM2API_ADMIN_STORE_PATH", "").strip()
    if override:
        return Path(override).expanduser()
    return Path(__file__).resolve().parent / "data" / "admin.json"


def _string(value: Any) -> str:
    if value is None:
        return ""
    return str(value).strip()


def _normalize_object(value: Any, fields: tuple[str, ...]) -> dict[str, str]:
    raw = value if isinstance(value, dict) else {}
    return {field: _string(raw.get(field)) for field in fields}


def _normalize_kiro_accounts(value: Any) -> list[dict[str, Any]]:
    raw_items = value if isinstance(value, list) else []
    items: list[dict[str, Any]] = []
    active_set = False
    for item in raw_items:
        if not isinstance(item, dict):
            continue
        access_token = _string(item.get("accessToken"))
        machine_id = _string(item.get("machineId"))
        if not access_token and not machine_id:
            continue
        normalized = {
            "id": _string(item.get("id")) or f"kiro-{len(items) + 1}",
            "name": _string(item.get("name")) or f"Kiro Account {len(items) + 1}",
            "accessToken": access_token,
            "machineId": machine_id,
            "preferredEndpoint": _string(item.get("preferredEndpoint")).lower(),
            "active": bool(item.get("active")) and not active_set,
        }
        if normalized["active"]:
            active_set = True
        items.append(normalized)
    if items and not active_set:
        items[0]["active"] = True
    return items


def _normalize_grok_tokens(value: Any) -> list[dict[str, Any]]:
    raw_items = value if isinstance(value, list) else []
    items: list[dict[str, Any]] = []
    active_set = False
    for item in raw_items:
        if not isinstance(item, dict):
            continue
        cookie_token = _string(item.get("cookieToken"))
        if not cookie_token:
            continue
        normalized = {
            "id": _string(item.get("id")) or f"grok-{len(items) + 1}",
            "name": _string(item.get("name")) or f"Grok Token {len(items) + 1}",
            "cookieToken": cookie_token,
            "active": bool(item.get("active")) and not active_set,
        }
        if normalized["active"]:
            active_set = True
        items.append(normalized)
    if items and not active_set:
        items[0]["active"] = True
    return items


def _normalize_data(raw: Any, default_provider: str, admin_password: str, api_key: str) -> dict[str, Any]:
    source = raw if isinstance(raw, dict) else {}
    settings = source.get("settings") if isinstance(source.get("settings"), dict) else {}
    providers = source.get("providers") if isinstance(source.get("providers"), dict) else {}
    normalized_password = _string(settings.get("adminPassword")) or admin_password
    normalized_provider = _string(settings.get("defaultProvider")) or default_provider
    return {
        "settings": {
            "adminPassword": normalized_password,
            "apiKey": _string(settings.get("apiKey")) or api_key,
            "defaultProvider": normalized_provider,
        },
        "providers": {
            "cursorConfig": _normalize_object(providers.get("cursorConfig"), CURSOR_FIELDS),
            "kiroAccounts": _normalize_kiro_accounts(providers.get("kiroAccounts")),
            "grokTokens": _normalize_grok_tokens(providers.get("grokTokens")),
            "orchidsConfig": _normalize_object(providers.get("orchidsConfig"), ORCHIDS_FIELDS),
        },
    }


class AdminStore:
    def __init__(self, path: str | Path, default_provider: str, admin_password: str, api_key: str = "") -> None:
        self._path = Path(path)
        self._lock = threading.RLock()
        self._defaults = {
            "default_provider": _string(default_provider) or "cursor",
            "admin_password": _string(admin_password) or "changeme",
            "api_key": _string(api_key),
        }
        self._data = _normalize_data({}, self._defaults["default_provider"], self._defaults["admin_password"], self._defaults["api_key"])
        self._load()

    def snapshot(self) -> dict[str, Any]:
        with self._lock:
            return deepcopy(self._data)

    def admin_password(self) -> str:
        with self._lock:
            return _string(self._data["settings"].get("adminPassword")) or self._defaults["admin_password"]

    def update_settings(self, api_key: str, default_provider: str, admin_password: str) -> dict[str, Any]:
        with self._lock:
            self._data["settings"]["apiKey"] = _string(api_key)
            self._data["settings"]["defaultProvider"] = _string(default_provider) or self._defaults["default_provider"]
            if _string(admin_password):
                self._data["settings"]["adminPassword"] = _string(admin_password)
            self._persist_locked()
            return deepcopy(self._data)

    def replace_cursor_config(self, config: Any) -> dict[str, Any]:
        return self._replace_provider("cursorConfig", _normalize_object(config, CURSOR_FIELDS))

    def replace_kiro_accounts(self, accounts: Any) -> dict[str, Any]:
        return self._replace_provider("kiroAccounts", _normalize_kiro_accounts(accounts))

    def replace_grok_tokens(self, tokens: Any) -> dict[str, Any]:
        return self._replace_provider("grokTokens", _normalize_grok_tokens(tokens))

    def replace_orchids_config(self, config: Any) -> dict[str, Any]:
        return self._replace_provider("orchidsConfig", _normalize_object(config, ORCHIDS_FIELDS))

    def _replace_provider(self, key: str, value: Any) -> dict[str, Any]:
        with self._lock:
            self._data["providers"][key] = value
            self._persist_locked()
            return deepcopy(self._data)

    def _load(self) -> None:
        with self._lock:
            if not self._path.exists() or not self._path.read_text(encoding="utf-8").strip():
                self._persist_locked()
                return
            raw = json.loads(self._path.read_text(encoding="utf-8"))
            self._data = _normalize_data(raw, self._defaults["default_provider"], self._defaults["admin_password"], self._defaults["api_key"])

    def _persist_locked(self) -> None:
        self._path.parent.mkdir(parents=True, exist_ok=True)
        content = json.dumps(self._data, indent=2)
        self._path.write_text(f"{content}\n", encoding="utf-8")
