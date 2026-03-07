from __future__ import annotations

import http.client
import json
import os
import threading
import tempfile
import unittest
from unittest.mock import patch

from server import create_server


class FakeResponse:
    def __init__(self, body: bytes):
        self._body = body

    def read(self) -> bytes:
        return self._body

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        return False


class AdminContractTests(unittest.TestCase):
    def setUp(self):
        self.previous_password = os.environ.get("NEWPLATFORM2API_ADMIN_PASSWORD")
        self.previous_provider = os.environ.get("NEWPLATFORM2API_DEFAULT_PROVIDER")
        self.previous_store_path = os.environ.get("NEWPLATFORM2API_ADMIN_STORE_PATH")
        self.previous_api_key = os.environ.get("NEWPLATFORM2API_API_KEY")
        self.tempdir = tempfile.TemporaryDirectory()
        os.environ["NEWPLATFORM2API_ADMIN_PASSWORD"] = "changeme"
        os.environ["NEWPLATFORM2API_DEFAULT_PROVIDER"] = "cursor"
        os.environ["NEWPLATFORM2API_ADMIN_STORE_PATH"] = os.path.join(self.tempdir.name, "admin.json")
        os.environ["NEWPLATFORM2API_API_KEY"] = ""
        self._start_server()

    def tearDown(self):
        self._stop_server()
        self.tempdir.cleanup()
        self._restore_env("NEWPLATFORM2API_ADMIN_PASSWORD", self.previous_password)
        self._restore_env("NEWPLATFORM2API_DEFAULT_PROVIDER", self.previous_provider)
        self._restore_env("NEWPLATFORM2API_ADMIN_STORE_PATH", self.previous_store_path)
        self._restore_env("NEWPLATFORM2API_API_KEY", self.previous_api_key)

    def _restore_env(self, key: str, value: str | None) -> None:
        if value is None:
            os.environ.pop(key, None)
        else:
            os.environ[key] = value

    def _start_server(self):
        self.server = create_server("127.0.0.1", 0)
        self.port = self.server.server_address[1]
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def _stop_server(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)

    def restart_server(self):
        self._stop_server()
        self._start_server()

    def request(self, method: str, path: str, body: dict | None = None, headers: dict[str, str] | None = None):
        conn = http.client.HTTPConnection("127.0.0.1", self.port, timeout=5)
        payload = json.dumps(body).encode("utf-8") if body is not None else None
        actual_headers = {**(headers or {})}
        if payload is not None:
            actual_headers.setdefault("Content-Type", "application/json")
        conn.request(method, path, body=payload, headers=actual_headers)
        response = conn.getresponse()
        raw = response.read().decode("utf-8")
        conn.close()
        parsed = json.loads(raw) if raw else None
        return response, parsed

    def login(self, password: str = "changeme") -> str:
        response, body = self.request("POST", "/api/admin/auth/login", {"password": password})
        self.assertEqual(response.status, 200)
        self.assertTrue(body["ok"])
        self.assertTrue(body["token"])
        return body["token"]

    def test_meta_endpoint(self):
        response, body = self.request("GET", "/api/admin/meta", headers={"Origin": "http://localhost:1420"})
        self.assertEqual(response.status, 200)
        self.assertEqual(body["backend"]["language"], "python")
        self.assertEqual(body["auth"]["mode"], "session_cookie")
        self.assertTrue(body["features"]["providers"])
        self.assertTrue(body["features"]["credentials"])
        self.assertTrue(body["features"]["providerState"])
        self.assertFalse(body["features"]["configImportExport"])
        self.assertEqual(response.getheader("Access-Control-Allow-Origin"), "http://localhost:1420")

    def test_login_session_logout_lifecycle(self):
        bad_response, _ = self.request("POST", "/api/admin/auth/login", {"password": "wrong"})
        self.assertEqual(bad_response.status, 401)

        login_response, login_body = self.request("POST", "/api/admin/auth/login", {"password": "changeme"})
        self.assertEqual(login_response.status, 200)
        self.assertTrue(login_body["ok"])
        token = login_body["token"]
        self.assertTrue(token)
        self.assertIn("newplatform2api_admin_session=", login_response.getheader("Set-Cookie") or "")

        session_response, session_body = self.request(
            "GET",
            "/api/admin/auth/session",
            headers={"Authorization": f"Bearer {token}"},
        )
        self.assertEqual(session_response.status, 200)
        self.assertTrue(session_body["authenticated"])
        self.assertEqual(session_body["user"]["role"], "admin")

        logout_response, logout_body = self.request(
            "POST",
            "/api/admin/auth/logout",
            headers={"Authorization": f"Bearer {token}"},
        )
        self.assertEqual(logout_response.status, 200)
        self.assertTrue(logout_body["ok"])

        after_response, after_body = self.request(
            "GET",
            "/api/admin/auth/session",
            headers={"Authorization": f"Bearer {token}"},
        )
        self.assertEqual(after_response.status, 401)
        self.assertEqual(after_body["error"], "admin login required")

    def test_admin_api_requires_auth_and_supports_cors(self):
        unauth_response, unauth_body = self.request("GET", "/admin/api/settings")
        self.assertEqual(unauth_response.status, 401)
        self.assertEqual(unauth_body["error"], "admin login required")

        options_response, _ = self.request("OPTIONS", "/admin/api/settings", headers={"Origin": "http://localhost:1420"})
        self.assertEqual(options_response.status, 204)
        self.assertEqual(options_response.getheader("Access-Control-Allow-Origin"), "http://localhost:1420")
        self.assertIn("PUT", options_response.getheader("Access-Control-Allow-Methods") or "")

    def test_settings_and_provider_roundtrip_persist_across_restart(self):
        token = self.login()
        auth = {"Authorization": f"Bearer {token}"}

        settings_response, settings_body = self.request("GET", "/admin/api/settings", headers=auth)
        self.assertEqual(settings_response.status, 200)
        self.assertEqual(settings_body["defaultProvider"], "cursor")
        self.assertTrue(settings_body["adminPasswordConfigured"])

        update_settings_response, update_settings_body = self.request(
            "PUT",
            "/admin/api/settings",
            {"apiKey": "sk-python", "defaultProvider": "grok", "adminPassword": "newpass"},
            headers=auth,
        )
        self.assertEqual(update_settings_response.status, 200)
        self.assertEqual(update_settings_body["apiKey"], "sk-python")
        self.assertEqual(update_settings_body["defaultProvider"], "grok")
        self.assertTrue(update_settings_body["adminPasswordConfigured"])

        cursor_response, cursor_body = self.request(
            "PUT",
            "/admin/api/providers/cursor/config",
            {"config": {"apiUrl": "https://cursor.test/chat", "cookie": "cursor-cookie"}},
            headers=auth,
        )
        self.assertEqual(cursor_response.status, 200)
        self.assertEqual(cursor_body["config"]["apiUrl"], "https://cursor.test/chat")
        self.assertEqual(cursor_body["config"]["cookie"], "cursor-cookie")

        kiro_response, kiro_body = self.request(
            "PUT",
            "/admin/api/providers/kiro/accounts",
            {"accounts": [
                {"name": "Main", "accessToken": "ak-1", "machineId": "machine-1", "active": True},
                {"name": "Backup", "accessToken": "ak-2", "machineId": "machine-2", "active": True},
            ]},
            headers=auth,
        )
        self.assertEqual(kiro_response.status, 200)
        self.assertEqual(len(kiro_body["accounts"]), 2)
        self.assertTrue(kiro_body["accounts"][0]["active"])
        self.assertFalse(kiro_body["accounts"][1]["active"])
        self.assertTrue(kiro_body["accounts"][0]["id"])

        grok_response, grok_body = self.request(
            "PUT",
            "/admin/api/providers/grok/tokens",
            {"tokens": [
                {"name": "Primary", "cookieToken": "gt-1", "active": False},
                {"name": "Secondary", "cookieToken": "gt-2", "active": True},
            ]},
            headers=auth,
        )
        self.assertEqual(grok_response.status, 200)
        self.assertEqual(len(grok_body["tokens"]), 2)
        self.assertFalse(grok_body["tokens"][0]["active"])
        self.assertTrue(grok_body["tokens"][1]["active"])

        orchids_response, orchids_body = self.request(
            "PUT",
            "/admin/api/providers/orchids/config",
            {"config": {"clientCookie": "orchids-cookie", "projectId": "project-1", "agentMode": "claude-sonnet-4.5"}},
            headers=auth,
        )
        self.assertEqual(orchids_response.status, 200)
        self.assertEqual(orchids_body["config"]["clientCookie"], "orchids-cookie")

        status_response, status_body = self.request("GET", "/admin/api/status", headers=auth)
        self.assertEqual(status_response.status, 200)
        self.assertEqual(status_body["project"], "any2api-python")
        self.assertEqual(status_body["settings"]["defaultProvider"], "grok")
        self.assertTrue(status_body["providers"]["cursor"]["configured"])
        self.assertEqual(status_body["providers"]["cursor"]["active"], "default")
        self.assertEqual(status_body["providers"]["kiro"]["count"], 2)
        self.assertTrue(status_body["providers"]["kiro"]["configured"])
        self.assertEqual(status_body["providers"]["kiro"]["active"], kiro_body["accounts"][0]["id"])
        self.assertEqual(status_body["providers"]["grok"]["count"], 2)
        self.assertEqual(status_body["providers"]["grok"]["active"], grok_body["tokens"][1]["id"])
        self.assertTrue(status_body["providers"]["orchids"]["configured"])

        self.restart_server()

        relogin_bad, _ = self.request("POST", "/api/admin/auth/login", {"password": "changeme"})
        self.assertEqual(relogin_bad.status, 401)
        persisted_token = self.login("newpass")
        persisted_auth = {"Authorization": f"Bearer {persisted_token}"}

        persisted_settings_response, persisted_settings_body = self.request("GET", "/admin/api/settings", headers=persisted_auth)
        self.assertEqual(persisted_settings_response.status, 200)
        self.assertEqual(persisted_settings_body["apiKey"], "sk-python")
        self.assertEqual(persisted_settings_body["defaultProvider"], "grok")

        persisted_kiro_response, persisted_kiro_body = self.request("GET", "/admin/api/providers/kiro/accounts", headers=persisted_auth)
        self.assertEqual(persisted_kiro_response.status, 200)
        self.assertEqual(len(persisted_kiro_body["accounts"]), 2)

        grok_stream = b'{"result":{"response":{"token":"live grok"}}}\n'
        with patch("providers.grok.provider.urlopen", return_value=FakeResponse(grok_stream)):
            models_response, models_body = self.request("POST", "/v1/chat/completions", {"messages": [{"role": "user", "content": "hi"}]})
        self.assertEqual(models_response.status, 200)
        self.assertEqual(models_response.getheader("X-Newplatform2API-Provider"), "grok")
        self.assertEqual(models_body["choices"][0]["message"]["content"], "live grok")
        self.assertTrue(models_body["id"].startswith("chatcmpl_"))
        self.assertNotIn("skeleton", models_body["choices"][0]["message"]["content"].lower())


if __name__ == "__main__":
    unittest.main()