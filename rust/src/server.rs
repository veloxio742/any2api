use std::collections::HashMap;
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Mutex, OnceLock};
use std::time::{SystemTime, UNIX_EPOCH};

use serde_json::{json, Value};

use crate::admin_store::{
    json_array_field, json_array_objects, json_bool_field, json_escape, json_object_field,
    json_string_field, AdminConfig, AdminSettings, AdminStore, CursorRuntimeConfig, GrokToken,
    KiroAccount, OrchidsRuntimeConfig,
};
use crate::providers::default_registry;
use crate::registry::Registry;
use crate::types::{Message, UnifiedRequest};

const ADMIN_SESSION_COOKIE: &str = "newplatform2api_admin_session";
const ADMIN_AUTH_MODE: &str = "session_cookie";
const ADMIN_BACKEND_VERSION: &str = "0.1.0";

#[derive(Debug, Clone)]
struct ParsedRequest {
    method: String,
    path: String,
    headers: HashMap<String, String>,
    body: String,
}

impl ParsedRequest {
    fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .get(&name.to_ascii_lowercase())
            .map(String::as_str)
    }
}

struct AdminSessionStore {
    sessions: Mutex<HashMap<String, u64>>,
    counter: AtomicU64,
}

impl AdminSessionStore {
    fn new() -> Self {
        Self {
            sessions: Mutex::new(HashMap::new()),
            counter: AtomicU64::new(1),
        }
    }

    fn create(&self) -> (String, u64) {
        let expires_at = now_unix() + 86_400;
        let token = format!(
            "{:x}{:x}",
            expires_at,
            self.counter.fetch_add(1, Ordering::Relaxed)
        );
        self.sessions
            .lock()
            .expect("admin sessions lock poisoned")
            .insert(token.clone(), expires_at);
        (token, expires_at)
    }

    fn expires_at(&self, token: &str) -> Option<u64> {
        let trimmed = token.trim();
        if trimmed.is_empty() {
            return None;
        }
        let mut sessions = self.sessions.lock().expect("admin sessions lock poisoned");
        let expires_at = sessions.get(trimmed).copied()?;
        if now_unix() >= expires_at {
            sessions.remove(trimmed);
            return None;
        }
        Some(expires_at)
    }

    fn delete(&self, token: &str) {
        let trimmed = token.trim();
        if trimmed.is_empty() {
            return;
        }
        self.sessions
            .lock()
            .expect("admin sessions lock poisoned")
            .remove(trimmed);
    }

    #[cfg(test)]
    fn clear(&self) {
        self.sessions
            .lock()
            .expect("admin sessions lock poisoned")
            .clear();
    }
}

static ADMIN_SESSIONS: OnceLock<AdminSessionStore> = OnceLock::new();
static ADMIN_STORE: OnceLock<AdminStore> = OnceLock::new();
static RESPONSE_COUNTER: AtomicU64 = AtomicU64::new(1);

fn admin_sessions() -> &'static AdminSessionStore {
    ADMIN_SESSIONS.get_or_init(AdminSessionStore::new)
}

fn admin_store() -> &'static AdminStore {
    ADMIN_STORE.get_or_init(AdminStore::from_env)
}

fn now_unix() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs()
}

fn response(
    status: &str,
    content_type: &str,
    headers: Vec<(String, String)>,
    body: String,
) -> String {
    let mut lines = vec![
        format!("HTTP/1.1 {status}"),
        format!("Content-Type: {content_type}"),
        format!("Content-Length: {}", body.len()),
        "Connection: close".to_string(),
    ];
    for (key, value) in headers {
        lines.push(format!("{key}: {value}"));
    }
    format!("{}\r\n\r\n{}", lines.join("\r\n"), body)
}

fn json_response(status: &str, headers: Vec<(String, String)>, body: String) -> String {
    response(status, "application/json; charset=utf-8", headers, body)
}

fn json_response_value(status: &str, headers: Vec<(String, String)>, body: Value) -> String {
    json_response(status, headers, body.to_string())
}

fn event_stream_response(status: &str, headers: Vec<(String, String)>, body: String) -> String {
    response(status, "text/event-stream", headers, body)
}

fn next_response_id(prefix: &str) -> String {
    format!(
        "{}_{:x}{:x}",
        prefix,
        now_unix(),
        RESPONSE_COUNTER.fetch_add(1, Ordering::Relaxed)
    )
}

fn json_error_response(status: &str, headers: Vec<(String, String)>, message: &str) -> String {
    json_response_value(status, headers, json!({"error": message}))
}

fn parse_request(raw: &str) -> ParsedRequest {
    let (head, body) = raw.split_once("\r\n\r\n").unwrap_or((raw, ""));
    let mut lines = head.lines();
    let request_line = lines.next().unwrap_or("");
    let mut parts = request_line.split_whitespace();
    let method = parts.next().unwrap_or("").to_string();
    let path = parts.next().unwrap_or("/").to_string();
    let mut headers = HashMap::new();
    for line in lines {
        if let Some((name, value)) = line.split_once(':') {
            headers.insert(name.trim().to_ascii_lowercase(), value.trim().to_string());
        }
    }
    ParsedRequest {
        method,
        path,
        headers,
        body: body.to_string(),
    }
}

fn parse_json_object_body(body: &str) -> Result<Value, String> {
    let value = serde_json::from_str::<Value>(body).map_err(|_| "invalid json".to_string())?;
    if !value.is_object() {
        return Err("invalid json".to_string());
    }
    Ok(value)
}

fn provider_from_payload(payload: &Value) -> Option<String> {
    payload
        .get("provider")
        .and_then(Value::as_str)
        .map(str::trim)
        .filter(|item| !item.is_empty())
        .map(ToString::to_string)
}

fn payload_string(payload: &Value, key: &str) -> String {
    payload
        .get(key)
        .and_then(Value::as_str)
        .unwrap_or("")
        .trim()
        .to_string()
}

fn payload_bool(payload: &Value, key: &str) -> bool {
    payload.get(key).and_then(Value::as_bool).unwrap_or(false)
}

fn payload_messages(payload: &Value) -> Vec<Message> {
    payload
        .get("messages")
        .and_then(Value::as_array)
        .map(|items| {
            items
                .iter()
                .filter_map(|item| {
                    let obj = item.as_object()?;
                    Some(Message {
                        role: obj
                            .get("role")
                            .and_then(Value::as_str)
                            .unwrap_or("user")
                            .trim()
                            .to_string(),
                        content: obj.get("content").cloned().unwrap_or(Value::Null),
                    })
                })
                .collect::<Vec<_>>()
        })
        .unwrap_or_default()
}

fn provider_headers(provider_id: &str) -> Vec<(String, String)> {
    vec![(
        "X-Newplatform2API-Provider".to_string(),
        provider_id.to_string(),
    )]
}

fn build_openai_request(payload: &Value, provider_hint: String) -> UnifiedRequest {
    UnifiedRequest {
        provider_hint,
        protocol: "openai",
        model: payload_string(payload, "model"),
        messages: payload_messages(payload),
        system: None,
        stream: payload_bool(payload, "stream"),
    }
}

fn build_anthropic_request(payload: &Value, provider_hint: String) -> UnifiedRequest {
    UnifiedRequest {
        provider_hint,
        protocol: "anthropic",
        model: payload_string(payload, "model"),
        messages: payload_messages(payload),
        system: payload.get("system").cloned(),
        stream: payload_bool(payload, "stream"),
    }
}

fn openai_sse_body(text: &str, completion_id: &str, model: &str) -> String {
    let chunk = json!({
        "id": completion_id,
        "object": "chat.completion.chunk",
        "model": model,
        "choices": [{
            "index": 0,
            "delta": {"role": "assistant", "content": text},
            "finish_reason": Value::Null
        }]
    });
    format!("data: {}\n\ndata: [DONE]\n\n", chunk)
}

fn anthropic_sse_body(text: &str, message_id: &str) -> String {
    let start = json!({"type": "message_start", "message": {"id": message_id}});
    let delta = json!({
        "type": "content_block_delta",
        "delta": {"type": "text_delta", "text": text}
    });
    let stop = json!({"type": "message_stop"});
    format!(
        "event: message_start\ndata: {}\n\nevent: content_block_delta\ndata: {}\n\nevent: message_stop\ndata: {}\n\n",
        start, delta, stop
    )
}

fn handle_openai_chat(
    request: &ParsedRequest,
    registry: &Registry,
    provider_hint_from_path: Option<&str>,
) -> String {
    let payload = match parse_json_object_body(&request.body) {
        Ok(value) => value,
        Err(err) => return json_error_response("400 Bad Request", vec![], &err),
    };
    let provider_hint = provider_from_payload(&payload)
        .or_else(|| provider_hint_from_path.map(ToString::to_string));
    let provider = match registry.resolve(provider_hint.as_deref()) {
        Ok(value) => value,
        Err(err) => return json_error_response("400 Bad Request", vec![], &err),
    };
    if !provider.capabilities().openai_compatible {
        return json_error_response(
            "400 Bad Request",
            provider_headers(provider.id()),
            "provider does not support /v1/chat/completions",
        );
    }
    let req = build_openai_request(&payload, provider_hint.unwrap_or_default());
    match provider.generate_reply(&req) {
        Ok(text) => {
            let completion_id = next_response_id("chatcmpl");
            let headers = provider_headers(provider.id());
            if req.stream {
                return event_stream_response(
                    "200 OK",
                    headers,
                    openai_sse_body(&text, &completion_id, &req.model),
                );
            }
            json_response_value(
                "200 OK",
                headers,
                json!({
                    "id": completion_id,
                    "object": "chat.completion",
                    "model": req.model,
                    "choices": [{
                        "index": 0,
                        "message": {"role": "assistant", "content": text},
                        "finish_reason": "stop"
                    }]
                }),
            )
        }
        Err(err) => json_error_response(
            "502 Bad Gateway",
            provider_headers(provider.id()),
            &err,
        ),
    }
}

fn handle_anthropic_messages(
    request: &ParsedRequest,
    registry: &Registry,
    provider_hint_from_path: Option<&str>,
) -> String {
    let payload = match parse_json_object_body(&request.body) {
        Ok(value) => value,
        Err(err) => return json_error_response("400 Bad Request", vec![], &err),
    };
    let provider_hint = provider_from_payload(&payload)
        .or_else(|| provider_hint_from_path.map(ToString::to_string));
    let provider = match registry.resolve(provider_hint.as_deref()) {
        Ok(value) => value,
        Err(err) => return json_error_response("400 Bad Request", vec![], &err),
    };
    if !provider.capabilities().anthropic_compatible {
        return json_error_response(
            "400 Bad Request",
            provider_headers(provider.id()),
            "provider does not support /v1/messages",
        );
    }
    let req = build_anthropic_request(&payload, provider_hint.unwrap_or_default());
    match provider.generate_reply(&req) {
        Ok(text) => {
            let message_id = next_response_id("msg");
            let headers = provider_headers(provider.id());
            if req.stream {
                return event_stream_response(
                    "200 OK",
                    headers,
                    anthropic_sse_body(&text, &message_id),
                );
            }
            json_response_value(
                "200 OK",
                headers,
                json!({
                    "id": message_id,
                    "type": "message",
                    "role": "assistant",
                    "model": req.model,
                    "content": [{"type": "text", "text": text}],
                    "stop_reason": "end_turn"
                }),
            )
        }
        Err(err) => json_error_response(
            "502 Bad Gateway",
            provider_headers(provider.id()),
            &err,
        ),
    }
}

fn admin_cors_headers(request: &ParsedRequest) -> Vec<(String, String)> {
    let origin = request.header("origin").unwrap_or("*").to_string();
    let mut headers = vec![
        ("Access-Control-Allow-Origin".to_string(), origin.clone()),
        (
            "Access-Control-Allow-Headers".to_string(),
            "Content-Type, Authorization".to_string(),
        ),
        (
            "Access-Control-Allow-Methods".to_string(),
            "GET, POST, PUT, OPTIONS".to_string(),
        ),
    ];
    if origin != "*" {
        headers.push((
            "Access-Control-Allow-Credentials".to_string(),
            "true".to_string(),
        ));
    }
    headers
}

fn path_only(path: &str) -> &str {
    path.split('?').next().unwrap_or(path)
}

fn provider_from_path(path: &str) -> Option<&str> {
    path.split('?')
        .nth(1)
        .and_then(|query| query.split('&').find_map(|item| item.strip_prefix("provider=")))
}

fn admin_session_token(request: &ParsedRequest) -> Option<String> {
    if let Some(value) = request.header("authorization") {
        if let Some(token) = value.strip_prefix("Bearer ") {
            let trimmed = token.trim();
            if !trimmed.is_empty() {
                return Some(trimmed.to_string());
            }
        }
    }
    if let Some(cookie_header) = request.header("cookie") {
        for chunk in cookie_header.split(';') {
            if let Some((name, value)) = chunk.trim().split_once('=') {
                if name == ADMIN_SESSION_COOKIE && !value.trim().is_empty() {
                    return Some(value.trim().to_string());
                }
            }
        }
    }
    None
}

fn require_admin_token(request: &ParsedRequest) -> Result<(String, u64), String> {
    let token = admin_session_token(request).ok_or_else(|| {
        json_response(
            "401 Unauthorized",
            admin_cors_headers(request),
            "{\"error\":\"admin login required\"}".to_string(),
        )
    })?;
    let expires_at = admin_sessions().expires_at(&token).ok_or_else(|| {
        json_response(
            "401 Unauthorized",
            admin_cors_headers(request),
            "{\"error\":\"admin login required\"}".to_string(),
        )
    })?;
    Ok((token, expires_at))
}

fn shared_admin_features_json() -> &'static str {
    "{\"providers\":true,\"credentials\":true,\"providerState\":true,\"stats\":false,\"logs\":false,\"users\":false,\"configImportExport\":false}"
}

fn bool_count(value: bool) -> usize {
    if value {
        1
    } else {
        0
    }
}

fn provider_active_label(configured: bool) -> &'static str {
    if configured {
        "default"
    } else {
        ""
    }
}

fn active_kiro_id(accounts: &[KiroAccount]) -> String {
    accounts
        .iter()
        .find(|item| item.active)
        .map(|item| item.id.clone())
        .unwrap_or_default()
}

fn active_grok_id(tokens: &[GrokToken]) -> String {
    tokens
        .iter()
        .find(|item| item.active)
        .map(|item| item.id.clone())
        .unwrap_or_default()
}

fn admin_settings_json(settings: &AdminSettings) -> String {
    format!(
        "{{\"apiKey\":\"{}\",\"defaultProvider\":\"{}\",\"adminPasswordConfigured\":{}}}",
        json_escape(&settings.api_key),
        json_escape(&settings.default_provider),
        !settings.admin_password.trim().is_empty()
    )
}

fn cursor_config_json(config: &CursorRuntimeConfig) -> String {
    let fields = [
        format!("\"apiUrl\":\"{}\"", json_escape(&config.api_url)),
        format!("\"scriptUrl\":\"{}\"", json_escape(&config.script_url)),
        format!("\"cookie\":\"{}\"", json_escape(&config.cookie)),
        format!("\"xIsHuman\":\"{}\"", json_escape(&config.x_is_human)),
        format!("\"userAgent\":\"{}\"", json_escape(&config.user_agent)),
        format!("\"referer\":\"{}\"", json_escape(&config.referer)),
        format!("\"webglVendor\":\"{}\"", json_escape(&config.webgl_vendor)),
        format!(
            "\"webglRenderer\":\"{}\"",
            json_escape(&config.webgl_renderer)
        ),
    ];
    format!("{{{}}}", fields.join(","))
}

fn kiro_account_json(account: &KiroAccount) -> String {
    let fields = [
        format!("\"id\":\"{}\"", json_escape(&account.id)),
        format!("\"name\":\"{}\"", json_escape(&account.name)),
        format!(
            "\"accessToken\":\"{}\"",
            json_escape(&account.access_token)
        ),
        format!("\"machineId\":\"{}\"", json_escape(&account.machine_id)),
        format!(
            "\"preferredEndpoint\":\"{}\"",
            json_escape(&account.preferred_endpoint)
        ),
        format!("\"active\":{}", account.active),
    ];
    format!("{{{}}}", fields.join(","))
}

fn grok_token_json(token: &GrokToken) -> String {
    format!(
        "{{\"id\":\"{}\",\"name\":\"{}\",\"cookieToken\":\"{}\",\"active\":{}}}",
        json_escape(&token.id),
        json_escape(&token.name),
        json_escape(&token.cookie_token),
        token.active,
    )
}

fn orchids_config_json(config: &OrchidsRuntimeConfig) -> String {
    let fields = [
        format!("\"apiUrl\":\"{}\"", json_escape(&config.api_url)),
        format!("\"clerkUrl\":\"{}\"", json_escape(&config.clerk_url)),
        format!(
            "\"clientCookie\":\"{}\"",
            json_escape(&config.client_cookie)
        ),
        format!("\"clientUat\":\"{}\"", json_escape(&config.client_uat)),
        format!("\"sessionId\":\"{}\"", json_escape(&config.session_id)),
        format!("\"projectId\":\"{}\"", json_escape(&config.project_id)),
        format!("\"userId\":\"{}\"", json_escape(&config.user_id)),
        format!("\"email\":\"{}\"", json_escape(&config.email)),
        format!("\"agentMode\":\"{}\"", json_escape(&config.agent_mode)),
    ];
    format!("{{{}}}", fields.join(","))
}

fn admin_status_json(config: &AdminConfig) -> String {
    let cursor_configured = !config.providers.cursor_config.cookie.trim().is_empty();
    let orchids_configured = !config.providers.orchids_config.client_cookie.trim().is_empty();
    let kiro_count = config.providers.kiro_accounts.len();
    let grok_count = config.providers.grok_tokens.len();
    let providers = [
        format!(
            "\"cursor\":{{\"count\":{},\"configured\":{},\"active\":\"{}\"}}",
            bool_count(cursor_configured),
            cursor_configured,
            provider_active_label(cursor_configured)
        ),
        format!(
            "\"kiro\":{{\"count\":{},\"configured\":{},\"active\":\"{}\"}}",
            kiro_count,
            kiro_count > 0,
            json_escape(&active_kiro_id(&config.providers.kiro_accounts))
        ),
        format!(
            "\"grok\":{{\"count\":{},\"configured\":{},\"active\":\"{}\"}}",
            grok_count,
            grok_count > 0,
            json_escape(&active_grok_id(&config.providers.grok_tokens))
        ),
        format!(
            "\"orchids\":{{\"count\":{},\"configured\":{},\"active\":\"{}\"}}",
            bool_count(orchids_configured),
            orchids_configured,
            provider_active_label(orchids_configured)
        ),
    ];
    format!(
        "{{\"project\":\"any2api-rust\",\"settings\":{},\"providers\":{{{}}}}}",
        admin_settings_json(&config.settings),
        providers.join(",")
    )
}

fn parse_settings_body(body: &str) -> Result<(String, String, String), ()> {
    if !body.trim_start().starts_with('{') {
        return Err(());
    }
    Ok((
        json_string_field(body, "apiKey").unwrap_or_default(),
        json_string_field(body, "defaultProvider").unwrap_or_default(),
        json_string_field(body, "adminPassword").unwrap_or_default(),
    ))
}

fn parse_kiro_account(body: &str) -> KiroAccount {
    KiroAccount {
        id: json_string_field(body, "id").unwrap_or_default(),
        name: json_string_field(body, "name").unwrap_or_default(),
        access_token: json_string_field(body, "accessToken").unwrap_or_default(),
        machine_id: json_string_field(body, "machineId").unwrap_or_default(),
        preferred_endpoint: json_string_field(body, "preferredEndpoint")
            .unwrap_or_default()
            .to_lowercase(),
        active: json_bool_field(body, "active").unwrap_or(false),
    }
}

fn parse_grok_token(body: &str) -> GrokToken {
    GrokToken {
        id: json_string_field(body, "id").unwrap_or_default(),
        name: json_string_field(body, "name").unwrap_or_default(),
        cookie_token: json_string_field(body, "cookieToken").unwrap_or_default(),
        active: json_bool_field(body, "active").unwrap_or(false),
    }
}

fn parse_cursor_config_body(body: &str) -> Result<CursorRuntimeConfig, ()> {
    let config = json_object_field(body, "config").ok_or(())?;
    Ok(CursorRuntimeConfig::from_json(&config))
}

fn parse_orchids_config_body(body: &str) -> Result<OrchidsRuntimeConfig, ()> {
    let config = json_object_field(body, "config").ok_or(())?;
    Ok(OrchidsRuntimeConfig::from_json(&config))
}

fn parse_kiro_accounts_body(body: &str) -> Result<Vec<KiroAccount>, ()> {
    let accounts = json_array_field(body, "accounts").ok_or(())?;
    Ok(json_array_objects(&accounts)
        .into_iter()
        .map(|item| parse_kiro_account(&item))
        .collect())
}

fn parse_grok_tokens_body(body: &str) -> Result<Vec<GrokToken>, ()> {
    let tokens = json_array_field(body, "tokens").ok_or(())?;
    Ok(json_array_objects(&tokens)
        .into_iter()
        .map(|item| parse_grok_token(&item))
        .collect())
}

fn handle_admin_api(request: &ParsedRequest, method: &str, clean_path: &str) -> Option<String> {
    if !clean_path.starts_with("/admin/api/") {
        return None;
    }
    if let Err(response) = require_admin_token(request) {
        return Some(response);
    }
    let store = admin_store();
    let snapshot = store.snapshot();
    let response = match (method, clean_path) {
        ("GET", "/admin/api/status") => json_response(
            "200 OK",
            admin_cors_headers(request),
            admin_status_json(&snapshot),
        ),
        ("GET", "/admin/api/settings") => json_response(
            "200 OK",
            admin_cors_headers(request),
            admin_settings_json(&snapshot.settings),
        ),
        ("PUT", "/admin/api/settings") => match parse_settings_body(&request.body) {
            Ok((api_key, default_provider, admin_password)) => match store
                .update_settings(&api_key, &default_provider, &admin_password)
            {
                Ok(config) => json_response(
                    "200 OK",
                    admin_cors_headers(request),
                    admin_settings_json(&config.settings),
                ),
                Err(err) => json_response(
                    "500 Internal Server Error",
                    admin_cors_headers(request),
                    format!("{{\"error\":\"{}\"}}", json_escape(&err)),
                ),
            },
            Err(()) => json_response(
                "400 Bad Request",
                admin_cors_headers(request),
                "{\"error\":\"invalid json\"}".to_string(),
            ),
        },
        ("GET", "/admin/api/providers/cursor/config") => json_response(
            "200 OK",
            admin_cors_headers(request),
            format!(
                "{{\"config\":{}}}",
                cursor_config_json(&snapshot.providers.cursor_config)
            ),
        ),
        ("PUT", "/admin/api/providers/cursor/config") => {
            match parse_cursor_config_body(&request.body) {
                Ok(config) => match store.replace_cursor_config(config) {
                    Ok(config) => json_response(
                        "200 OK",
                        admin_cors_headers(request),
                        format!(
                            "{{\"config\":{}}}",
                            cursor_config_json(&config.providers.cursor_config)
                        ),
                    ),
                    Err(err) => json_response(
                        "500 Internal Server Error",
                        admin_cors_headers(request),
                        format!("{{\"error\":\"{}\"}}", json_escape(&err)),
                    ),
                },
                Err(()) => json_response(
                    "400 Bad Request",
                    admin_cors_headers(request),
                    "{\"error\":\"invalid json\"}".to_string(),
                ),
            }
        }
        ("GET", "/admin/api/providers/kiro/accounts") => {
            let body = snapshot
                .providers
                .kiro_accounts
                .iter()
                .map(kiro_account_json)
                .collect::<Vec<_>>()
                .join(",");
            json_response(
                "200 OK",
                admin_cors_headers(request),
                format!("{{\"accounts\":[{body}]}}"),
            )
        }
        ("PUT", "/admin/api/providers/kiro/accounts") => {
            match parse_kiro_accounts_body(&request.body) {
                Ok(accounts) => match store.replace_kiro_accounts(accounts) {
                    Ok(config) => {
                        let body = config
                            .providers
                            .kiro_accounts
                            .iter()
                            .map(kiro_account_json)
                            .collect::<Vec<_>>()
                            .join(",");
                        json_response(
                            "200 OK",
                            admin_cors_headers(request),
                            format!("{{\"accounts\":[{body}]}}"),
                        )
                    }
                    Err(err) => json_response(
                        "500 Internal Server Error",
                        admin_cors_headers(request),
                        format!("{{\"error\":\"{}\"}}", json_escape(&err)),
                    ),
                },
                Err(()) => json_response(
                    "400 Bad Request",
                    admin_cors_headers(request),
                    "{\"error\":\"invalid json\"}".to_string(),
                ),
            }
        }
        ("GET", "/admin/api/providers/grok/tokens") => {
            let body = snapshot
                .providers
                .grok_tokens
                .iter()
                .map(grok_token_json)
                .collect::<Vec<_>>()
                .join(",");
            json_response(
                "200 OK",
                admin_cors_headers(request),
                format!("{{\"tokens\":[{body}]}}"),
            )
        }
        ("PUT", "/admin/api/providers/grok/tokens") => {
            match parse_grok_tokens_body(&request.body) {
                Ok(tokens) => match store.replace_grok_tokens(tokens) {
                    Ok(config) => {
                        let body = config
                            .providers
                            .grok_tokens
                            .iter()
                            .map(grok_token_json)
                            .collect::<Vec<_>>()
                            .join(",");
                        json_response(
                            "200 OK",
                            admin_cors_headers(request),
                            format!("{{\"tokens\":[{body}]}}"),
                        )
                    }
                    Err(err) => json_response(
                        "500 Internal Server Error",
                        admin_cors_headers(request),
                        format!("{{\"error\":\"{}\"}}", json_escape(&err)),
                    ),
                },
                Err(()) => json_response(
                    "400 Bad Request",
                    admin_cors_headers(request),
                    "{\"error\":\"invalid json\"}".to_string(),
                ),
            }
        }
        ("GET", "/admin/api/providers/orchids/config") => json_response(
            "200 OK",
            admin_cors_headers(request),
            format!(
                "{{\"config\":{}}}",
                orchids_config_json(&snapshot.providers.orchids_config)
            ),
        ),
        ("PUT", "/admin/api/providers/orchids/config") => {
            match parse_orchids_config_body(&request.body) {
                Ok(config) => match store.replace_orchids_config(config) {
                    Ok(config) => json_response(
                        "200 OK",
                        admin_cors_headers(request),
                        format!(
                            "{{\"config\":{}}}",
                            orchids_config_json(&config.providers.orchids_config)
                        ),
                    ),
                    Err(err) => json_response(
                        "500 Internal Server Error",
                        admin_cors_headers(request),
                        format!("{{\"error\":\"{}\"}}", json_escape(&err)),
                    ),
                },
                Err(()) => json_response(
                    "400 Bad Request",
                    admin_cors_headers(request),
                    "{\"error\":\"invalid json\"}".to_string(),
                ),
            }
        }
        _ => json_response(
            "404 Not Found",
            admin_cors_headers(request),
            "{\"error\":\"not found\"}".to_string(),
        ),
    };
    Some(response)
}

fn handle_request(request: ParsedRequest) -> String {
    admin_store().sync_from_env();

    let method = request.method.as_str();
    let path = request.path.as_str();
    let clean_path = path_only(path);

    if (clean_path.starts_with("/api/admin/") || clean_path.starts_with("/admin/api/"))
        && method == "OPTIONS"
    {
        return response(
            "204 No Content",
            "text/plain; charset=utf-8",
            admin_cors_headers(&request),
            String::new(),
        );
    }

    if method == "GET" && clean_path == "/api/admin/meta" {
        return json_response(
            "200 OK",
            admin_cors_headers(&request),
            format!(
                "{{\"backend\":{{\"language\":\"rust\",\"version\":\"{ADMIN_BACKEND_VERSION}\"}},\"auth\":{{\"mode\":\"{ADMIN_AUTH_MODE}\"}},\"features\":{}}}",
                shared_admin_features_json()
            ),
        );
    }

    if method == "POST" && clean_path == "/api/admin/auth/login" {
        let Some(password) = json_string_field(&request.body, "password") else {
            return json_response(
                "400 Bad Request",
                admin_cors_headers(&request),
                "{\"error\":\"invalid json\"}".to_string(),
            );
        };
        if password.trim() != admin_store().admin_password() {
            return json_response(
                "401 Unauthorized",
                admin_cors_headers(&request),
                "{\"error\":\"invalid admin password\"}".to_string(),
            );
        }
        let (token, _) = admin_sessions().create();
        let mut headers = admin_cors_headers(&request);
        headers.push((
            "Set-Cookie".to_string(),
            format!(
                "{ADMIN_SESSION_COOKIE}={token}; Path=/; HttpOnly; SameSite=Lax; Max-Age=86400"
            ),
        ));
        return json_response(
            "200 OK",
            headers,
            format!("{{\"ok\":true,\"token\":\"{}\"}}", json_escape(&token)),
        );
    }

    if method == "GET" && clean_path == "/api/admin/auth/session" {
        let (_, expires_at) = match require_admin_token(&request) {
            Ok(value) => value,
            Err(response) => return response,
        };
        return json_response(
            "200 OK",
            admin_cors_headers(&request),
            format!(
                "{{\"authenticated\":true,\"user\":{{\"id\":\"local-admin\",\"name\":\"Admin\",\"role\":\"admin\"}},\"expiresAt\":\"{}\"}}",
                expires_at
            ),
        );
    }

    if method == "POST" && clean_path == "/api/admin/auth/logout" {
        let (token, _) = match require_admin_token(&request) {
            Ok(value) => value,
            Err(response) => return response,
        };
        admin_sessions().delete(&token);
        let mut headers = admin_cors_headers(&request);
        headers.push((
            "Set-Cookie".to_string(),
            format!(
                "{ADMIN_SESSION_COOKIE}=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0"
            ),
        ));
        return json_response("200 OK", headers, "{\"ok\":true}".to_string());
    }

    if let Some(response) = handle_admin_api(&request, method, clean_path) {
        return response;
    }

    let snapshot = admin_store().snapshot();
    let registry = default_registry(&snapshot.settings.default_provider, &snapshot);
    let provider_key = provider_from_path(path);

    if method == "GET" && path.starts_with("/health") {
        return json_response(
            "200 OK",
            vec![],
            "{\"status\":\"ok\",\"project\":\"any2api-rust\"}".to_string(),
        );
    }

    if method == "GET" && path.starts_with("/v1/models") {
        return match registry.models(provider_key) {
            Ok(models) => {
                let data = models.iter().map(|item| format!(
                    "{{\"id\":\"{}\",\"object\":\"model\",\"owned_by\":\"{}\",\"provider\":\"{}\",\"upstream_model\":\"{}\"}}",
                    item.public_model, item.owned_by, item.provider, item.upstream_model
                )).collect::<Vec<_>>().join(",");
                json_response("200 OK", vec![], format!("{{\"object\":\"list\",\"data\":[{}]}}", data))
            }
            Err(err) => json_response(
                "400 Bad Request",
                vec![],
                format!("{{\"error\":\"{}\"}}", json_escape(&err)),
            ),
        };
    }

    if method == "POST" && path.starts_with("/v1/chat/completions") {
        return handle_openai_chat(&request, &registry, provider_key);
    }

    if method == "POST" && path.starts_with("/v1/messages") {
        return handle_anthropic_messages(&request, &registry, provider_key);
    }

    json_response("404 Not Found", vec![], "{\"error\":\"not found\"}".to_string())
}

fn handle_stream(mut stream: TcpStream, raw_request: &str) {
    let payload = handle_request(parse_request(raw_request));
    let _ = stream.write_all(payload.as_bytes());
}

pub fn run(addr: &str) -> std::io::Result<()> {
    let listener = TcpListener::bind(addr)?;
    println!("any2api-rust listening on http://{addr}");
    for incoming in listener.incoming() {
        let mut stream = incoming?;
        let mut buffer = [0_u8; 8192];
        let size = stream.read(&mut buffer)?;
        let raw = String::from_utf8_lossy(&buffer[..size]).to_string();
        handle_stream(stream, &raw);
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::env;
    use std::io::{Read, Write};
    use std::fs;
    use std::net::TcpListener;
    use std::path::PathBuf;
    use std::thread;

    static TEST_LOCK: OnceLock<Mutex<()>> = OnceLock::new();
    static TEST_COUNTER: AtomicU64 = AtomicU64::new(1);

    fn build_request(
        method: &str,
        path: &str,
        headers: &[(&str, &str)],
        body: &str,
    ) -> ParsedRequest {
        let mut parsed_headers = HashMap::new();
        for (name, value) in headers {
            parsed_headers.insert(name.to_ascii_lowercase(), (*value).to_string());
        }
        ParsedRequest {
            method: method.to_string(),
            path: path.to_string(),
            headers: parsed_headers,
            body: body.to_string(),
        }
    }

    fn response_token(response: &str) -> Option<String> {
        let marker = "\"token\":\"";
        let start = response.find(marker)? + marker.len();
        let rest = &response[start..];
        Some(rest.split('"').next()?.to_string())
    }

    fn test_lock() -> &'static Mutex<()> {
        TEST_LOCK.get_or_init(|| Mutex::new(()))
    }

    struct EnvVarGuard {
        key: &'static str,
        previous: Option<String>,
    }

    impl EnvVarGuard {
        fn set(key: &'static str, value: impl Into<String>) -> Self {
            let previous = env::var(key).ok();
            env::set_var(key, value.into());
            Self { key, previous }
        }
    }

    impl Drop for EnvVarGuard {
        fn drop(&mut self) {
            match &self.previous {
                Some(value) => env::set_var(self.key, value),
                None => env::remove_var(self.key),
            }
        }
    }

    struct MockResponse {
        status: &'static str,
        content_type: &'static str,
        headers: Vec<(String, String)>,
        body: Vec<u8>,
    }

    fn text_mock_response(status: &'static str, content_type: &'static str, body: &str) -> MockResponse {
        MockResponse {
            status,
            content_type,
            headers: vec![],
            body: body.as_bytes().to_vec(),
        }
    }

    fn spawn_mock_server<F>(expected_requests: usize, handler: F) -> String
    where
        F: Fn(&ParsedRequest) -> MockResponse + Send + 'static,
    {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind mock server");
        let addr = listener.local_addr().expect("mock server local addr");
        thread::spawn(move || {
            for _ in 0..expected_requests {
                let (mut stream, _) = listener.accept().expect("accept mock request");
                let mut buffer = [0_u8; 16 * 1024];
                let size = stream.read(&mut buffer).expect("read mock request");
                let raw = String::from_utf8_lossy(&buffer[..size]).to_string();
                let request = parse_request(&raw);
                let response = handler(&request);
                let mut head = vec![
                    format!("HTTP/1.1 {}", response.status),
                    format!("Content-Type: {}", response.content_type),
                    format!("Content-Length: {}", response.body.len()),
                    "Connection: close".to_string(),
                ];
                for (key, value) in response.headers {
                    head.push(format!("{key}: {value}"));
                }
                let raw_head = format!("{}\r\n\r\n", head.join("\r\n"));
                stream
                    .write_all(raw_head.as_bytes())
                    .expect("write mock response head");
                stream
                    .write_all(&response.body)
                    .expect("write mock response body");
            }
        });
        format!("http://{addr}")
    }

    fn kiro_frame(event_type: &str, payload: &str) -> Vec<u8> {
        let mut headers = Vec::new();
        headers.push(11);
        headers.extend_from_slice(b":event-type");
        headers.push(7);
        headers.extend_from_slice(&(event_type.len() as u16).to_be_bytes());
        headers.extend_from_slice(event_type.as_bytes());
        let payload_bytes = payload.as_bytes();
        let total_length = 12 + headers.len() + payload_bytes.len() + 4;
        let mut frame = Vec::new();
        frame.extend_from_slice(&(total_length as u32).to_be_bytes());
        frame.extend_from_slice(&(headers.len() as u32).to_be_bytes());
        frame.extend_from_slice(&0_u32.to_be_bytes());
        frame.extend_from_slice(&headers);
        frame.extend_from_slice(payload_bytes);
        frame.extend_from_slice(&0_u32.to_be_bytes());
        frame
    }

    struct TestEnvGuard {
        previous_password: Option<String>,
        previous_provider: Option<String>,
        previous_store_path: Option<String>,
        previous_api_key: Option<String>,
        temp_dir: PathBuf,
    }

    impl TestEnvGuard {
        fn new(name: &str) -> Self {
            let temp_dir = env::temp_dir().join(format!(
                "any2api-rust-{name}-{}",
                TEST_COUNTER.fetch_add(1, Ordering::Relaxed)
            ));
            fs::create_dir_all(&temp_dir).expect("create rust test temp dir");
            let guard = Self {
                previous_password: env::var("NEWPLATFORM2API_ADMIN_PASSWORD").ok(),
                previous_provider: env::var("NEWPLATFORM2API_DEFAULT_PROVIDER").ok(),
                previous_store_path: env::var("NEWPLATFORM2API_ADMIN_STORE_PATH").ok(),
                previous_api_key: env::var("NEWPLATFORM2API_API_KEY").ok(),
                temp_dir,
            };
            env::set_var("NEWPLATFORM2API_ADMIN_PASSWORD", "changeme");
            env::set_var("NEWPLATFORM2API_DEFAULT_PROVIDER", "cursor");
            env::set_var(
                "NEWPLATFORM2API_ADMIN_STORE_PATH",
                guard.store_path().to_string_lossy().to_string(),
            );
            env::set_var("NEWPLATFORM2API_API_KEY", "");
            admin_sessions().clear();
            admin_store().sync_from_env();
            guard
        }

        fn store_path(&self) -> PathBuf {
            self.temp_dir.join("admin.json")
        }

        fn reload(&self) {
            admin_sessions().clear();
            admin_store().sync_from_env();
        }
    }

    impl Drop for TestEnvGuard {
        fn drop(&mut self) {
            admin_sessions().clear();
            match &self.previous_password {
                Some(value) => env::set_var("NEWPLATFORM2API_ADMIN_PASSWORD", value),
                None => env::remove_var("NEWPLATFORM2API_ADMIN_PASSWORD"),
            }
            match &self.previous_provider {
                Some(value) => env::set_var("NEWPLATFORM2API_DEFAULT_PROVIDER", value),
                None => env::remove_var("NEWPLATFORM2API_DEFAULT_PROVIDER"),
            }
            match &self.previous_store_path {
                Some(value) => env::set_var("NEWPLATFORM2API_ADMIN_STORE_PATH", value),
                None => env::remove_var("NEWPLATFORM2API_ADMIN_STORE_PATH"),
            }
            match &self.previous_api_key {
                Some(value) => env::set_var("NEWPLATFORM2API_API_KEY", value),
                None => env::remove_var("NEWPLATFORM2API_API_KEY"),
            }
            let _ = fs::remove_dir_all(&self.temp_dir);
        }
    }

    #[test]
    fn admin_meta_endpoint() {
        let _serial = test_lock().lock().expect("test lock poisoned");
        let _env = TestEnvGuard::new("meta");

        let response = handle_request(build_request(
            "GET",
            "/api/admin/meta",
            &[("Origin", "http://localhost:1420")],
            "",
        ));
        assert!(response.starts_with("HTTP/1.1 200 OK"));
        assert!(response.contains("\"language\":\"rust\""));
        assert!(response.contains("\"mode\":\"session_cookie\""));
        assert!(response.contains("\"providers\":true"));
        assert!(response.contains("\"credentials\":true"));
        assert!(response.contains("\"providerState\":true"));
        assert!(response.contains("Access-Control-Allow-Origin: http://localhost:1420"));
    }

    #[test]
    fn admin_login_session_logout_lifecycle() {
        let _serial = test_lock().lock().expect("test lock poisoned");
        let _env = TestEnvGuard::new("auth");

        let bad_login = handle_request(build_request(
            "POST",
            "/api/admin/auth/login",
            &[],
            "{\"password\":\"wrong\"}",
        ));
        assert!(bad_login.starts_with("HTTP/1.1 401 Unauthorized"));

        let login = handle_request(build_request(
            "POST",
            "/api/admin/auth/login",
            &[],
            "{\"password\":\"changeme\"}",
        ));
        assert!(login.starts_with("HTTP/1.1 200 OK"));
        assert!(login.contains("Set-Cookie: newplatform2api_admin_session="));
        let token = response_token(&login).expect("missing token in login response");

        let session = handle_request(build_request(
            "GET",
            "/api/admin/auth/session",
            &[("Authorization", &format!("Bearer {token}"))],
            "",
        ));
        assert!(session.starts_with("HTTP/1.1 200 OK"));
        assert!(session.contains("\"authenticated\":true"));
        assert!(session.contains("\"role\":\"admin\""));

        let logout = handle_request(build_request(
            "POST",
            "/api/admin/auth/logout",
            &[("Authorization", &format!("Bearer {token}"))],
            "",
        ));
        assert!(logout.starts_with("HTTP/1.1 200 OK"));
        assert!(logout.contains("\"ok\":true"));

        let session_after = handle_request(build_request(
            "GET",
            "/api/admin/auth/session",
            &[("Authorization", &format!("Bearer {token}"))],
            "",
        ));
        assert!(session_after.starts_with("HTTP/1.1 401 Unauthorized"));
        assert!(session_after.contains("admin login required"));
    }

    #[test]
    fn admin_api_requires_auth_and_supports_cors() {
        let _serial = test_lock().lock().expect("test lock poisoned");
        let _env = TestEnvGuard::new("cors");

        let unauth = handle_request(build_request("GET", "/admin/api/settings", &[], ""));
        assert!(unauth.starts_with("HTTP/1.1 401 Unauthorized"));
        assert!(unauth.contains("admin login required"));

        let options = handle_request(build_request(
            "OPTIONS",
            "/admin/api/settings",
            &[("Origin", "http://localhost:1420")],
            "",
        ));
        assert!(options.starts_with("HTTP/1.1 204 No Content"));
        assert!(options.contains("Access-Control-Allow-Origin: http://localhost:1420"));
        assert!(options.contains("Access-Control-Allow-Methods: GET, POST, PUT, OPTIONS"));
    }

    #[test]
    fn settings_and_provider_roundtrip_persist_across_reload() {
        let _serial = test_lock().lock().expect("test lock poisoned");
        let env_guard = TestEnvGuard::new("roundtrip");

        let login = handle_request(build_request(
            "POST",
            "/api/admin/auth/login",
            &[],
            "{\"password\":\"changeme\"}",
        ));
        let token = response_token(&login).expect("missing token in login response");
        let auth_header = format!("Bearer {token}");

        let settings = handle_request(build_request(
            "GET",
            "/admin/api/settings",
            &[("Authorization", auth_header.as_str())],
            "",
        ));
        assert!(settings.starts_with("HTTP/1.1 200 OK"));
        assert!(settings.contains("\"defaultProvider\":\"cursor\""));
        assert!(settings.contains("\"adminPasswordConfigured\":true"));

        let updated_settings = handle_request(build_request(
            "PUT",
            "/admin/api/settings",
            &[("Authorization", auth_header.as_str())],
            "{\"apiKey\":\"sk-rust\",\"defaultProvider\":\"grok\",\"adminPassword\":\"newpass\"}",
        ));
        assert!(updated_settings.starts_with("HTTP/1.1 200 OK"));
        assert!(updated_settings.contains("\"apiKey\":\"sk-rust\""));
        assert!(updated_settings.contains("\"defaultProvider\":\"grok\""));

        let cursor = handle_request(build_request(
            "PUT",
            "/admin/api/providers/cursor/config",
            &[("Authorization", auth_header.as_str())],
            "{\"config\":{\"apiUrl\":\"https://cursor.test/chat\",\"cookie\":\"cursor-cookie\"}}",
        ));
        assert!(cursor.starts_with("HTTP/1.1 200 OK"));
        assert!(cursor.contains("\"apiUrl\":\"https://cursor.test/chat\""));
        assert!(cursor.contains("\"cookie\":\"cursor-cookie\""));

        let kiro = handle_request(build_request(
            "PUT",
            "/admin/api/providers/kiro/accounts",
            &[("Authorization", auth_header.as_str())],
            "{\"accounts\":[{\"name\":\"Main\",\"accessToken\":\"ak-1\",\"machineId\":\"machine-1\",\"active\":true},{\"name\":\"Backup\",\"accessToken\":\"ak-2\",\"machineId\":\"machine-2\",\"active\":true}]}",
        ));
        assert!(kiro.starts_with("HTTP/1.1 200 OK"));
        assert!(kiro.contains("\"name\":\"Main\""));
        assert!(kiro.contains("\"name\":\"Backup\""));
        assert!(kiro.contains("\"active\":true"));
        assert!(kiro.contains("\"active\":false"));
        assert!(kiro.contains("\"id\":\"kiro-1\""));

        let grok = handle_request(build_request(
            "PUT",
            "/admin/api/providers/grok/tokens",
            &[("Authorization", auth_header.as_str())],
            "{\"tokens\":[{\"name\":\"Primary\",\"cookieToken\":\"gt-1\",\"active\":false},{\"name\":\"Secondary\",\"cookieToken\":\"gt-2\",\"active\":true}]}",
        ));
        assert!(grok.starts_with("HTTP/1.1 200 OK"));
        assert!(grok.contains("\"name\":\"Primary\""));
        assert!(grok.contains("\"name\":\"Secondary\""));
        assert!(grok.contains("\"active\":true"));

        let orchids = handle_request(build_request(
            "PUT",
            "/admin/api/providers/orchids/config",
            &[("Authorization", auth_header.as_str())],
            "{\"config\":{\"clientCookie\":\"orchids-cookie\",\"projectId\":\"project-1\",\"agentMode\":\"claude-sonnet-4.5\"}}",
        ));
        assert!(orchids.starts_with("HTTP/1.1 200 OK"));
        assert!(orchids.contains("\"clientCookie\":\"orchids-cookie\""));

        let status = handle_request(build_request(
            "GET",
            "/admin/api/status",
            &[("Authorization", auth_header.as_str())],
            "",
        ));
        assert!(status.starts_with("HTTP/1.1 200 OK"));
        assert!(status.contains("\"project\":\"any2api-rust\""));
        assert!(status.contains("\"defaultProvider\":\"grok\""));
        assert!(status.contains("\"cursor\":{\"count\":1,\"configured\":true,\"active\":\"default\"}"));
        assert!(status.contains("\"kiro\":{\"count\":2,\"configured\":true,\"active\":\"kiro-1\"}"));
        assert!(status.contains("\"grok\":{\"count\":2,\"configured\":true,\"active\":\"grok-2\"}"));
        assert!(status.contains("\"orchids\":{\"count\":1,\"configured\":true,\"active\":\"default\"}"));

        env_guard.reload();

        let relogin_bad = handle_request(build_request(
            "POST",
            "/api/admin/auth/login",
            &[],
            "{\"password\":\"changeme\"}",
        ));
        assert!(relogin_bad.starts_with("HTTP/1.1 401 Unauthorized"));

        let login_again = handle_request(build_request(
            "POST",
            "/api/admin/auth/login",
            &[],
            "{\"password\":\"newpass\"}",
        ));
        let token_again = response_token(&login_again).expect("missing token in second login response");
        let auth_header_again = format!("Bearer {token_again}");

        let persisted_settings = handle_request(build_request(
            "GET",
            "/admin/api/settings",
            &[("Authorization", auth_header_again.as_str())],
            "",
        ));
        assert!(persisted_settings.starts_with("HTTP/1.1 200 OK"));
        assert!(persisted_settings.contains("\"apiKey\":\"sk-rust\""));
        assert!(persisted_settings.contains("\"defaultProvider\":\"grok\""));

        let persisted_kiro = handle_request(build_request(
            "GET",
            "/admin/api/providers/kiro/accounts",
            &[("Authorization", auth_header_again.as_str())],
            "",
        ));
        assert!(persisted_kiro.starts_with("HTTP/1.1 200 OK"));
        assert!(persisted_kiro.contains("\"name\":\"Main\""));
        assert!(persisted_kiro.contains("\"name\":\"Backup\""));

        let models = handle_request(build_request("GET", "/v1/models?provider=grok", &[], ""));
        assert!(models.starts_with("HTTP/1.1 200 OK"));
        assert!(models.contains("\"provider\":\"grok\""));
        assert!(models.contains("\"id\":\"grok-4\""));
    }

    #[test]
    fn openai_chat_uses_grok_provider_real_upstream_shape() {
        let _serial = test_lock().lock().expect("test lock poisoned");
        let _env = TestEnvGuard::new("grok-upstream");

        admin_store()
            .replace_grok_tokens(vec![GrokToken {
                id: "grok-1".to_string(),
                name: "Main".to_string(),
                cookie_token: "cookie-token".to_string(),
                active: true,
            }])
            .expect("store grok token");

        let mock_url = spawn_mock_server(1, |request| {
            assert_eq!(request.method, "POST");
            assert_eq!(request.path, "/grok");
            assert!(request.body.contains("\"message\":\"reply exactly OK\""));
            text_mock_response(
                "200 OK",
                "application/json",
                "{\"result\":{\"response\":{\"token\":\"OK\"}}}\n",
            )
        });
        let _grok_api = EnvVarGuard::set("NEWPLATFORM2API_GROK_API_URL", format!("{mock_url}/grok"));

        let completion = handle_request(build_request(
            "POST",
            "/v1/chat/completions",
            &[],
            "{\"provider\":\"grok\",\"model\":\"grok-4\",\"messages\":[{\"role\":\"user\",\"content\":\"reply exactly OK\"}]}",
        ));
        assert!(completion.starts_with("HTTP/1.1 200 OK"));
        assert!(completion.contains("X-Newplatform2API-Provider: grok"));
        assert!(completion.contains("\"content\":\"OK\""));
    }

    #[test]
    fn openai_chat_uses_cursor_provider_real_upstream_shape() {
        let _serial = test_lock().lock().expect("test lock poisoned");
        let _env = TestEnvGuard::new("cursor-upstream");

        let mock_url = spawn_mock_server(1, |request| {
            assert_eq!(request.method, "POST");
            assert_eq!(request.path, "/cursor");
            assert_eq!(request.header("x-is-human"), Some("human-token"));
            assert_eq!(request.header("cookie"), Some("cursor-cookie"));
            assert!(request.body.contains("\"trigger\":\"submit-message\""));
            assert!(request.body.contains("anthropic/claude-sonnet-4.6"));
            assert!(request.body.contains("reply exactly OK"));
            text_mock_response("200 OK", "application/json", "{\"text\":\"OK\"}")
        });

        admin_store()
            .replace_cursor_config(CursorRuntimeConfig {
                api_url: format!("{mock_url}/cursor"),
                script_url: format!("{mock_url}/script"),
                cookie: "cursor-cookie".to_string(),
                x_is_human: "human-token".to_string(),
                user_agent: String::new(),
                referer: String::new(),
                webgl_vendor: String::new(),
                webgl_renderer: String::new(),
            })
            .expect("store cursor config");

        let completion = handle_request(build_request(
            "POST",
            "/v1/chat/completions",
            &[],
            "{\"provider\":\"cursor\",\"model\":\"claude-sonnet-4.6\",\"messages\":[{\"role\":\"user\",\"content\":\"reply exactly OK\"}]}",
        ));
        assert!(completion.starts_with("HTTP/1.1 200 OK"));
        assert!(completion.contains("X-Newplatform2API-Provider: cursor"));
        assert!(completion.contains("\"content\":\"OK\""));
    }

    #[test]
    fn openai_chat_uses_kiro_provider_real_upstream_shape() {
        let _serial = test_lock().lock().expect("test lock poisoned");
        let _env = TestEnvGuard::new("kiro-upstream");

        admin_store()
            .replace_kiro_accounts(vec![KiroAccount {
                id: "kiro-1".to_string(),
                name: "Main".to_string(),
                access_token: "kiro-token".to_string(),
                machine_id: "machine-1".to_string(),
                preferred_endpoint: "codewhisperer".to_string(),
                active: true,
            }])
            .expect("store kiro account");

        let frame = kiro_frame(
            "assistantResponseEvent",
            "{\"content\":\"OK\"}",
        );
        let mock_url = spawn_mock_server(1, move |request| {
            assert_eq!(request.method, "POST");
            assert_eq!(request.path, "/kiro");
            assert!(request.body.contains("reply exactly OK"));
            MockResponse {
                status: "200 OK",
                content_type: "application/octet-stream",
                headers: vec![],
                body: frame.clone(),
            }
        });
        let _kiro_api = EnvVarGuard::set(
            "NEWPLATFORM2API_KIRO_CODEWHISPERER_URL",
            format!("{mock_url}/kiro"),
        );
        let _kiro_alt = EnvVarGuard::set(
            "NEWPLATFORM2API_KIRO_AMAZONQ_URL",
            format!("{mock_url}/kiro-alt"),
        );

        let completion = handle_request(build_request(
            "POST",
            "/v1/chat/completions",
            &[],
            "{\"provider\":\"kiro\",\"model\":\"claude-sonnet-4.6\",\"messages\":[{\"role\":\"user\",\"content\":\"reply exactly OK\"}]}",
        ));
        assert!(completion.starts_with("HTTP/1.1 200 OK"));
        assert!(completion.contains("X-Newplatform2API-Provider: kiro"));
        assert!(completion.contains("\"content\":\"OK\""));
    }

    #[test]
    fn anthropic_messages_use_orchids_provider_real_upstream_shape() {
        let _serial = test_lock().lock().expect("test lock poisoned");
        let _env = TestEnvGuard::new("orchids-upstream");

        admin_store()
            .replace_orchids_config(OrchidsRuntimeConfig {
                api_url: String::new(),
                clerk_url: String::new(),
                client_cookie: "client-cookie".to_string(),
                client_uat: "123".to_string(),
                session_id: String::new(),
                project_id: "project-1".to_string(),
                user_id: String::new(),
                email: String::new(),
                agent_mode: "claude-sonnet-4.5".to_string(),
            })
            .expect("store orchids config");

        let mock_url = spawn_mock_server(3, |request| {
            if request.path.starts_with("/v1/client/sessions/sess-1/tokens") {
                return text_mock_response(
                    "200 OK",
                    "application/json",
                    "{\"jwt\":\"orchids-jwt\"}",
                );
            }
            if request.path.starts_with("/v1/client") {
                return text_mock_response(
                    "200 OK",
                    "application/json",
                    "{\"response\":{\"last_active_session_id\":\"sess-1\",\"sessions\":[{\"user\":{\"id\":\"user-1\",\"email_addresses\":[{\"email_address\":\"u@example.com\"}]}}]}}",
                );
            }
            assert_eq!(request.path, "/agent");
            assert!(request.body.contains("reply exactly OK"));
            text_mock_response(
                "200 OK",
                "text/event-stream",
                "data: {\"type\":\"model\",\"event\":{\"type\":\"text-delta\",\"delta\":\"OK\"}}\n\n",
            )
        });
        let _orchids_api = EnvVarGuard::set("NEWPLATFORM2API_ORCHIDS_API_URL", format!("{mock_url}/agent"));
        let _orchids_clerk = EnvVarGuard::set("NEWPLATFORM2API_ORCHIDS_CLERK_URL", mock_url.clone());

        let completion = handle_request(build_request(
            "POST",
            "/v1/messages",
            &[],
            "{\"provider\":\"orchids\",\"model\":\"claude-sonnet-4.5\",\"messages\":[{\"role\":\"user\",\"content\":\"reply exactly OK\"}]}",
        ));
        assert!(completion.starts_with("HTTP/1.1 200 OK"));
        assert!(completion.contains("X-Newplatform2API-Provider: orchids"));
        assert!(completion.contains("\"text\":\"OK\""));
    }
}