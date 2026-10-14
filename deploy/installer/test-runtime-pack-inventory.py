import importlib.util
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("inventory", Path(__file__).with_name("runtime-pack-inventory.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class InventoryTests(unittest.TestCase):
    def test_manifest_creates_unapproved_inventory(self):
        root = Path(__file__).resolve().parents[2]
        report = module.inventory(root)
        self.assertEqual(report["manifest_type"], "template")
        self.assertGreater(len(report["artifacts"]), 30)
        self.assertFalse(report["release_eligible"])
        for item in report["artifacts"]:
            self.assertFalse(item["redistribution_approved"])
            self.assertFalse(item["provenance_verified"])
            self.assertFalse(item["artifact_verified"])

if __name__ == "__main__":
    unittest.main()
