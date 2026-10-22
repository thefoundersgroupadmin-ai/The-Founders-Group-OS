"""Tests for the outbox document parser in scripts/sync-paperclip-outbox.py.

Run: python3 -I scripts/tests/test_sync_paperclip_outbox.py
"""
import importlib.util
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("sync_outbox", Path(__file__).resolve().parent.parent / "sync-paperclip-outbox.py")
sync = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(sync)

GOOD = '{"to": "info@hitratech.co.uk", "toName": "Eren Can", "company": "Hitratech", "subject": "Hi", "body": "Hello\\tthere\\nNader"}'


class EntriesTest(unittest.TestCase):
    def test_reads_a_fenced_json_array(self):
        got = sync.entries("Drafts for approval:\n```json\n[" + GOOD + "]\n```\n")
        self.assertEqual(got[0]["to"], "info@hitratech.co.uk")
        self.assertEqual(got[0]["body"], "Hello\tthere\nNader")

    def test_reads_a_bare_array(self):
        self.assertEqual(len(sync.entries("[" + GOOD + "]")), 1)

    def test_rejects_non_object_entries(self):
        with self.assertRaisesRegex(ValueError, "entry 2: must be an object"):
            sync.entries("[" + GOOD + ', "oops"]')

    def test_rejects_invalid_addresses_and_empty_content(self):
        for bad in ('{"to": "not-an-email", "subject": "s", "body": "b"}',
                    '{"to": "a@b.co", "subject": "", "body": "b"}',
                    '{"to": "a@b.co", "subject": "s", "body": "   "}',
                    '{"to": "a@b.co", "subject": "' + "x" * 301 + '", "body": "b"}'):
            with self.assertRaises(ValueError, msg=bad):
                sync.entries("[" + bad + "]")

    def test_rejects_documents_that_are_not_arrays(self):
        for bad in ("not json", '{"to": "a@b.co"}'):
            with self.assertRaises(ValueError):
                sync.entries(bad)


class ContactTest(unittest.TestCase):
    def test_contact_id_matches_the_pipeline_sync(self):
        self.assertEqual(sync.contact_id({"company": "Basalt Health", "to": "ben@basalt.health"}), "fc-pc-basalt-health")

    def test_contact_id_falls_back_to_the_domain(self):
        self.assertEqual(sync.contact_id({"company": None, "to": "info@gallatin.ai"}), "fc-pc-gallatin-ai")

    def test_our_own_addresses_are_not_prospects(self):
        own = {"thefoundersgroupadmin@gmail.com"}
        self.assertTrue(sync.is_own("Nader@TheFoundersGroup.net", own))
        self.assertTrue(sync.is_own("TheFoundersGroupAdmin@gmail.com", own))
        self.assertFalse(sync.is_own("ben@basalt.health", own))


if __name__ == "__main__":
    unittest.main()
