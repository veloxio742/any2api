use std::env;
use std::io::Write;
use std::process::{Command, Stdio};
use std::sync::Mutex;
use std::thread;
use std::time::Duration;

use reqwest::blocking::{Client, Response};
use serde_json::{json, Value};

use super::headers::CursorHeaderGenerator;
use crate::admin_store::CursorRuntimeConfig;
use crate::providers::common::{content_text, random_hex};
use crate::registry::Provider;
use crate::types::{Message, ModelInfo, ProviderCapabilities, UnifiedRequest};

const DEFAULT_CURSOR_API_URL: &str = "https://cursor.com/api/chat";
const DEFAULT_CURSOR_SCRIPT_URL: &str = "https://cursor.com/_next/static/chunks/pages/_app.js";
const DEFAULT_CURSOR_WEBGL_VENDOR: &str = "Google Inc. (Intel)";
const DEFAULT_CURSOR_WEBGL_RENDERER: &str =
    "ANGLE (Intel, Intel(R) UHD Graphics 620 Direct3D11 vs_5_0 ps_5_0, D3D11)";
const DEFAULT_TIMEOUT_SECONDS: u64 = 60;
const DEFAULT_MAX_INPUT_LENGTH: usize = 200_000;
const SCRIPT_CACHE_TTL_SECONDS: u64 = 60;
const MAX_RETRIES: usize = 2;

#[derive(Default)]
struct ScriptCache {
    script: String,
    fetched_at: u64,
}

pub struct CursorProvider {
    config: CursorRuntimeConfig,
    client: Client,
    script_cache: Mutex<ScriptCache>,
    header_generator: Mutex<CursorHeaderGenerator>,
}

impl CursorProvider {
    pub fn new(config: CursorRuntimeConfig) -> Self {
        let timeout = Duration::from_secs(env_u64(
            "NEWPLATFORM2API_CURSOR_TIMEOUT",
            Some("TIMEOUT"),
            DEFAULT_TIMEOUT_SECONDS,
        ));
        let client = Client::builder()
            .timeout(timeout)
            .build()
            .expect("build cursor reqwest client");
        Self {
            config,
            client,
            script_cache: Mutex::new(ScriptCache::default()),
            header_generator: Mutex::new(CursorHeaderGenerator::new()),
        }
    }

    fn config_value(
        &self,
        current: &str,
        primary_env: &str,
        legacy_env: Option<&str>,
        default: &str,
    ) -> String {
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

    fn resolved_config(&self) -> CursorRuntimeConfig {
        CursorRuntimeConfig {
            api_url: self.config_value(
                &self.config.api_url,
                "NEWPLATFORM2API_CURSOR_API_URL",
                None,
                DEFAULT_CURSOR_API_URL,
            ),
            script_url: self.config_value(
                &self.config.script_url,
                "NEWPLATFORM2API_CURSOR_SCRIPT_URL",
                Some("SCRIPT_URL"),
                DEFAULT_CURSOR_SCRIPT_URL,
            ),
            cookie: self.config_value(
                &self.config.cookie,
                "NEWPLATFORM2API_CURSOR_COOKIE",
                None,
                "",
            ),
            x_is_human: self.config_value(
                &self.config.x_is_human,
                "NEWPLATFORM2API_CURSOR_X_IS_HUMAN",
                Some("X_IS_HUMAN"),
                "",
            ),
            user_agent: self.config_value(
                &self.config.user_agent,
                "NEWPLATFORM2API_CURSOR_USER_AGENT",
                Some("USER_AGENT"),
                "",
            ),
            referer: self.config_value(
                &self.config.referer,
                "NEWPLATFORM2API_CURSOR_REFERER",
                Some("REFERER"),
                "",
            ),
            webgl_vendor: self.config_value(
                &self.config.webgl_vendor,
                "NEWPLATFORM2API_CURSOR_UNMASKED_VENDOR_WEBGL",
                Some("UNMASKED_VENDOR_WEBGL"),
                DEFAULT_CURSOR_WEBGL_VENDOR,
            ),
            webgl_renderer: self.config_value(
                &self.config.webgl_renderer,
                "NEWPLATFORM2API_CURSOR_UNMASKED_RENDERER_WEBGL",
                Some("UNMASKED_RENDERER_WEBGL"),
                DEFAULT_CURSOR_WEBGL_RENDERER,
            ),
        }
    }

    fn max_input_length(&self) -> usize {
        env_u64(
            "NEWPLATFORM2API_CURSOR_MAX_INPUT_LENGTH",
            Some("MAX_INPUT_LENGTH"),
            DEFAULT_MAX_INPUT_LENGTH as u64,
        ) as usize
    }

    fn build_payload(&self, req: &UnifiedRequest) -> Value {
        let messages = prepare_cursor_messages(req, self.max_input_length())
            .into_iter()
            .filter_map(|message| {
                let text = content_text(&message.content);
                if text.is_empty() {
                    return None;
                }
                Some(json!({
                    "role": message.role,
                    "parts": [{"type": "text", "text": text}],
                }))
            })
            .collect::<Vec<_>>();
        json!({
            "context": [],
            "model": map_cursor_model(&req.model),
            "id": random_hex(16),
            "messages": messages,
            "trigger": "submit-message",
        })
    }

    fn resolve_x_is_human(&self, config: &CursorRuntimeConfig) -> Result<String, String> {
        let manual = config.x_is_human.trim();
        if !manual.is_empty() {
            return Ok(manual.to_string());
        }
        self.fetch_x_is_human(config)
    }

    fn fetch_x_is_human(&self, config: &CursorRuntimeConfig) -> Result<String, String> {
        let cached = self.cached_script();
        let script = if !cached.0.is_empty()
            && now_unix_seconds().saturating_sub(cached.1) < SCRIPT_CACHE_TTL_SECONDS
        {
            cached.0
        } else {
            match self.fetch_cursor_script(config) {
                Ok(script) => {
                    self.store_script_cache(&script);
                    script
                }
                Err(_) if !cached.0.is_empty() => cached.0,
                Err(_) => return Ok(random_hex(64)),
            }
        };

        let compiled = self.prepare_js(config, &script);
        match run_node_js(&compiled) {
            Ok(value) => {
                let normalized = normalize_js_result(&value);
                if normalized.is_empty() {
                    self.clear_script_cache();
                    Ok(random_hex(64))
                } else {
                    Ok(normalized)
                }
            }
            Err(_) => {
                self.clear_script_cache();
                Ok(random_hex(64))
            }
        }
    }

    fn fetch_cursor_script(&self, config: &CursorRuntimeConfig) -> Result<String, String> {
        let mut request = self.client.get(&config.script_url);
        for (key, value) in self.script_headers(config) {
            request = request.header(key, value);
        }
        let response = request
            .send()
            .map_err(|err| format!("fetch cursor script: {err}"))?;
        let status = response.status();
        if !status.is_success() {
            let body = response.text().unwrap_or_default();
            return Err(format!(
                "cursor script error: status={} body={}",
                status.as_u16(),
                body.trim()
            ));
        }
        response
            .text()
            .map_err(|err| format!("read cursor script body: {err}"))
    }

    fn prepare_js(&self, config: &CursorRuntimeConfig, cursor_js: &str) -> String {
        let profile = self
            .header_generator
            .lock()
            .expect("cursor header generator lock poisoned")
            .profile();
        let user_agent = first_non_empty(&config.user_agent, &profile.user_agent);
        let mut main_script = FALLBACK_CURSOR_MAIN_JS.replace("$$currentScriptSrc$$", &config.script_url);
        main_script = main_script.replace(
            "$$UNMASKED_VENDOR_WEBGL$$",
            first_non_empty(&config.webgl_vendor, DEFAULT_CURSOR_WEBGL_VENDOR),
        );
        main_script = main_script.replace(
            "$$UNMASKED_RENDERER_WEBGL$$",
            first_non_empty(&config.webgl_renderer, DEFAULT_CURSOR_WEBGL_RENDERER),
        );
        main_script = main_script.replace("$$userAgent$$", user_agent);
        main_script = main_script.replace("$$env_jscode$$", FALLBACK_CURSOR_ENV_JS);
        main_script.replace("$$cursor_jscode$$", cursor_js)
    }

    fn chat_headers(&self, config: &CursorRuntimeConfig, x_is_human: &str) -> Vec<(String, String)> {
        let mut headers = self
            .header_generator
            .lock()
            .expect("cursor header generator lock poisoned")
            .chat_headers(x_is_human);
        headers.push((
            "Accept".to_string(),
            "text/event-stream, application/json".to_string(),
        ));
        headers.push(("Origin".to_string(), "https://cursor.com".to_string()));
        if !config.cookie.trim().is_empty() {
            headers.push(("Cookie".to_string(), config.cookie.trim().to_string()));
        }
        if !config.user_agent.trim().is_empty() {
            headers.push((
                "User-Agent".to_string(),
                config.user_agent.trim().to_string(),
            ));
        }
        if !config.referer.trim().is_empty() {
            headers.push(("Referer".to_string(), config.referer.trim().to_string()));
            headers.push(("referer".to_string(), config.referer.trim().to_string()));
        }
        headers
    }

    fn script_headers(&self, config: &CursorRuntimeConfig) -> Vec<(String, String)> {
        let mut headers = self
            .header_generator
            .lock()
            .expect("cursor header generator lock poisoned")
            .script_headers();
        if !config.user_agent.trim().is_empty() {
            headers.push((
                "User-Agent".to_string(),
                config.user_agent.trim().to_string(),
            ));
        }
        if !config.referer.trim().is_empty() {
            headers.push(("Referer".to_string(), config.referer.trim().to_string()));
            headers.push(("referer".to_string(), config.referer.trim().to_string()));
        }
        headers
    }

    fn cached_script(&self) -> (String, u64) {
        let cache = self.script_cache.lock().expect("cursor script cache lock poisoned");
        (cache.script.clone(), cache.fetched_at)
    }

    fn store_script_cache(&self, script: &str) {
        let mut cache = self.script_cache.lock().expect("cursor script cache lock poisoned");
        cache.script = script.to_string();
        cache.fetched_at = now_unix_seconds();
    }

    fn clear_script_cache(&self) {
        let mut cache = self.script_cache.lock().expect("cursor script cache lock poisoned");
        cache.script.clear();
        cache.fetched_at = 0;
    }

    fn refresh_fingerprint(&self) {
        self.header_generator
            .lock()
            .expect("cursor header generator lock poisoned")
            .refresh();
    }
}

impl Provider for CursorProvider {
    fn id(&self) -> &'static str {
        "cursor"
    }

    fn capabilities(&self) -> ProviderCapabilities {
        ProviderCapabilities {
            openai_compatible: true,
            anthropic_compatible: true,
            tools: true,
            images: false,
            multi_account: false,
        }
    }

    fn models(&self) -> Vec<ModelInfo> {
        vec![ModelInfo {
            provider: "cursor",
            public_model: "claude-sonnet-4.6",
            upstream_model: "anthropic/claude-sonnet-4.6",
            owned_by: "cursor",
        }]
    }

    fn build_upstream_preview(&self, req: &UnifiedRequest) -> String {
        let config = self.resolved_config();
        let payload = self.build_payload(req);
        let message_count = payload
            .get("messages")
            .and_then(Value::as_array)
            .map(|items| items.len())
            .unwrap_or(0);
        format!(
            "url={} script_url={} auth=dynamic-browser-fingerprint+x-is-human+optional-cookie protocol={} model={} message_count={} cookie_configured={}",
            config.api_url,
            config.script_url,
            req.protocol,
            payload.get("model").and_then(Value::as_str).unwrap_or(""),
            message_count,
            !config.cookie.trim().is_empty(),
        )
    }

    fn generate_reply(&self, req: &UnifiedRequest) -> Result<String, String> {
        let config = self.resolved_config();
        let payload = self.build_payload(req).to_string();
        let mut last_error = None;

        for attempt in 1..=MAX_RETRIES {
            let x_is_human = self.resolve_x_is_human(&config)?;
            let mut request = self.client.post(&config.api_url).body(payload.clone());
            for (key, value) in self.chat_headers(&config, &x_is_human) {
                request = request.header(key, value);
            }

            let response = request
                .send()
                .map_err(|err| format!("cursor upstream request failed: {err}"));
            let response = match response {
                Ok(value) => value,
                Err(err) => {
                    last_error = Some(err);
                    if attempt < MAX_RETRIES {
                        thread::sleep(Duration::from_millis(200 * attempt as u64));
                        continue;
                    }
                    return Err(last_error.unwrap_or_else(|| "cursor upstream request failed".to_string()));
                }
            };

            let status = response.status();
            if status.as_u16() == 403 && attempt < MAX_RETRIES {
                let _ = response.text();
                self.refresh_fingerprint();
                self.clear_script_cache();
                thread::sleep(Duration::from_millis(200 * attempt as u64));
                continue;
            }
            if !status.is_success() {
                let body = response.text().unwrap_or_default();
                let message = if body.contains("Attention Required! | Cloudflare") {
                    "Cloudflare 403".to_string()
                } else if body.trim().is_empty() {
                    "empty upstream error body".to_string()
                } else {
                    body.trim().to_string()
                };
                return Err(format!(
                    "cursor upstream status {}: {}",
                    status.as_u16(),
                    message
                ));
            }
            return parse_cursor_response(response);
        }

        Err(last_error.unwrap_or_else(|| "cursor upstream failed after retries".to_string()))
    }
}

fn prepare_cursor_messages(req: &UnifiedRequest, max_input_length: usize) -> Vec<Message> {
    truncate_messages(merge_system_message(req), max_input_length)
}

fn merge_system_message(req: &UnifiedRequest) -> Vec<Message> {
    let mut messages = Vec::new();
    if let Some(system) = &req.system {
        let text = content_text(system);
        if !text.is_empty() {
            messages.push(Message {
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
        messages.push(Message {
            role: if role.is_empty() { "user".to_string() } else { role },
            content: Value::String(text),
        });
    }
    messages
}

fn truncate_messages(messages: Vec<Message>, max_input_length: usize) -> Vec<Message> {
    if messages.is_empty() || max_input_length == 0 {
        return messages;
    }
    let total = messages
        .iter()
        .map(|message| content_text(&message.content).len())
        .sum::<usize>();
    if total <= max_input_length {
        return messages;
    }

    let mut result = Vec::new();
    let mut remaining = max_input_length;
    let mut start_index = 0;
    if messages.first().map(|item| item.role.as_str()) == Some("system") {
        result.push(messages[0].clone());
        remaining = remaining.saturating_sub(content_text(&messages[0].content).len());
        start_index = 1;
    }

    let mut collected = Vec::new();
    let mut current = 0;
    for message in messages[start_index..].iter().rev() {
        let text = content_text(&message.content);
        if text.is_empty() || current + text.len() > remaining {
            continue;
        }
        current += text.len();
        collected.push(message.clone());
    }
    collected.reverse();
    result.extend(collected);
    result
}

fn map_cursor_model(model: &str) -> String {
    match model.trim() {
        "" => "anthropic/claude-sonnet-4.6".to_string(),
        "claude-sonnet-4.6" => "anthropic/claude-sonnet-4.6".to_string(),
        other => other.to_string(),
    }
}

fn parse_cursor_response(response: Response) -> Result<String, String> {
    let content_type = response
        .headers()
        .get("content-type")
        .and_then(|value| value.to_str().ok())
        .unwrap_or_default()
        .to_lowercase();
    let body = response
        .text()
        .map_err(|err| format!("read cursor upstream body: {err}"))?;
    if content_type.contains("application/json") {
        extract_cursor_json_text(&body)
    } else {
        extract_cursor_sse_text(&body)
    }
}

fn extract_cursor_json_text(body: &str) -> Result<String, String> {
    let payload = serde_json::from_str::<Value>(body)
        .map_err(|err| format!("decode cursor json response: {err}"))?;
    if let Some(text) = payload.get("text").and_then(Value::as_str) {
        if !text.trim().is_empty() {
            return Ok(text.to_string());
        }
    }
    if let Some(text) = payload.get("content").and_then(Value::as_str) {
        if !text.trim().is_empty() {
            return Ok(text.to_string());
        }
    }
    if let Some(text) = payload
        .get("message")
        .and_then(Value::as_object)
        .and_then(|item| item.get("content"))
        .and_then(Value::as_str)
    {
        if !text.trim().is_empty() {
            return Ok(text.to_string());
        }
    }
    if let Some(text) = payload
        .get("choices")
        .and_then(Value::as_array)
        .and_then(|choices| choices.first())
        .and_then(Value::as_object)
        .and_then(|choice| choice.get("message"))
        .and_then(Value::as_object)
        .and_then(|message| message.get("content"))
        .and_then(Value::as_str)
    {
        if !text.trim().is_empty() {
            return Ok(text.to_string());
        }
    }
    Err("cursor json response did not contain assistant content".to_string())
}

fn extract_cursor_sse_text(body: &str) -> Result<String, String> {
    let mut output = String::new();
    for line in body.lines() {
        let line = line.trim();
        if !line.starts_with("data:") {
            continue;
        }
        let data = line.trim_start_matches("data:").trim();
        if data.is_empty() {
            continue;
        }
        if data == "[DONE]" {
            break;
        }
        let event = match serde_json::from_str::<Value>(data) {
            Ok(value) => value,
            Err(_) => continue,
        };
        let event_type = event.get("type").and_then(Value::as_str).unwrap_or_default();
        match event_type {
            "error" => {
                let message = event
                    .get("errorText")
                    .and_then(Value::as_str)
                    .unwrap_or("unknown error");
                return Err(format!("cursor upstream error: {message}"));
            }
            "finish" => return Ok(output),
            _ => {
                if let Some(delta) = event.get("delta").and_then(Value::as_str) {
                    output.push_str(delta);
                }
            }
        }
    }
    if output.is_empty() {
        Err("cursor upstream returned no assistant content".to_string())
    } else {
        Ok(output)
    }
}

fn run_node_js(js_code: &str) -> Result<String, String> {
    let final_js = format!(
        "const crypto = require('crypto').webcrypto;\nglobal.crypto = crypto;\nglobalThis.crypto = crypto;\nif (typeof window === 'undefined') {{ global.window = global; }}\nwindow.crypto = crypto;\nthis.crypto = crypto;\n{}",
        js_code
    );
    let mut child = Command::new("node")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|err| format!("failed to execute node.js: {err}"))?;
    if let Some(mut stdin) = child.stdin.take() {
        stdin
            .write_all(final_js.as_bytes())
            .map_err(|err| format!("write node.js stdin failed: {err}"))?;
    }
    let output = child
        .wait_with_output()
        .map_err(|err| format!("wait node.js output failed: {err}"))?;
    if !output.status.success() {
        return Err(format!(
            "node.js execution failed: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    Ok(String::from_utf8_lossy(&output.stdout).trim().to_string())
}

fn normalize_js_result(value: &str) -> String {
    let trimmed = value.trim();
    if trimmed.is_empty() {
        return String::new();
    }
    serde_json::from_str::<String>(trimmed)
        .map(|decoded| decoded.trim().to_string())
        .unwrap_or_else(|_| trimmed.trim_matches('"').to_string())
}

fn now_unix_seconds() -> u64 {
    use std::time::{SystemTime, UNIX_EPOCH};
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs()
}

fn env_u64(name: &str, legacy: Option<&str>, default: u64) -> u64 {
    env::var(name)
        .ok()
        .or_else(|| legacy.and_then(|key| env::var(key).ok()))
        .and_then(|raw| raw.trim().parse::<u64>().ok())
        .filter(|value| *value > 0)
        .unwrap_or(default)
}

fn first_non_empty<'a>(primary: &'a str, fallback: &'a str) -> &'a str {
    if !primary.trim().is_empty() {
        primary
    } else {
        fallback
    }
}

const FALLBACK_CURSOR_MAIN_JS: &str = r#"
global.cursor_config = {
    currentScriptSrc: "$$currentScriptSrc$$",
    fp: {
        UNMASKED_VENDOR_WEBGL: "$$UNMASKED_VENDOR_WEBGL$$",
        UNMASKED_RENDERER_WEBGL: "$$UNMASKED_RENDERER_WEBGL$$",
        userAgent: "$$userAgent$$"
    }
}

$$env_jscode$$
$$cursor_jscode$$

Promise.resolve(window.V_C && window.V_C[0] ? window.V_C[0]() : "")
    .then(value => console.log(JSON.stringify(value)))
    .catch(error => {
        console.error(String(error));
        process.exit(1);
    });
"#;

const FALLBACK_CURSOR_ENV_JS: &str = r#"
window = global;
window.console = console;
window.document = { currentScript: { src: global.cursor_config.currentScriptSrc } };
window.navigator = { userAgent: global.cursor_config.fp.userAgent };
"#;

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn cursor_json_response_extracts_assistant_text() {
        assert_eq!(
            extract_cursor_json_text("{\"text\":\"hello\"}").expect("json text"),
            "hello"
        );
        assert_eq!(
            extract_cursor_json_text("{\"choices\":[{\"message\":{\"content\":\"hi\"}}]}")
                .expect("choices text"),
            "hi"
        );
    }

    #[test]
    fn cursor_sse_response_collects_delta_text() {
        let body = concat!(
            "data: {\"type\":\"delta\",\"delta\":\"Hel\"}\n\n",
            "data: {\"type\":\"delta\",\"delta\":\"lo\"}\n\n",
            "data: {\"type\":\"finish\"}\n\n"
        );
        assert_eq!(extract_cursor_sse_text(body).expect("sse text"), "Hello");
    }

    #[test]
    fn cursor_message_preparation_keeps_request_system_and_latest_user() {
        let req = UnifiedRequest {
            provider_hint: "cursor".to_string(),
            protocol: "openai",
            model: "claude-sonnet-4.6".to_string(),
            messages: vec![
                Message {
                    role: "user".to_string(),
                    content: json!("this is too long"),
                },
                Message {
                    role: "user".to_string(),
                    content: json!("hi"),
                },
            ],
            system: Some(json!("rules")),
            stream: false,
        };
        let prepared = prepare_cursor_messages(&req, 7);
        assert_eq!(prepared.len(), 2);
        assert_eq!(prepared[0].role, "system");
        assert_eq!(content_text(&prepared[0].content), "rules");
        assert_eq!(prepared[1].role, "user");
        assert_eq!(content_text(&prepared[1].content), "hi");
    }
}