use std::env;
use std::time::Duration;

use reqwest::blocking::Client;
use serde_json::{json, Value};

use crate::admin_store::GrokToken;
use crate::providers::common::{
    env_int, normalize_incremental_chunk, normalize_messages, pick_active_grok_token,
    random_hex, trim_cookie_value, DEFAULT_MAX_INPUT_LENGTH,
};
use crate::registry::Provider;
use crate::types::{ModelInfo, ProviderCapabilities, UnifiedRequest};

const DEFAULT_GROK_API_URL: &str = "https://grok.com/rest/app-chat/conversations/new";
const DEFAULT_GROK_USER_AGENT: &str = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36";
const DEFAULT_GROK_ORIGIN: &str = "https://grok.com";
const DEFAULT_GROK_REFERER: &str = "https://grok.com/";
const DEFAULT_TIMEOUT_SECONDS: u64 = 60;

struct GrokStreamFilter {
    tool_card_open: bool,
    buffer: String,
}

impl GrokStreamFilter {
    fn new() -> Self {
        Self {
            tool_card_open: false,
            buffer: String::new(),
        }
    }

    fn filter(&mut self, token: &str) -> String {
        if token.is_empty() {
            return String::new();
        }
        let start_tag = "<xai:tool_usage_card";
        let end_tag = "</xai:tool_usage_card>";
        let mut output = String::new();
        let mut remaining = token;
        while !remaining.is_empty() {
            if self.tool_card_open {
                let Some(end_index) = remaining.find(end_tag) else {
                    self.buffer.push_str(remaining);
                    return output;
                };
                let end_pos = end_index + end_tag.len();
                self.buffer.push_str(&remaining[..end_pos]);
                let summary = summarize_grok_tool_card(&self.buffer);
                if !summary.is_empty() {
                    output.push_str(&summary);
                    if !summary.ends_with('\n') {
                        output.push('\n');
                    }
                }
                self.buffer.clear();
                self.tool_card_open = false;
                remaining = &remaining[end_pos..];
                continue;
            }
            let Some(start_index) = remaining.find(start_tag) else {
                output.push_str(remaining);
                break;
            };
            if start_index > 0 {
                output.push_str(&remaining[..start_index]);
            }
            let Some(end_index) = remaining[start_index..].find(end_tag) else {
                self.tool_card_open = true;
                self.buffer.push_str(&remaining[start_index..]);
                break;
            };
            let end_pos = start_index + end_index + end_tag.len();
            let summary = summarize_grok_tool_card(&remaining[start_index..end_pos]);
            if !summary.is_empty() {
                output.push_str(&summary);
                if !summary.ends_with('\n') {
                    output.push('\n');
                }
            }
            remaining = &remaining[end_pos..];
        }
        output
    }
}

pub struct GrokProvider {
    tokens: Vec<GrokToken>,
    client: Client,
}

impl GrokProvider {
    pub fn new(tokens: Vec<GrokToken>) -> Self {
        let timeout = Duration::from_secs(env_int(
            "NEWPLATFORM2API_GROK_TIMEOUT",
            DEFAULT_TIMEOUT_SECONDS,
        ));
        let client = Client::builder()
            .timeout(timeout)
            .build()
            .expect("build grok reqwest client");
        Self { tokens, client }
    }

    fn token(&self) -> Result<GrokToken, String> {
        pick_active_grok_token(&self.tokens)
            .or_else(Self::env_token)
            .ok_or_else(|| "grok cookie token is not configured".to_string())
    }

    fn env_token() -> Option<GrokToken> {
        let cookie_token = env::var("NEWPLATFORM2API_GROK_COOKIE_TOKEN").ok()?;
        let cookie_token = cookie_token.trim().to_string();
        if cookie_token.is_empty() {
            return None;
        }
        Some(GrokToken {
            id: "grok-env".to_string(),
            name: "Env Grok Token".to_string(),
            cookie_token,
            active: true,
        })
    }

    fn api_url(&self) -> String {
        let configured = env::var("NEWPLATFORM2API_GROK_API_URL")
            .unwrap_or_else(|_| DEFAULT_GROK_API_URL.to_string());
        let trimmed = configured.trim();
        if trimmed.is_empty() {
            DEFAULT_GROK_API_URL.to_string()
        } else {
            trimmed.to_string()
        }
    }

    fn headers(&self, cookie_token: &str) -> Vec<(&'static str, String)> {
        vec![
            ("Accept", "*/*".to_string()),
            ("Content-Type", "application/json".to_string()),
            ("Cookie", build_grok_cookie_header(cookie_token)),
            (
                "Origin",
                env::var("NEWPLATFORM2API_GROK_ORIGIN")
                    .unwrap_or_else(|_| DEFAULT_GROK_ORIGIN.to_string())
                    .trim()
                    .to_string(),
            ),
            (
                "Referer",
                env::var("NEWPLATFORM2API_GROK_REFERER")
                    .unwrap_or_else(|_| DEFAULT_GROK_REFERER.to_string())
                    .trim()
                    .to_string(),
            ),
            (
                "User-Agent",
                env::var("NEWPLATFORM2API_GROK_USER_AGENT")
                    .unwrap_or_else(|_| DEFAULT_GROK_USER_AGENT.to_string())
                    .trim()
                    .to_string(),
            ),
            ("X-Statsig-Id", random_hex(16)),
            ("X-XAI-Request-Id", random_hex(32)),
            ("X-Requested-With", "XMLHttpRequest".to_string()),
        ]
    }

    fn build_payload(&self, req: &UnifiedRequest) -> Value {
        let model = map_grok_model(&req.model);
        json!({
            "deviceEnvInfo": {
                "darkModeEnabled": false,
                "devicePixelRatio": 2,
                "screenWidth": 2056,
                "screenHeight": 1329,
                "viewportWidth": 2056,
                "viewportHeight": 1083,
            },
            "disableMemory": false,
            "disableSearch": false,
            "disableSelfHarmShortCircuit": false,
            "disableTextFollowUps": false,
            "enableImageGeneration": true,
            "enableImageStreaming": true,
            "enableSideBySide": true,
            "fileAttachments": [],
            "forceConcise": false,
            "forceSideBySide": false,
            "imageAttachments": [],
            "imageGenerationCount": 2,
            "isAsyncChat": false,
            "isReasoning": false,
            "message": self.flatten_messages(req),
            "modelName": model,
            "responseMetadata": {
                "requestModelDetails": {"modelId": map_grok_model(&req.model)}
            },
            "returnImageBytes": false,
            "returnRawGrokInXaiRequest": false,
            "sendFinalMetadata": true,
            "temporary": false,
            "toolOverrides": {},
        })
    }

    fn flatten_messages(&self, req: &UnifiedRequest) -> String {
        let normalized = normalize_messages(req, DEFAULT_MAX_INPUT_LENGTH);
        let mut parts = Vec::new();
        for message in normalized {
            let text = super::super::common::content_text(&message.content);
            if text.is_empty() {
                continue;
            }
            let role = message.role.trim().to_lowercase();
            parts.push((if role.is_empty() { "user".to_string() } else { role }, text));
        }
        if parts.is_empty() {
            return ".".to_string();
        }
        let mut last_user_index = None;
        for (index, (role, _)) in parts.iter().enumerate().rev() {
            if role == "user" {
                last_user_index = Some(index);
                break;
            }
        }
        let last_user_index = last_user_index.unwrap_or(parts.len().saturating_sub(1));
        parts
            .iter()
            .enumerate()
            .map(|(index, (role, text))| {
                if index == last_user_index {
                    text.clone()
                } else {
                    format!("{role}: {text}")
                }
            })
            .collect::<Vec<_>>()
            .join("\n\n")
    }

    fn collect_text(&self, raw: &str) -> String {
        let mut filter = GrokStreamFilter::new();
        let mut last_message = String::new();
        let mut token_seen = false;
        let mut parts = Vec::new();
        for line in raw.lines() {
            let line = line.trim();
            if line.is_empty() {
                continue;
            }
            let Ok(payload) = serde_json::from_str::<Value>(line) else {
                continue;
            };
            let Some(response) = nested_value(&payload, &["result", "response"]) else {
                continue;
            };
            if let Some(token) = response.get("token").and_then(Value::as_str) {
                if !token.is_empty() {
                    token_seen = true;
                    let filtered = strip_grok_artifacts(&filter.filter(token));
                    if !filtered.is_empty() {
                        parts.push(filtered);
                    }
                    continue;
                }
            }
            if token_seen {
                continue;
            }
            let Some(message) = response
                .get("modelResponse")
                .and_then(Value::as_object)
                .and_then(|item| item.get("message"))
                .and_then(Value::as_str)
            else {
                continue;
            };
            if message.is_empty() {
                continue;
            }
            let filtered = strip_grok_artifacts(message);
            let delta = normalize_incremental_chunk(&filtered, &last_message);
            if !delta.is_empty() {
                last_message = filtered;
                parts.push(delta);
            }
        }
        parts.join("").trim().to_string()
    }
}

impl Provider for GrokProvider {
    fn id(&self) -> &'static str {
        "grok"
    }

    fn capabilities(&self) -> ProviderCapabilities {
        ProviderCapabilities {
            openai_compatible: true,
            anthropic_compatible: false,
            tools: true,
            images: true,
            multi_account: true,
        }
    }

    fn models(&self) -> Vec<ModelInfo> {
        vec![ModelInfo {
            provider: "grok",
            public_model: "grok-4",
            upstream_model: "grok-4",
            owned_by: "xai",
        }]
    }

    fn build_upstream_preview(&self, req: &UnifiedRequest) -> String {
        format!(
            "url={} auth=sso-cookie protocol={} model={}",
            self.api_url(),
            req.protocol,
            map_grok_model(&req.model)
        )
    }

    fn generate_reply(&self, req: &UnifiedRequest) -> Result<String, String> {
        let token = self.token()?;
        let cookie_token = token.cookie_token.trim();
        if cookie_token.is_empty() {
            return Err("grok cookie token is not configured".to_string());
        }
        let payload = self.build_payload(req);
        let mut request = self
            .client
            .post(self.api_url())
            .header("Content-Type", "application/json")
            .body(payload.to_string());
        for (key, value) in self.headers(cookie_token) {
            request = request.header(key, value);
        }
        let response = request
            .send()
            .map_err(|err| format!("grok upstream request failed: {err}"))?;
        let status = response.status();
        if !status.is_success() {
            let body = response.text().unwrap_or_default();
            return Err(format!(
                "grok upstream error: status={} body={}",
                status.as_u16(),
                body.trim()
            ));
        }
        let body = response
            .text()
            .map_err(|err| format!("read grok upstream body: {err}"))?;
        Ok(self.collect_text(&body))
    }
}

fn map_grok_model(model: &str) -> String {
    let trimmed = model.trim();
    if trimmed.is_empty() {
        "grok-4".to_string()
    } else {
        trimmed.to_string()
    }
}

fn build_grok_cookie_header(token: &str) -> String {
    let trimmed = token.trim();
    if trimmed.is_empty() {
        return String::new();
    }
    if trimmed.contains(';') {
        return trimmed.to_string();
    }
    let trimmed = trim_cookie_value(trimmed, "sso=");
    format!("sso={trimmed}; sso-rw={trimmed}")
}

fn nested_value<'a>(root: &'a Value, keys: &[&str]) -> Option<&'a Value> {
    let mut current = root;
    for key in keys {
        current = current.get(*key)?;
    }
    Some(current)
}

fn summarize_grok_tool_card(raw: &str) -> String {
    let name = extract_tag_text(raw, "<xai:tool_name>", "</xai:tool_name>");
    let args = extract_tag_text(raw, "<xai:tool_args>", "</xai:tool_args>");
    if name.is_empty() && args.is_empty() {
        return String::new();
    }
    if args.is_empty() {
        return format!("[{name}]");
    }
    format!("[{name}] {args}")
}

fn extract_tag_text(raw: &str, start_tag: &str, end_tag: &str) -> String {
    let Some(start) = raw.find(start_tag) else {
        return String::new();
    };
    let content_start = start + start_tag.len();
    let Some(relative_end) = raw[content_start..].find(end_tag) else {
        return String::new();
    };
    let text = &raw[content_start..content_start + relative_end];
    strip_cdata(text).trim().to_string()
}

fn strip_cdata(text: &str) -> String {
    let trimmed = text.trim();
    if let Some(rest) = trimmed.strip_prefix("<![CDATA[") {
        if let Some(end) = rest.strip_suffix("]]>") {
            return end.to_string();
        }
    }
    trimmed.to_string()
}

fn strip_wrapped_sections(text: &str, start_tag: &str, end_tag: &str) -> String {
    let mut remaining = text;
    let mut out = String::new();
    while let Some(start) = remaining.find(start_tag) {
        out.push_str(&remaining[..start]);
        let tail = &remaining[start + start_tag.len()..];
        if let Some(end) = tail.find(end_tag) {
            remaining = &tail[end + end_tag.len()..];
        } else {
            remaining = "";
            break;
        }
    }
    out.push_str(remaining);
    out
}

fn remove_xai_tags(text: &str) -> String {
    let mut out = String::new();
    let mut remaining = text;
    while let Some(start) = remaining.find('<') {
        out.push_str(&remaining[..start]);
        let tail = &remaining[start..];
        let Some(end) = tail.find('>') else {
            out.push_str(tail);
            return out;
        };
        let tag = &tail[1..end].trim();
        if !tag.starts_with("xai:") && !tag.starts_with("/xai:") {
            out.push_str(&tail[..=end]);
        }
        remaining = &tail[end + 1..];
    }
    out.push_str(remaining);
    out
}

fn replace_tool_cards(text: &str) -> String {
    let start_tag = "<xai:tool_usage_card";
    let end_tag = "</xai:tool_usage_card>";
    let mut remaining = text;
    let mut out = String::new();
    while let Some(start) = remaining.find(start_tag) {
        out.push_str(&remaining[..start]);
        let tail = &remaining[start..];
        let Some(end) = tail.find(end_tag) else {
            break;
        };
        let end_pos = end + end_tag.len();
        out.push_str(&summarize_grok_tool_card(&tail[..end_pos]));
        remaining = &tail[end_pos..];
    }
    out.push_str(remaining);
    out
}

fn strip_grok_artifacts(text: &str) -> String {
    if text.is_empty() {
        return String::new();
    }
    let cleaned = replace_tool_cards(text);
    let cleaned = strip_wrapped_sections(&cleaned, "<rolloutId>", "</rolloutId>");
    remove_xai_tags(&cleaned)
}