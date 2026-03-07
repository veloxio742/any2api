use std::env;
use std::process;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

use serde_json::{json, Value};

use crate::admin_store::{GrokToken, KiroAccount};
use crate::types::{Message, UnifiedRequest};

pub const DEFAULT_MAX_INPUT_LENGTH: usize = 200_000;
static ID_COUNTER: AtomicU64 = AtomicU64::new(1);

pub fn string(value: &Value) -> String {
    match value {
        Value::Null => String::new(),
        Value::String(text) => text.trim().to_string(),
        Value::Bool(flag) => flag.to_string(),
        Value::Number(num) => num.to_string(),
        Value::Array(_) | Value::Object(_) => value.to_string(),
    }
}

pub fn text_value(value: &Value) -> String {
    match value {
        Value::Null => String::new(),
        Value::String(text) => text.clone(),
        _ => value.to_string(),
    }
}

pub fn trim_cookie_value(value: &str, prefix: &str) -> String {
    value.trim().trim_start_matches(prefix).to_string()
}

pub fn content_text(content: &Value) -> String {
    match content {
        Value::Null => String::new(),
        Value::String(text) => text.trim().to_string(),
        Value::Array(items) => {
            let mut parts = Vec::new();
            for item in items {
                let Some(obj) = item.as_object() else { continue };
                let block_type = obj.get("type").map(string).unwrap_or_default();
                match block_type.as_str() {
                    "text" => {
                        let text = obj.get("text").map(string).unwrap_or_default();
                        if !text.is_empty() {
                            parts.push(text);
                        }
                    }
                    "image" => {
                        let media_type = obj
                            .get("source")
                            .and_then(Value::as_object)
                            .and_then(|source| source.get("media_type"))
                            .map(string)
                            .unwrap_or_else(|| "unknown".to_string());
                        parts.push(format!("[Image: {media_type}]"));
                    }
                    "tool_use" => {
                        let name = obj.get("name").map(string).unwrap_or_default();
                        let input = obj.get("input").cloned().unwrap_or_else(|| json!({}));
                        parts.push(format!("<tool_use name=\"{name}\">{input}</tool_use>"));
                    }
                    "tool_result" => {
                        let tool_use_id = obj.get("tool_use_id").map(string).unwrap_or_default();
                        let text = tool_result_text(obj.get("content").unwrap_or(&Value::Null));
                        parts.push(format!(
                            "<tool_result tool_use_id=\"{tool_use_id}\">{text}</tool_result>"
                        ));
                    }
                    _ => {}
                }
            }
            parts.join("\n").trim().to_string()
        }
        Value::Object(obj) => obj
            .get("text")
            .map(string)
            .filter(|text| !text.is_empty())
            .unwrap_or_else(|| content.to_string()),
        _ => string(content),
    }
}

fn tool_result_text(content: &Value) -> String {
    match content {
        Value::String(text) => text.trim().to_string(),
        Value::Array(items) => {
            let mut texts = Vec::new();
            for item in items {
                if let Some(obj) = item.as_object() {
                    let text = obj.get("text").map(string).unwrap_or_default();
                    if !text.is_empty() {
                        texts.push(text);
                    }
                }
            }
            if texts.is_empty() {
                content.to_string()
            } else {
                texts.join("\n")
            }
        }
        _ => content.to_string(),
    }
}

pub fn normalize_messages(req: &UnifiedRequest, max_input_length: usize) -> Vec<Message> {
    let mut normalized = Vec::new();
    if let Some(system) = &req.system {
        let text = content_text(system);
        if !text.is_empty() {
            normalized.push(Message {
                role: "system".to_string(),
                content: Value::String(text),
            });
        }
    }
    for message in &req.messages {
        let role = message.role.trim().to_lowercase();
        let text = content_text(&message.content);
        if text.is_empty() {
            continue;
        }
        normalized.push(Message {
            role: if role.is_empty() { "user".to_string() } else { role },
            content: Value::String(text),
        });
    }
    if max_input_length == 0 {
        return normalized;
    }
    let mut kept = Vec::new();
    let mut remaining = max_input_length;
    for message in normalized.iter().rev() {
        let text = content_text(&message.content);
        if text.len() <= remaining {
            remaining -= text.len();
            kept.push(message.clone());
        }
    }
    kept.reverse();
    kept
}

pub fn split_system_messages(messages: &[Message]) -> (String, Vec<Message>) {
    let mut system_parts = Vec::new();
    let mut non_system = Vec::new();
    for message in messages {
        let text = content_text(&message.content);
        if message.role == "system" {
            if !text.is_empty() {
                system_parts.push(text);
            }
            continue;
        }
        non_system.push(Message {
            role: message.role.clone(),
            content: Value::String(text),
        });
    }
    (system_parts.join("\n\n"), non_system)
}

pub fn pick_active_kiro_account(accounts: &[KiroAccount]) -> Option<KiroAccount> {
    accounts
        .iter()
        .find(|item| item.active && !item.access_token.trim().is_empty())
        .cloned()
        .or_else(|| {
            accounts
                .iter()
                .find(|item| !item.access_token.trim().is_empty())
                .cloned()
        })
}

pub fn pick_active_grok_token(tokens: &[GrokToken]) -> Option<GrokToken> {
    tokens
        .iter()
        .find(|item| item.active && !item.cookie_token.trim().is_empty())
        .cloned()
        .or_else(|| {
            tokens
                .iter()
                .find(|item| !item.cookie_token.trim().is_empty())
                .cloned()
        })
}

pub fn normalize_incremental_chunk(chunk: &str, previous: &str) -> String {
    if chunk.is_empty() {
        return String::new();
    }
    if previous.is_empty() {
        return chunk.to_string();
    }
    if chunk == previous || previous.starts_with(chunk) {
        return String::new();
    }
    if let Some(stripped) = chunk.strip_prefix(previous) {
        return stripped.to_string();
    }
    let max_overlap = previous
        .char_indices()
        .filter_map(|(idx, _)| {
            let suffix = &previous[idx..];
            chunk.starts_with(suffix).then_some(previous.len() - idx)
        })
        .max()
        .unwrap_or(0);
    chunk[max_overlap..].to_string()
}

pub fn env_int(name: &str, default: u64) -> u64 {
    env::var(name)
        .ok()
        .and_then(|raw| raw.trim().parse::<u64>().ok())
        .filter(|value| *value > 0)
        .unwrap_or(default)
}

pub fn now_unix_seconds() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs()
}

pub fn random_hex(len: usize) -> String {
    let seed = format!(
        "{:x}{:x}{:x}",
        now_unix_seconds(),
        process::id(),
        ID_COUNTER.fetch_add(1, Ordering::Relaxed)
    );
    if seed.len() >= len {
        seed[..len].to_string()
    } else {
        seed.repeat((len / seed.len()) + 1)[..len].to_string()
    }
}

pub fn generate_machine_id() -> String {
    let raw = random_hex(32);
    format!(
        "{}-{}-{}-{}-{}",
        &raw[0..8],
        &raw[8..12],
        &raw[12..16],
        &raw[16..20],
        &raw[20..32]
    )
}