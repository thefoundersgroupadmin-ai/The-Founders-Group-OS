"""Tests for the prospects document parser in scripts/sync-paperclip-pipeline.py.

Run: python3 -I scripts/tests/test_sync_paperclip_pipeline.py
"""
import importlib.util
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("sync_pipeline", Path(__file__).resolve().parent.parent / "sync-paperclip-pipeline.py")
sync = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(sync)

GOOD = ('{"company": "OuterSignal", "website": "https://outersignal.com", "location": "New York", '
        '"buyer": "Zach Zelner", "buyerRole": "CEO", "email": "zach@outersignal.com", "score": 82, '
        '"evidence": "Series A, $22m, Sep 2026", "nextAction": "Day-3 follow-up"}')


class JsonProspectsTest(unittest.TestCase):
    def test_reads_a_fenced_json_array(self):
        got, bad = sync.json_prospects("New accounts today:\n```json\n[" + GOOD + "]\n```\n")
        self.assertEqual(bad, 0)
        self.assertEqual(got[0]["name"], "OuterSignal")
        self.assertEqual(got[0]["url"], "https://outersignal.com")
        self.assertEqual(got[0]["buyer"], "Zach Zelner")
        self.assertEqual(got[0]["email"], "zach@outersignal.com")
        self.assertEqual(got[0]["score"], 82)
        self.assertEqual(got[0]["next"], "Day-3 follow-up")

    def test_skips_entries_without_a_company_and_counts_them(self):
        got, bad = sync.json_prospects('[{"buyer": "Nobody"}, "nope", ' + GOOD + "]")
        self.assertEqual([a["name"] for a in got], ["OuterSignal"])
        self.assertEqual(bad, 2)

    def test_drops_bad_emails_scores_and_our_own_address(self):
        got, _ = sync.json_prospects('[{"company": "A", "email": "not-an-email", "score": "high"},'
                                     ' {"company": "B", "email": "nader@thefoundersgroup.net", "score": 140}]')
        self.assertEqual([(a["email"], a["score"]) for a in got], [(None, None), (None, None)])

    def test_unknown_buyer_is_none(self):
        got, _ = sync.json_prospects('[{"company": "A", "buyer": "Unknown - verify"}]')
        self.assertIsNone(got[0]["buyer"])

    def test_not_json_is_an_error(self):
        with self.assertRaises(ValueError):
            sync.json_prospects("| a | table |")


class AddressMatchTest(unittest.TestCase):
    def test_a_name_inside_another_word_is_not_a_match(self):
        row = "Sandstone | enterprise sales lead | team@sandstone.com"
        self.assertFalse(sync.address_for_company("Rise", row, "team@sandstone.com"))
        self.assertTrue(sync.address_for_company("Sandstone", row, "team@sandstone.com"))

    def test_the_domain_must_belong_to_the_company(self):
        row = "Common Method | Thomas Evans | hello@commonmethod.co.uk | cc ben@basalt.health"
        self.assertTrue(sync.address_for_company("Common Method", row, "hello@commonmethod.co.uk"))
        self.assertFalse(sync.address_for_company("Common Method", row, "ben@basalt.health"))

    def test_short_domains_inside_longer_names_match(self):
        self.assertTrue(sync.address_for_company("Whirl AI", "Whirl AI | hello@whirlai.com", "hello@whirlai.com"))
        self.assertTrue(sync.address_for_company("Basalt Health", "Basalt Health | ben@basalt.health", "ben@basalt.health"))

    def test_a_shortened_domain_matches_on_the_first_word(self):
        row = "Polyphasic Developers | info@polyphasicdevs.com"
        self.assertTrue(sync.address_for_company("Polyphasic Developers", row, "info@polyphasicdevs.com"))


if __name__ == "__main__":
    unittest.main()
