use crate::types::{ModelInfo, ProviderCapabilities, UnifiedRequest};

pub trait Provider: Send + Sync {
    fn id(&self) -> &'static str;
    fn capabilities(&self) -> ProviderCapabilities;
    fn models(&self) -> Vec<ModelInfo>;
    fn build_upstream_preview(&self, req: &UnifiedRequest) -> String;
    fn generate_reply(&self, req: &UnifiedRequest) -> Result<String, String>;
}

pub struct Registry {
    default_provider: String,
    providers: Vec<Box<dyn Provider>>,
}

impl Registry {
    pub fn new(default_provider: &str) -> Self {
        Self { default_provider: default_provider.to_string(), providers: Vec::new() }
    }

    pub fn register(&mut self, provider: Box<dyn Provider>) {
        self.providers.push(provider);
    }

    pub fn resolve(&self, provider: Option<&str>) -> Result<&dyn Provider, String> {
        let key = provider.unwrap_or(&self.default_provider);
        self.providers
            .iter()
            .find(|item| item.id() == key)
            .map(|item| item.as_ref())
            .ok_or_else(|| format!("unknown provider: {key}"))
    }

    pub fn provider_ids(&self) -> Vec<&'static str> {
        let mut ids = self.providers.iter().map(|item| item.id()).collect::<Vec<_>>();
        ids.sort_unstable();
        ids
    }

    pub fn models(&self, provider: Option<&str>) -> Result<Vec<ModelInfo>, String> {
        if let Some(id) = provider {
            return Ok(self.resolve(Some(id))?.models());
        }
        let mut models = Vec::new();
        let ids = self.provider_ids();
        for id in ids {
            models.extend(self.resolve(Some(id))?.models());
        }
        Ok(models)
    }
}
