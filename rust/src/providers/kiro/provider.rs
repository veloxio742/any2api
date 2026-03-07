use std::env;
use std::time::Duration;

use reqwest::blocking::Client;
use serde_json::{json, Value};

use crate::admin_store::KiroAccount;
use crate::providers::common::{
    env_int, generate_machine_id, normalize_incremental_chunk, normalize_messages,
    pick_active_kiro_account, random_hex, split_system_messages, text_value,
    DEFAULT_MAX_INPUT_LENGTH,
};
use crate::registry::Provider;
use crate::types::{Message, ModelInfo, ProviderCapabilities, UnifiedRequest};

const DEFAULT_KIRO_CODEWHISPERER_URL: &str =
    "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse";
const DEFAULT_KIRO_AMAZONQ_URL: &str =
    "https://q.us-east-1.amazonaws.com/generateAssistantResponse";
const DEFAULT_TIMEOUT_SECONDS: u64 = 60;
const KIRO_VERSION: &str = "0.7.45";

struct KiroEndpoint {
    name: &'static str,
    url: String,
    origin: &'static str,
    amz_target: &'static str,
}

pub struct KiroProvider {
    accounts: Vec<KiroAccount>,
    client: Client,
}

impl KiroProvider {
    pub fn new(accounts: Vec<KiroAccount>) -> Self {
        let timeout = Duration::from_secs(env_int(
            "NEWPLATFORM2API_KIRO_TIMEOUT",
            DEFAULT_TIMEOUT_SECONDS,
        ));
        let client = Client::builder()
            .timeout(timeout)
            .build()
            .expect("build kiro reqwest client");
        Self { accounts, client }
    }

    fn account(&self) -> Result<KiroAccount, String> {
        pick_active_kiro_account(&self.accounts)
            .or_else(Self::env_account)
            .ok_or_else(|| "kiro access token is not configured".to_string())
    }

    fn env_account() -> Option<KiroAccount> {
        let access_token = env::var("NEWPLATFORM2API_KIRO_ACCESS_TOKEN").ok()?;
        let access_token = access_token.trim().to_string();
        if access_token.is_empty() {
            return None;
        }
        Some(KiroAccount {
            id: "kiro-env".to_string(),
            name: "Env Kiro Account".to_string(),
            access_token,
            machine_id: env::var("NEWPLATFORM2API_KIRO_MACHINE_ID")
                .unwrap_or_default()
                .trim()
                .to_string(),
            preferred_endpoint: env::var("NEWPLATFORM2API_KIRO_PREFERRED_ENDPOINT")
                .unwrap_or_default()
                .trim()
                .to_lowercase(),
            active: true,
        })
    }

    fn sorted_endpoints(&self, account: &KiroAccount) -> Vec<KiroEndpoint> {
        let codewhisperer = KiroEndpoint {
            name: "codewhisperer",
            url: env::var("NEWPLATFORM2API_KIRO_CODEWHISPERER_URL")
                .unwrap_or_else(|_| DEFAULT_KIRO_CODEWHISPERER_URL.to_string())
                .trim()
                .to_string(),
            origin: "AI_EDITOR",
            amz_target: "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
        };
        let amazonq = KiroEndpoint {
            name: "amazonq",
            url: env::var("NEWPLATFORM2API_KIRO_AMAZONQ_URL")
                .unwrap_or_else(|_| DEFAULT_KIRO_AMAZONQ_URL.to_string())
                .trim()
                .to_string(),
            origin: "CLI",
            amz_target: "AmazonQDeveloperStreamingService.SendMessage",
        };
        match account.preferred_endpoint.trim().to_lowercase().as_str() {
            "amazonq" => vec![amazonq, codewhisperer],
            _ => vec![codewhisperer, amazonq],
        }
    }

    fn user_agents(&self, machine_id: &str) -> (String, String) {
        if machine_id.trim().is_empty() {
            return (
                format!(
                    "aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.21.1 api/codewhispererstreaming#1.0.27 m/E KiroIDE-{KIRO_VERSION}"
                ),
                format!("aws-sdk-js/1.0.27 KiroIDE {KIRO_VERSION}"),
            );
        }
        (
            format!(
                "aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.21.1 api/codewhispererstreaming#1.0.27 m/E KiroIDE-{KIRO_VERSION}-{machine_id}"
            ),
            format!("aws-sdk-js/1.0.27 KiroIDE {KIRO_VERSION} {machine_id}"),
        )
    }

    fn build_request(&self, req: &UnifiedRequest, origin: &str) -> Value {
        let normalized = normalize_messages(req, DEFAULT_MAX_INPUT_LENGTH);
        let model_id = map_kiro_model(&req.model);
        let (system_prompt, mut non_system) = split_system_messages(&normalized);
        if non_system.is_empty() {
            non_system.push(Message {
                role: "user".to_string(),
                content: Value::String(".".to_string()),
            });
        }

        let mut history = Vec::new();
        let mut current_content = String::new();
        let mut system_merged = false;
        for (index, message) in non_system.iter().enumerate() {
            let mut text = text_value(&message.content).trim().to_string();
            let role = message.role.trim().to_lowercase();
            let is_last = index + 1 == non_system.len();
            if role == "assistant" {
                if !text.is_empty() {
                    history.push(json!({
                        "assistantResponseMessage": {"content": text}
                    }));
                }
                continue;
            }
            if role != "user" && !role.is_empty() && !text.is_empty() {
                text = format!("{role}: {text}");
            }
            if !system_merged && !system_prompt.is_empty() {
                text = if text.is_empty() {
                    system_prompt.clone()
                } else {
                    format!("{system_prompt}\n\n{text}")
                };
                system_merged = true;
            }
            if text.is_empty() {
                text = ".".to_string();
            }
            let entry = json!({
                "content": text,
                "modelId": model_id,
                "origin": origin,
            });
            if is_last {
                current_content = entry
                    .get("content")
                    .and_then(Value::as_str)
                    .unwrap_or(".")
                    .to_string();
            } else {
                history.push(json!({"userInputMessage": entry}));
            }
        }
        if current_content.is_empty() {
            current_content = ".".to_string();
            if !system_merged && !system_prompt.is_empty() {
                current_content = format!("{system_prompt}\n\n{current_content}");
            }
        }

        json!({
            "conversationState": {
                "chatTriggerType": "MANUAL",
                "conversationId": random_hex(32),
                "currentMessage": {
                    "userInputMessage": {
                        "content": current_content,
                        "modelId": model_id,
                        "origin": origin,
                    }
                },
                "history": history,
            }
        })
    }

    fn collect_text(&self, raw: &[u8]) -> String {
        let mut offset = 0_usize;
        let mut last_assistant = String::new();
        let mut last_reasoning = String::new();
        let mut parts = Vec::new();
        while offset + 12 <= raw.len() {
            let prelude = &raw[offset..offset + 12];
            let total_length = u32::from_be_bytes([prelude[0], prelude[1], prelude[2], prelude[3]])
                as usize;
            let headers_length =
                u32::from_be_bytes([prelude[4], prelude[5], prelude[6], prelude[7]]) as usize;
            if total_length < 16 || offset + total_length > raw.len() {
                break;
            }
            let message = &raw[offset + 12..offset + total_length];
            offset += total_length;
            if headers_length > message.len().saturating_sub(4) {
                continue;
            }
            let event_type = extract_kiro_event_type(&message[..headers_length]);
            let payload_bytes = &message[headers_length..message.len().saturating_sub(4)];
            if payload_bytes.is_empty() {
                continue;
            }
            let Ok(event) = serde_json::from_slice::<Value>(payload_bytes) else {
                continue;
            };
            let mut delta = String::new();
            if event_type == "assistantResponseEvent" {
                let content = event
                    .get("content")
                    .map(text_value)
                    .unwrap_or_default();
                delta = normalize_incremental_chunk(&content, &last_assistant);
                if !delta.is_empty() {
                    last_assistant = content;
                }
            } else if event_type == "reasoningContentEvent" {
                let reasoning = event.get("text").map(text_value).unwrap_or_default();
                delta = normalize_incremental_chunk(&reasoning, &last_reasoning);
                if !delta.is_empty() {
                    last_reasoning = reasoning;
                }
            }
            if !delta.is_empty() {
                parts.push(delta);
            }
        }
        parts.join("").trim().to_string()
    }
}

impl Provider for KiroProvider {
    fn id(&self) -> &'static str {
        "kiro"
    }

    fn capabilities(&self) -> ProviderCapabilities {
        ProviderCapabilities {
            openai_compatible: true,
            anthropic_compatible: true,
            tools: true,
            images: true,
            multi_account: true,
        }
    }

    fn models(&self) -> Vec<ModelInfo> {
        vec![ModelInfo {
            provider: "kiro",
            public_model: "claude-sonnet-4.6",
            upstream_model: "claude-sonnet-4.6",
            owned_by: "amazonq/kiro",
        }]
    }

    fn build_upstream_preview(&self, req: &UnifiedRequest) -> String {
        match self.account() {
            Ok(account) => {
                let endpoint = self
                    .sorted_endpoints(&account)
                    .into_iter()
                    .next()
                    .map(|item| item.url)
                    .unwrap_or_else(|| DEFAULT_KIRO_CODEWHISPERER_URL.to_string());
                format!(
                    "url={endpoint} auth=bearer+x-amz-user-agent protocol={} model={}",
                    req.protocol,
                    map_kiro_model(&req.model)
                )
            }
            Err(err) => format!("kiro unavailable: {err}"),
        }
    }

    fn generate_reply(&self, req: &UnifiedRequest) -> Result<String, String> {
        let account = self.account()?;
        let access_token = account.access_token.trim();
        if access_token.is_empty() {
            return Err("kiro access token is not configured".to_string());
        }
        let machine_id = if account.machine_id.trim().is_empty() {
            generate_machine_id()
        } else {
            account.machine_id.trim().to_string()
        };
        let (user_agent, amz_user_agent) = self.user_agents(&machine_id);
        let mut last_error = None;
        for endpoint in self.sorted_endpoints(&account) {
            let payload = self.build_request(req, endpoint.origin);
            let response = self
                .client
                .post(&endpoint.url)
                .header("Content-Type", "application/json")
                .header("Accept", "*/*")
                .header("Authorization", format!("Bearer {access_token}"))
                .header("X-Amz-Target", endpoint.amz_target)
                .header("User-Agent", &user_agent)
                .header("X-Amz-User-Agent", &amz_user_agent)
                .header("x-amzn-kiro-agent-mode", "vibe")
                .header("x-amzn-codewhisperer-optout", "true")
                .header("Amz-Sdk-Request", "attempt=1; max=2")
                .header("Amz-Sdk-Invocation-Id", random_hex(32))
                .body(payload.to_string())
                .send()
                .map_err(|err| format!("kiro upstream request failed: {err}"));
            let response = match response {
                Ok(value) => value,
                Err(err) => {
                    last_error = Some(err);
                    continue;
                }
            };
            let status = response.status();
            if status.as_u16() == 429 {
                last_error = Some(format!("kiro endpoint {} returned 429", endpoint.name));
                continue;
            }
            if !status.is_success() {
                let body = response.text().unwrap_or_default();
                let err = format!(
                    "kiro upstream error: status={} body={}",
                    status.as_u16(),
                    body.trim()
                );
                if status.as_u16() == 401 || status.as_u16() == 403 {
                    return Err(err);
                }
                last_error = Some(err);
                continue;
            }
            let body = response
                .bytes()
                .map_err(|err| format!("read kiro upstream body: {err}"))?;
            return Ok(self.collect_text(body.as_ref()));
        }
        Err(last_error.unwrap_or_else(|| "kiro upstream request failed".to_string()))
    }
}

fn map_kiro_model(model: &str) -> String {
    let lower = model.trim().to_lowercase();
    if lower.is_empty()
        || lower.contains("claude-sonnet-4.6")
        || lower.contains("claude-sonnet-4-6")
    {
        return "claude-sonnet-4.6".to_string();
    }
    if lower.contains("claude-sonnet-4.5")
        || lower.contains("claude-sonnet-4-5")
        || lower.contains("claude-3-5-sonnet")
        || lower.contains("gpt-4o")
        || lower.contains("gpt-4")
    {
        return "claude-sonnet-4.5".to_string();
    }
    if lower.starts_with("claude-") {
        return model.trim().to_string();
    }
    "claude-sonnet-4.6".to_string()
}

fn extract_kiro_event_type(headers: &[u8]) -> String {
    let mut offset = 0_usize;
    while offset < headers.len() {
        let name_len = headers[offset] as usize;
        offset += 1;
        if offset + name_len > headers.len() {
            break;
        }
        let name = String::from_utf8_lossy(&headers[offset..offset + name_len]).to_string();
        offset += name_len;
        if offset >= headers.len() {
            break;
        }
        let value_type = headers[offset];
        offset += 1;
        if value_type == 7 {
            if offset + 2 > headers.len() {
                break;
            }
            let value_len = ((headers[offset] as usize) << 8) | headers[offset + 1] as usize;
            offset += 2;
            if offset + value_len > headers.len() {
                break;
            }
            let value = String::from_utf8_lossy(&headers[offset..offset + value_len]).to_string();
            offset += value_len;
            if name == ":event-type" {
                return value;
            }
            continue;
        }
        if value_type == 6 {
            if offset + 2 > headers.len() {
                break;
            }
            let value_len = ((headers[offset] as usize) << 8) | headers[offset + 1] as usize;
            offset += 2 + value_len;
            continue;
        }
        let skip = match value_type {
            0 | 1 => 0,
            2 => 1,
            3 => 2,
            4 => 4,
            5 | 8 => 8,
            9 => 16,
            _ => break,
        };
        offset += skip;
    }
    String::new()
}