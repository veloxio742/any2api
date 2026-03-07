use std::env::consts::OS;
use std::sync::atomic::{AtomicU64, Ordering};

static HEADER_COUNTER: AtomicU64 = AtomicU64::new(1);

#[derive(Clone)]
pub struct CursorBrowserProfile {
    pub platform: &'static str,
    pub platform_version: &'static str,
    pub architecture: &'static str,
    pub bitness: &'static str,
    pub chrome_version: u32,
    pub user_agent: String,
}

pub struct CursorHeaderGenerator {
    profile: CursorBrowserProfile,
}

impl CursorHeaderGenerator {
    pub fn new() -> Self {
        let mut generator = Self {
            profile: CursorBrowserProfile {
                platform: "Windows",
                platform_version: "10.0.0",
                architecture: "x86",
                bitness: "64",
                chrome_version: 130,
                user_agent: String::new(),
            },
        };
        generator.refresh();
        generator
    }

    pub fn chat_headers(&self, x_is_human: &str) -> Vec<(String, String)> {
        let lang = choose(&["en-US,en;q=0.9", "zh-CN,zh;q=0.9,en;q=0.8", "en-GB,en;q=0.9"]);
        let referer = choose(&[
            "https://cursor.com/en-US/learn/how-ai-models-work",
            "https://cursor.com/cn/learn/how-ai-models-work",
            "https://cursor.com/",
        ]);
        let mut headers = vec![
            (
                "sec-ch-ua-platform".to_string(),
                format!("\"{}\"", self.profile.platform),
            ),
            ("x-path".to_string(), "/api/chat".to_string()),
            ("Referer".to_string(), referer.clone()),
            ("referer".to_string(), referer),
            ("sec-ch-ua".to_string(), self.sec_ch_ua()),
            ("x-method".to_string(), "POST".to_string()),
            ("sec-ch-ua-mobile".to_string(), "?0".to_string()),
            ("x-is-human".to_string(), x_is_human.to_string()),
            ("User-Agent".to_string(), self.profile.user_agent.clone()),
            ("content-type".to_string(), "application/json".to_string()),
            ("accept-language".to_string(), lang),
        ];
        self.add_optional_headers(&mut headers);
        headers
    }

    pub fn script_headers(&self) -> Vec<(String, String)> {
        let lang = choose(&["en-US,en;q=0.9", "zh-CN,zh;q=0.9,en;q=0.8", "en-GB,en;q=0.9"]);
        let referer = choose(&[
            "https://cursor.com/cn/learn/how-ai-models-work",
            "https://cursor.com/en-US/learn/how-ai-models-work",
            "https://cursor.com/",
        ]);
        let mut headers = vec![
            ("User-Agent".to_string(), self.profile.user_agent.clone()),
            (
                "sec-ch-ua-arch".to_string(),
                format!("\"{}\"", self.profile.architecture),
            ),
            (
                "sec-ch-ua-platform".to_string(),
                format!("\"{}\"", self.profile.platform),
            ),
            ("sec-ch-ua".to_string(), self.sec_ch_ua()),
            (
                "sec-ch-ua-bitness".to_string(),
                format!("\"{}\"", self.profile.bitness),
            ),
            ("sec-ch-ua-mobile".to_string(), "?0".to_string()),
            ("sec-fetch-site".to_string(), "same-origin".to_string()),
            ("sec-fetch-mode".to_string(), "no-cors".to_string()),
            ("sec-fetch-dest".to_string(), "script".to_string()),
            ("Referer".to_string(), referer.clone()),
            ("referer".to_string(), referer),
            ("accept-language".to_string(), lang),
        ];
        if !self.profile.platform_version.is_empty() {
            headers.push((
                "sec-ch-ua-platform-version".to_string(),
                format!("\"{}\"", self.profile.platform_version),
            ));
        }
        headers
    }

    pub fn profile(&self) -> CursorBrowserProfile {
        self.profile.clone()
    }

    pub fn refresh(&mut self) {
        let profiles = match OS {
            "darwin" => vec![
                ("macOS", "13.0.0", "arm", "64"),
                ("macOS", "14.0.0", "arm", "64"),
                ("macOS", "15.0.0", "arm", "64"),
                ("macOS", "13.0.0", "x86", "64"),
                ("macOS", "14.0.0", "x86", "64"),
            ],
            "linux" => vec![("Linux", "", "x86", "64")],
            _ => vec![
                ("Windows", "10.0.0", "x86", "64"),
                ("Windows", "11.0.0", "x86", "64"),
                ("Windows", "15.0.0", "x86", "64"),
            ],
        };
        let chrome_versions = [120, 121, 122, 123, 124, 125, 126, 127, 128, 129, 130];
        let (platform, platform_version, architecture, bitness) =
            profiles[next_index(profiles.len())];
        let chrome_version = chrome_versions[next_index(chrome_versions.len())];
        self.profile = CursorBrowserProfile {
            platform,
            platform_version,
            architecture,
            bitness,
            chrome_version,
            user_agent: cursor_user_agent(platform, chrome_version),
        };
    }

    fn add_optional_headers(&self, headers: &mut Vec<(String, String)>) {
        if !self.profile.architecture.is_empty() {
            headers.push((
                "sec-ch-ua-arch".to_string(),
                format!("\"{}\"", self.profile.architecture),
            ));
        }
        if !self.profile.bitness.is_empty() {
            headers.push((
                "sec-ch-ua-bitness".to_string(),
                format!("\"{}\"", self.profile.bitness),
            ));
        }
        if !self.profile.platform_version.is_empty() {
            headers.push((
                "sec-ch-ua-platform-version".to_string(),
                format!("\"{}\"", self.profile.platform_version),
            ));
        }
    }

    fn sec_ch_ua(&self) -> String {
        let not_a_brand = 24 + (next_index(10) as u32);
        format!(
            "\"Google Chrome\";v=\"{}\", \"Chromium\";v=\"{}\", \"Not(A:Brand\";v=\"{}\"",
            self.profile.chrome_version, self.profile.chrome_version, not_a_brand
        )
    }
}

fn choose(values: &[&str]) -> String {
    if values.is_empty() {
        return String::new();
    }
    values[next_index(values.len())].to_string()
}

fn next_index(len: usize) -> usize {
    if len <= 1 {
        return 0;
    }
    (HEADER_COUNTER.fetch_add(1, Ordering::Relaxed) as usize) % len
}

fn cursor_user_agent(platform: &str, chrome_version: u32) -> String {
    match platform {
        "Windows" => format!(
            "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/{}.0.0.0 Safari/537.36",
            chrome_version
        ),
        "macOS" => format!(
            "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/{}.0.0.0 Safari/537.36",
            chrome_version
        ),
        "Linux" => format!(
            "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/{}.0.0.0 Safari/537.36",
            chrome_version
        ),
        _ => format!(
            "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/{}.0.0.0 Safari/537.36",
            chrome_version
        ),
    }
}