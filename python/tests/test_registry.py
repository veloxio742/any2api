import unittest

from providers import default_registry


class RegistryTests(unittest.TestCase):
    def test_registry_contains_four_providers(self):
        registry = default_registry()
        self.assertEqual(registry.provider_ids(), ["cursor", "grok", "kiro", "orchids"])

    def test_provider_specific_models(self):
        registry = default_registry()
        models = registry.models("kiro")
        self.assertEqual(len(models), 1)
        self.assertEqual(models[0]["provider"], "kiro")


if __name__ == "__main__":
    unittest.main()