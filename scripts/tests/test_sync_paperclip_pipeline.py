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
    def test_command_prospects_are_scoped_without_changing_historical_ids(self):
        self.assertEqual(sync.prospect_id("vantage", "Acme"), "fc-pc-acme")
        self.assertEqual(sync.prospect_id("founderos", "Acme"), "fc-pc-founderos-acme")
        self.assertNotEqual(sync.prospect_id("client-a", "Acme"), sync.prospect_id("client-b", "Acme"))

    def test_venture_is_a_real_venture_for_any_command_workspace(self):
        # The funnel only accepts vantage/launchpad-cohort; an HQ ("founderos")
        # command rolled back every sync from 31 Oct.
        self.assertEqual(sync.venture_for("vantage"), "vantage")
        self.assertEqual(sync.venture_for("launchpad-cohort"), "launchpad-cohort")
        self.assertEqual(sync.venture_for("founderos"), "vantage")
        self.assertEqual(sync.venture_for("workspace-abc"), "vantage")

    def test_reads_a_fenced_json_array(self):
        got, bad = sync.json_prospects("New accounts today:\n```json\n[" + GOOD + "]\n```\n")
        self.assertEqual(bad, 0)
        self.assertEqual(got[0]["name"], "OuterSignal")
        self.assertEqual(got[0]["url"], "https://outersignal.com")
        self.assertEqual(got[0]["buyer"], "Zach Zelner")
        self.assertEqual(got[0]["buyerRole"], "CEO")
        self.assertEqual(got[0]["evidence"], "Series A, $22m, Sep 2026")
        self.assertEqual(got[0]["location"], "New York")
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


class PodRoutingTest(unittest.TestCase):
    """Client pods: research from a pod's board project lands in that pod's
    pipeline as venture 'client' selling the client's product; nothing else does."""
    PODS = {"proj-bv": {"project_id": "proj-bv", "slug": "client-brightvale", "client_name": "Brightvale"}}

    def test_a_pod_project_task_targets_the_pod(self):
        self.assertEqual(sync.target_for({"id": "i1", "projectId": "proj-bv"}, self.PODS, {"i1": "founderos"}), "client-brightvale")
        self.assertEqual(sync.target_for({"id": "i2", "projectId": "x"}, self.PODS, {"i2": "founderos"}), "founderos")
        self.assertEqual(sync.target_for({"id": "i3"}, self.PODS, {}), sync.WORKSPACE_SLUG)

    def test_pod_contacts_are_client_venture_with_the_clients_product(self):
        slugs = {"client-brightvale": "Brightvale"}
        self.assertEqual(sync.venture_for("client-brightvale", slugs), "client")
        self.assertEqual(sync.product_for("client-brightvale", slugs), "Brightvale")
        self.assertEqual(sync.venture_for("vantage", slugs), "vantage")
        self.assertEqual(sync.product_for("vantage", slugs), sync.PRODUCT)

    def test_a_failed_pod_lookup_stops_the_import(self):
        # Codex M1 review #6: never fall back to HQ when pods cannot be read.
        with self.assertRaises(SystemExit):
            sync.load_pods(lambda: (1, "", "connection refused"))
        self.assertEqual(sync.load_pods(lambda: (0, "[]", "")), {})

    def test_pod_documents_never_feed_hq_addresses(self):
        self.assertFalse(sync.scan_for_hq_addresses({"id": "i1", "projectId": "proj-bv"}, self.PODS))
        self.assertTrue(sync.scan_for_hq_addresses({"id": "i2", "projectId": "other"}, self.PODS))


if __name__ == "__main__":
    unittest.main()
