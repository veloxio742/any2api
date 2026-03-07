use serde_json::Value;

#[derive(Clone, Debug)]
pub struct ProviderCapabilities {
    pub openai_compatible: bool,
    pub anthropic_compatible: bool,
    pub tools: bool,
    pub images: bool,
    pub multi_account: bool,
}

#[derive(Clone, Debug)]
pub struct ModelInfo {
    pub provider: &'static str,
    pub public_model: &'static str,
    pub upstream_model: &'static str,
    pub owned_by: &'static str,
}

#[derive(Clone, Debug)]
pub struct Message {
    pub role: String,
    pub content: Value,
}

#[derive(Clone, Debug)]
pub struct UnifiedRequest {
    pub provider_hint: String,
    pub protocol: &'static str,
    pub model: String,
    pub messages: Vec<Message>,
    pub system: Option<Value>,
    pub stream: bool,
}
