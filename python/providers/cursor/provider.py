from gateway_types import ModelInfo, ProviderCapabilities

from ..common import SimpleProvider


class CursorProvider(SimpleProvider):
    def __init__(self) -> None:
        super().__init__(
            "cursor",
            ProviderCapabilities(openai_compatible=True, tools=True),
            [ModelInfo("cursor", "claude-sonnet-4.6", "anthropic/claude-sonnet-4.6", "cursor")],
            "cookie + x-is-human + fingerprint",
            "https://cursor.com/api/chat",
            "cursor web reverse flow",
        )