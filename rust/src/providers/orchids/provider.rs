use std::env;
use std::sync::Mutex;
use std::time::Duration;

use reqwest::blocking::Client;
use serde_json::{json, Value};

use crate::admin_store::OrchidsRuntimeConfig;
use crate::providers::common::{
    env_int, normalize_messages, now_unix_seconds, trim_cookie_value, DEFAULT_MAX_INPUT_LENGTH,
};
use crate::registry::Provider;
use crate::types::{Message, ModelInfo, ProviderCapabilities, UnifiedRequest};

const DEFAULT_TIMEOUT_SECONDS: u64 = 60;
const DEFAULT_ORCHIDS_API_URL: &str = "https://orchids-server.calmstone-6964e08a.westeurope.azurecontainerapps.io/agent/coding-agent";
const DEFAULT_ORCHIDS_CLERK_URL: &str = "https://clerk.orchids.app";
const DEFAULT_ORCHIDS_PROJECT_ID: &str = "280b7bae-cd29-41e4-a0a6-7f603c43b607";
const DEFAULT_ORCHIDS_AGENT_MODE: &str = "claude-opus-4.5";
const ORCHIDS_CLERK_QUERY_SUFFIX: &str = "?__clerk_api_version=2025-11-10&_clerk_js_version=5.117.0";
const ORCHIDS_SYSTEM_PRESET: &str = "你是 AI 编程助手，通过代理服务与用户交互。仅依赖当前工具和历史上下文，保持回复简洁专业。";
const ORCHIDS_TOKEN_TTL_SECONDS: u64 = 50 * 60;

#[derive(Clone)]
struct OrchidsAccount {
    client_cookie: String,
    client_uat: String,
    session_id: String,
    project_id: String,
    user_id: String,
    email: String,
}

#[derive(Default)]
struct TokenCache {
    token: String,
    token_key: String,
    token_until: u64,
}

pub struct OrchidsProvider {
    config: OrchidsRuntimeConfig,
    client: Client,
    token_cache: Mutex<TokenCache>,
}

impl OrchidsProvider {
    pub fn new(config: OrchidsRuntimeConfig) -> Self {
        let timeout = Duration::from_secs(env_int(
            "NEWPLATFORM2API_ORCHIDS_TIMEOUT",
            DEFAULT_TIMEOUT_SECONDS,
        ));
        let client = Client::builder()
            .timeout(timeout)
            .build()
            .expect("build orchids reqwest client");
        Self {
            config,
            client,
            token_cache: Mutex::new(TokenCache::default()),
        }
    }

    fn config_value(&self, current: &str, primary_env: &str, legacy_env: Option<&str>, default: &str) -> String {
        let trimmed = current.trim();
        if !trimmed.is_empty() {
            return trimmed.to_string();
        }
        if let Ok(value) = env::var(primary_env) {
            let value = value.trim();
            if !value.is_empty() {
                return value.to_string();
            }
        }
        if let Some(key) = legacy_env {
            if let Ok(value) = env::var(key) {
                let value = value.trim();
                if !value.is_empty() {
                    return value.to_string();
                }
            }
        }
        default.to_string()
    }

    fn resolved_config(&self) -> OrchidsRuntimeConfig {
        OrchidsRuntimeConfig {
            api_url: self.config_value(
                &self.config.api_url,
                "NEWPLATFORM2API_ORCHIDS_API_URL",
                None,
                DEFAULT_ORCHIDS_API_URL,
            ),
            clerk_url: self
                .config_value(
                    &self.config.clerk_url,
                    "NEWPLATFORM2API_ORCHIDS_CLERK_URL",
                    None,
                    DEFAULT_ORCHIDS_CLERK_URL,
                )
                .trim_end_matches('/')
                .to_string(),
            client_cookie: trim_cookie_value(
                &self.config_value(
                    &self.config.client_cookie,
                    "NEWPLATFORM2API_ORCHIDS_CLIENT_COOKIE",
                    Some("CLIENT_COOKIE"),
                    "",
                ),
                "__client=",
            ),
            client_uat: trim_cookie_value(
                &self.config_value(
                    &self.config.client_uat,
                    "NEWPLATFORM2API_ORCHIDS_CLIENT_UAT",
                    Some("CLIENT_UAT"),
                    "",
                ),
                "__client_uat=",
            ),
            session_id: self.config_value(
                &self.config.session_id,
                "NEWPLATFORM2API_ORCHIDS_SESSION_ID",
                Some("SESSION_ID"),
                "",
            ),
            project_id: self.config_value(
                &self.config.project_id,
                "NEWPLATFORM2API_ORCHIDS_PROJECT_ID",
                Some("PROJECT_ID"),
                DEFAULT_ORCHIDS_PROJECT_ID,
            ),
            user_id: self.config_value(
                &self.config.user_id,
                "NEWPLATFORM2API_ORCHIDS_USER_ID",
                Some("USER_ID"),
                "",
            ),
            email: self.config_value(
                &self.config.email,
                "NEWPLATFORM2API_ORCHIDS_EMAIL",
                Some("EMAIL"),
                "",
            ),
            agent_mode: self.config_value(
                &self.config.agent_mode,
                "NEWPLATFORM2API_ORCHIDS_AGENT_MODE",
                Some("AGENT_MODE"),
                DEFAULT_ORCHIDS_AGENT_MODE,
            ),
        }
    }

    fn resolve_account(&self, config: &OrchidsRuntimeConfig) -> Result<OrchidsAccount, String> {
        if config.client_cookie.trim().is_empty() {
            return Err("orchids client cookie is not configured".to_string());
        }
        let mut account = OrchidsAccount {
            client_cookie: config.client_cookie.trim().to_string(),
            client_uat: if config.client_uat.trim().is_empty() {
                now_unix_seconds().to_string()
            } else {
                config.client_uat.trim().to_string()
            },
            session_id: config.session_id.trim().to_string(),
            project_id: config.project_id.trim().to_string(),
            user_id: config.user_id.trim().to_string(),
            email: config.email.trim().to_string(),
        };
        if !account.session_id.is_empty() && !account.user_id.is_empty() && !account.email.is_empty() {
            return Ok(account);
        }
        let resolved = self.fetch_account_info(config, &account.client_cookie)?;
        if account.session_id.is_empty() {
            account.session_id = resolved.session_id;
        }
        if account.user_id.is_empty() {
            account.user_id = resolved.user_id;
        }
        if account.email.is_empty() {
            account.email = resolved.email;
        }
        if account.session_id.is_empty() || account.user_id.is_empty() || account.email.is_empty() {
            return Err("orchids account identity is incomplete".to_string());
        }
        Ok(account)
    }

    fn fetch_account_info(
        &self,
        config: &OrchidsRuntimeConfig,
        client_cookie: &str,
    ) -> Result<OrchidsAccount, String> {
        let url = format!("{}/v1/client{}", config.clerk_url, ORCHIDS_CLERK_QUERY_SUFFIX);
        let response = self
            .client
            .get(url)
            .header("User-Agent", "Mozilla/5.0")
            .header("Accept-Language", "zh-CN")
            .header("Cookie", format!("__client={client_cookie}"))
            .send()
            .map_err(|err| format!("fetch orchids account info: {err}"))?;
        let status = response.status();
        if !status.is_success() {
            let body = response.text().unwrap_or_default();
            return Err(format!(
                "orchids account info error: status={} body={}",
                status.as_u16(),
                body.trim()
            ));
        }
        let payload_text = response
            .text()
            .map_err(|err| format!("read orchids account info body: {err}"))?;
        let payload: Value = serde_json::from_str(&payload_text)
            .map_err(|err| format!("decode orchids account info: {err}"))?;
        let Some(root) = payload.get("response").and_then(Value::as_object) else {
            return Err("orchids account info missing active session".to_string());
        };
        let sessions = root
            .get("sessions")
            .and_then(Value::as_array)
            .cloned()
            .unwrap_or_default();
        let last_active_session_id = root
            .get("last_active_session_id")
            .and_then(Value::as_str)
            .unwrap_or("")
            .trim()
            .to_string();
        if sessions.is_empty() || last_active_session_id.is_empty() {
            return Err("orchids account info missing active session".to_string());
        }
        let user = sessions[0]
            .get("user")
            .and_then(Value::as_object)
            .ok_or_else(|| "orchids account info missing user identity".to_string())?;
        let user_id = user
            .get("id")
            .and_then(Value::as_str)
            .unwrap_or("")
            .trim()
            .to_string();
        let email = user
            .get("email_addresses")
            .and_then(Value::as_array)
            .and_then(|items| items.first())
            .and_then(Value::as_object)
            .and_then(|item| item.get("email_address"))
            .and_then(Value::as_str)
            .unwrap_or("")
            .trim()
            .to_string();
        if user_id.is_empty() || email.is_empty() {
            return Err("orchids account info missing user identity".to_string());
        }
        Ok(OrchidsAccount {
            client_cookie: client_cookie.to_string(),
            client_uat: String::new(),
            session_id: last_active_session_id,
            project_id: String::new(),
            user_id,
            email,
        })
    }

    fn get_token(&self, config: &OrchidsRuntimeConfig, account: &OrchidsAccount) -> Result<String, String> {
        let cache_key = format!(
            "{}:{}:{}",
            account.session_id, account.client_cookie, account.client_uat
        );
        {
            let cache = self.token_cache.lock().expect("orchids token cache poisoned");
            if !cache.token.is_empty()
                && cache.token_key == cache_key
                && now_unix_seconds() < cache.token_until
            {
                return Ok(cache.token.clone());
            }
        }
        let url = format!(
            "{}/v1/client/sessions/{}/tokens{}",
            config.clerk_url, account.session_id, ORCHIDS_CLERK_QUERY_SUFFIX
        );
        let response = self
            .client
            .post(url)
            .header("Content-Type", "application/x-www-form-urlencoded")
            .header(
                "Cookie",
                format!(
                    "__client={}; __client_uat={}",
                    account.client_cookie, account.client_uat
                ),
            )
            .body("organization_id=")
            .send()
            .map_err(|err| format!("fetch orchids token: {err}"))?;
        let status = response.status();
        if !status.is_success() {
            self.invalidate_token();
            let body = response.text().unwrap_or_default();
            return Err(format!(
                "orchids token request failed: status={} body={}",
                status.as_u16(),
                body.trim()
            ));
        }
        let payload_text = response
            .text()
            .map_err(|err| format!("read orchids token response body: {err}"))?;
        let payload: Value = serde_json::from_str(&payload_text)
            .map_err(|err| format!("decode orchids token response: {err}"))?;
        let token = payload
            .get("jwt")
            .and_then(Value::as_str)
            .unwrap_or("")
            .trim()
            .to_string();
        if token.is_empty() {
            return Err("orchids token response missing jwt".to_string());
        }
        let mut cache = self.token_cache.lock().expect("orchids token cache poisoned");
        cache.token = token.clone();
        cache.token_key = cache_key;
        cache.token_until = now_unix_seconds() + ORCHIDS_TOKEN_TTL_SECONDS;
        Ok(token)
    }

    fn invalidate_token(&self) {
        let mut cache = self.token_cache.lock().expect("orchids token cache poisoned");
        cache.token.clear();
        cache.token_key.clear();
        cache.token_until = 0;
    }

    fn build_agent_request(&self, req: &UnifiedRequest, config: &OrchidsRuntimeConfig, account: &OrchidsAccount) -> Value {
        let mapped_model = map_model(&req.model);
        let agent_mode = if config.agent_mode.trim().is_empty() {
            mapped_model.clone()
        } else {
            config.agent_mode.trim().to_string()
        };
        json!({
            "prompt": build_prompt(&normalize_messages(req, DEFAULT_MAX_INPUT_LENGTH)),
            "chatHistory": [],
            "projectId": account.project_id,
            "currentPage": {},
            "agentMode": agent_mode,
            "mode": "agent",
            "gitRepoUrl": "",
            "email": account.email,
            "chatSessionId": (now_unix_seconds() * 1000) % 90_000_000 + 10_000_000,
            "userId": account.user_id,
            "apiVersion": 2,
            "model": mapped_model,
        })
    }

    fn collect_text(&self, raw: &str) -> String {
        let mut parts = Vec::new();
        for chunk in raw.split("\n\n") {
            for line in chunk.lines() {
                let line = line.trim();
                if !line.starts_with("data: ") {
                    continue;
                }
                let data = line.trim_start_matches("data: ").trim();
                if data.is_empty() {
                    continue;
                }
                let Ok(message) = serde_json::from_str::<Value>(data) else {
                    continue;
                };
                if message.get("type").and_then(Value::as_str) != Some("model") {
                    continue;
                }
                let Some(event) = message.get("event").and_then(Value::as_object) else {
                    continue;
                };
                if event.get("type").and_then(Value::as_str) != Some("text-delta") {
                    continue;
                }
                if let Some(delta) = event.get("delta").and_then(Value::as_str) {
                    if !delta.is_empty() {
                        parts.push(delta.to_string());
                    }
                }
            }
        }
        parts.join("").trim().to_string()
    }
}

impl Provider for OrchidsProvider {
    fn id(&self) -> &'static str {
        "orchids"
    }

    fn capabilities(&self) -> ProviderCapabilities {
        ProviderCapabilities {
            openai_compatible: true,
            anthropic_compatible: true,
            tools: true,
            images: false,
            multi_account: true,
        }
    }

    fn models(&self) -> Vec<ModelInfo> {
        vec![ModelInfo {
            provider: "orchids",
            public_model: "claude-sonnet-4.5",
            upstream_model: "claude-sonnet-4-5",
            owned_by: "orchids",
        }]
    }

    fn build_upstream_preview(&self, req: &UnifiedRequest) -> String {
        let config = self.resolved_config();
        format!(
            "url={} auth=clerk-cookie->jwt protocol={} model={}",
            config.api_url,
            req.protocol,
            map_model(&req.model)
        )
    }

    fn generate_reply(&self, req: &UnifiedRequest) -> Result<String, String> {
        let config = self.resolved_config();
        let account = self.resolve_account(&config)?;
        let token = self.get_token(&config, &account)?;
        let payload = self.build_agent_request(req, &config, &account);
        let response = self
            .client
            .post(&config.api_url)
            .header("Accept", "text/event-stream")
            .header("Authorization", format!("Bearer {token}"))
            .header("Content-Type", "application/json")
            .header("X-Orchids-Api-Version", "2")
            .body(payload.to_string())
            .send()
            .map_err(|err| format!("orchids upstream request failed: {err}"))?;
        let status = response.status();
        if status.as_u16() == 401 {
            self.invalidate_token();
        }
        if !status.is_success() {
            let body = response.text().unwrap_or_default();
            return Err(format!(
                "orchids upstream error: status={} body={}",
                status.as_u16(),
                body.trim()
            ));
        }
        let body = response
            .text()
            .map_err(|err| format!("read orchids upstream body: {err}"))?;
        Ok(self.collect_text(&body))
    }
}

fn map_model(model: &str) -> String {
    let lower = model.trim().to_lowercase();
    if lower.contains("opus") {
        return "claude-opus-4.5".to_string();
    }
    if lower.contains("haiku") {
        return "gemini-3-flash".to_string();
    }
    if lower.is_empty() {
        return "claude-sonnet-4-5".to_string();
    }
    "claude-sonnet-4-5".to_string()
}

fn build_prompt(messages: &[Message]) -> String {
    let mut systems = Vec::new();
    let mut dialogue = Vec::new();
    for message in messages {
        let text = super::super::common::content_text(&message.content);
        if text.is_empty() {
            continue;
        }
        let role = message.role.trim().to_lowercase();
        if role == "system" {
            systems.push(text);
            continue;
        }
        dialogue.push((role, text));
    }
    let mut sections = Vec::new();
    if !systems.is_empty() {
        sections.push(format!(
            "<client_system>\n{}\n</client_system>",
            systems.join("\n\n")
        ));
    }
    sections.push(format!(
        "<proxy_instructions>\n{}\n</proxy_instructions>",
        ORCHIDS_SYSTEM_PRESET
    ));
    let history = format_history(&dialogue);
    if !history.is_empty() {
        sections.push(format!(
            "<conversation_history>\n{}\n</conversation_history>",
            history
        ));
    }
    let current_request = if let Some((role, text)) = dialogue.last() {
        if role == "user" && !text.is_empty() {
            text.clone()
        } else {
            "继续".to_string()
        }
    } else {
        "继续".to_string()
    };
    sections.push(format!(
        "<user_request>\n{}\n</user_request>",
        current_request
    ));
    sections.join("\n\n")
}

fn format_history(messages: &[(String, String)]) -> String {
    let history = if matches!(messages.last(), Some((role, _)) if role == "user") {
        &messages[..messages.len().saturating_sub(1)]
    } else {
        messages
    };
    let mut parts = Vec::new();
    let mut turn_index = 1;
    for (role, content) in history {
        if role != "user" && role != "assistant" {
            continue;
        }
        if content.is_empty() {
            continue;
        }
        parts.push(format!(
            "<turn index=\"{}\" role=\"{}\">\n{}\n</turn>",
            turn_index, role, content
        ));
        turn_index += 1;
    }
    parts.join("\n\n")
}