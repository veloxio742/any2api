from dataclasses import dataclass, field
from typing import Any


@dataclass
class ProviderCapabilities:
    openai_compatible: bool = False
    anthropic_compatible: bool = False
    tools: bool = False
    images: bool = False
    multi_account: bool = False


@dataclass
class ModelInfo:
    provider: str
    public_model: str
    upstream_model: str
    owned_by: str


@dataclass
class UnifiedRequest:
    provider_hint: str = ""
    protocol: str = "openai"
    model: str = ""
    messages: list[dict[str, Any]] = field(default_factory=list)
    system: Any = None
    tools: list[dict[str, Any]] = field(default_factory=list)
    stream: bool = False
