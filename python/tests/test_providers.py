import json
import unittest
from unittest.mock import patch

from gateway_types import UnifiedRequest
from providers import GrokProvider, KiroProvider, OrchidsProvider


class FakeResponse:
    def __init__(self, body: bytes):
        self._body = body

    def read(self) -> bytes:
        return self._body

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        return False


def _encode_kiro_stream(event_type: str, payload: dict) -> bytes:
    payload_bytes = json.dumps(payload).encode("utf-8")
    name = b":event-type"
    value = event_type.encode("utf-8")
    headers = bytes([len(name)]) + name + bytes([7]) + len(value).to_bytes(2, "big") + value
    total_length = 12 + len(headers) + len(payload_bytes) + 4
    prelude = total_length.to_bytes(4, "big") + len(headers).to_bytes(4, "big") + b"\x00\x00\x00\x00"
    return prelude + headers + payload_bytes + b"\x00\x00\x00\x00"


class ProviderTests(unittest.TestCase):
    def test_kiro_generate_reply_parses_eventstream_and_uses_snapshot_account(self):
        provider = KiroProvider(lambda: {"providers": {"kiroAccounts": [{"accessToken": "test-token", "machineId": "machine-1", "active": True}]}})
        req = UnifiedRequest(messages=[{"role": "system", "content": "follow rules"}, {"role": "user", "content": "hi"}], model="claude-sonnet-4.6")

        def fake_urlopen(request, timeout=0):
            headers = {key.lower(): value for key, value in request.header_items()}
            body = json.loads(request.data.decode("utf-8"))
            self.assertEqual(headers["authorization"], "Bearer test-token")
            self.assertEqual(headers["x-amz-target"], "AmazonCodeWhispererStreamingService.GenerateAssistantResponse")
            self.assertEqual(body["conversationState"]["currentMessage"]["userInputMessage"]["content"], "follow rules\n\nhi")
            return FakeResponse(_encode_kiro_stream("assistantResponseEvent", {"content": "kiro ok"}))

        with patch("providers.kiro.provider.urlopen", side_effect=fake_urlopen):
            self.assertEqual(provider.generate_reply(req), "kiro ok")

    def test_grok_generate_reply_builds_cookie_and_parses_json_stream(self):
        provider = GrokProvider(lambda: {"providers": {"grokTokens": [{"cookieToken": "test-token", "active": True}]}})
        req = UnifiedRequest(messages=[{"role": "system", "content": "be precise"}, {"role": "assistant", "content": "earlier reply"}, {"role": "user", "content": "latest question"}], model="grok-4")
        stream = "\n".join([
            json.dumps({"result": {"response": {"token": "hello "}}}),
            json.dumps({"result": {"response": {"token": "world"}}}),
        ]).encode("utf-8")

        def fake_urlopen(request, timeout=0):
            headers = {key.lower(): value for key, value in request.header_items()}
            body = json.loads(request.data.decode("utf-8"))
            self.assertEqual(headers["cookie"], "sso=test-token; sso-rw=test-token")
            self.assertEqual(body["modelName"], "grok-4")
            self.assertEqual(body["message"], "system: be precise\n\nassistant: earlier reply\n\nlatest question")
            return FakeResponse(stream)

        with patch("providers.grok.provider.urlopen", side_effect=fake_urlopen):
            self.assertEqual(provider.generate_reply(req), "hello world")

    def test_orchids_generate_reply_fetches_clerk_token_and_collects_sse(self):
        provider = OrchidsProvider(lambda: {"providers": {"orchidsConfig": {"clientCookie": "client-cookie", "clientUat": "100", "projectId": "project-1", "agentMode": "claude-sonnet-4.5"}}})
        req = UnifiedRequest(messages=[{"role": "system", "content": "be precise"}, {"role": "user", "content": "write code"}], model="claude-sonnet-4.5")
        seen_urls: list[str] = []

        def fake_urlopen(request, timeout=0):
            seen_urls.append(request.full_url)
            headers = {key.lower(): value for key, value in request.header_items()}
            if request.get_method() == "GET":
                self.assertIn("/v1/client", request.full_url)
                self.assertEqual(headers["cookie"], "__client=client-cookie")
                return FakeResponse(json.dumps({"response": {"sessions": [{"user": {"id": "user-1", "email_addresses": [{"email_address": "user@example.com"}]}}], "last_active_session_id": "sess-1"}}).encode("utf-8"))
            if "/tokens" in request.full_url:
                self.assertIn("__client=client-cookie", headers["cookie"])
                self.assertIn("__client_uat=100", headers["cookie"])
                return FakeResponse(json.dumps({"jwt": "orchids-jwt"}).encode("utf-8"))
            body = json.loads(request.data.decode("utf-8"))
            self.assertEqual(headers["authorization"], "Bearer orchids-jwt")
            self.assertEqual(body["projectId"], "project-1")
            self.assertIn("<client_system>\nbe precise\n</client_system>", body["prompt"])
            self.assertIn("<user_request>\nwrite code\n</user_request>", body["prompt"])
            return FakeResponse(b'data: {"type":"model","event":{"type":"text-delta","delta":"hello "}}\n\ndata: {"type":"model","event":{"type":"text-delta","delta":"orchids"}}\n\n')

        with patch("providers.orchids.provider.urlopen", side_effect=fake_urlopen):
            self.assertEqual(provider.generate_reply(req), "hello orchids")
        self.assertEqual(len(seen_urls), 3)


if __name__ == "__main__":
    unittest.main()