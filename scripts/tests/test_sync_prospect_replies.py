"""Tests for which inbox senders scripts/sync-prospect-replies.py treats as prospect replies.

Run: python3 -I scripts/tests/test_sync_prospect_replies.py
"""
import importlib.util
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("sync_replies", Path(__file__).resolve().parent.parent / "sync-prospect-replies.py")
sync = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(sync)


class WatchKeysTest(unittest.TestCase):
    def test_company_domains_personal_addresses_and_not_us(self):
        contacts = [
            ("fc-pc-basalt", "ws", "Basalt", "Ben@Basalt.Health"),
            ("fc-pc-solo", "ws", "Solo", "founder.solo@gmail.com"),
            ("fc-pc-us", "ws", "TFG", "nader@thefoundersgroup.net"),
            ("fc-pc-admin", "ws", "Admin", "thefoundersgroupadmin@gmail.com"),
        ]
        keys = sync.watch_keys(contacts, {"thefoundersgroupadmin@gmail.com"})
        self.assertEqual(sorted(keys), ["basalt.health", "founder.solo@gmail.com"])
        self.assertEqual(keys["basalt.health"][0], "fc-pc-basalt")


if __name__ == "__main__":
    unittest.main()
