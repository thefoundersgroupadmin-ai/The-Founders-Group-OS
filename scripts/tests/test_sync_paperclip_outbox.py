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
    def test_an_already_imported_draft_keeps_its_workspace(self):
        hq = {"workspace_id": "w-hq", "slug": "founderos"}
        vantage = {"workspace_id": "w-v", "slug": "vantage"}
        self.assertEqual(sync.recipient_workspace([hq, vantage], [vantage]), vantage)
        self.assertEqual(sync.recipient_workspace([hq, vantage], []), "ambiguous")
        self.assertEqual(sync.recipient_workspace([hq], []), hq)
        self.assertIsNone(sync.recipient_workspace([], []))  # caller uses the default pipeline

    def test_the_earlier_pipeline_comes_from_the_linked_contact_not_the_outbox_row(self):
        # Codex review, 2 Nov: every outbox row lives in HQ, so reading the
        # row's own workspace sent Vantage drafts to HQ.
        q = sync.imported_pipeline_query("pc-the-14-abc")
        self.assertIn("JOIN founderos_funnel_contacts c ON c.id=o.contact_id", q)
        self.assertIn("w.id=c.workspace_id", q)
        self.assertNotIn("w.id=o.workspace_id", q)
        self.assertIn("'pc-the-14-abc'", q)

    def test_contact_id_matches_the_pipeline_sync(self):
        self.assertEqual(sync.contact_id({"company": "Basalt Health", "to": "ben@basalt.health"}), "fc-pc-basalt-health")

    def test_contact_id_falls_back_to_the_domain(self):
        self.assertEqual(sync.contact_id({"company": None, "to": "info@gallatin.ai"}), "fc-pc-gallatin-ai")

    def test_our_own_addresses_are_not_prospects(self):
        own = {"thefoundersgroupadmin@gmail.com"}
        self.assertTrue(sync.is_own("Nader@TheFoundersGroup.net", own))
        self.assertTrue(sync.is_own("TheFoundersGroupAdmin@gmail.com", own))
        self.assertFalse(sync.is_own("ben@basalt.health", own))


class PodRoutingTest(unittest.TestCase):
    """Client pods (docs/tfg/CLIENT-PODS.md): a task in a pod's board project
    goes to that pod only; HQ tasks never land in a pod."""
    PODS = {"proj-bv": {"project_id": "proj-bv", "workspace_id": "w-bv", "slug": "client-brightvale",
                        "client_name": "Brightvale", "sender_name": "Sam Reed"}}

    def test_a_pod_project_task_routes_to_the_pod_whatever_else_matches(self):
        binding = {"workspace_id": "w-hq", "slug": "founderos"}
        r = sync.route({"id": "i1", "projectId": "proj-bv"}, self.PODS, {"i1": binding}, "w-v")
        self.assertEqual((r["outbox_ws"], r["pipeline_ws"], r["venture"], r["product"]), ("w-bv", "w-bv", "client", "Brightvale"))
        self.assertTrue(r["pod"])
        self.assertEqual(r["sender"], "Sam Reed")

    def test_other_tasks_keep_hq_routing(self):
        r = sync.route({"id": "i2", "projectId": "proj-tfg"}, self.PODS, {}, "w-v")
        self.assertFalse(r["pod"])
        self.assertEqual(r["outbox_ws"], None)  # caller uses HQ
        self.assertEqual(r["pipeline_ws"], "w-v")

    def test_hq_recipient_matching_never_looks_inside_pods(self):
        q = sync.recipient_matches_query("priya@brightvale.example")
        self.assertIn("NOT IN (SELECT workspace_id FROM founderos_client_pods)", q)
        self.assertIn("'priya@brightvale.example'", q)

    def test_pod_draft_ids_name_the_pod_and_hq_ids_are_unchanged(self):
        # Codex M1 review #7: a task moved between pods must not rewrite another pod's draft.
        it = {"to": "a@b.co", "subject": "Hi"}
        self.assertTrue(sync.outbox_id("THE-90", it, "client-brightvale").startswith("pc-client-brightvale-the-90-"))
        self.assertNotEqual(sync.outbox_id("THE-90", it, "client-brightvale"), sync.outbox_id("THE-90", it, "client-kestrel"))
        self.assertTrue(sync.outbox_id("THE-14", it, None).startswith("pc-the-14-"))

    def test_conflicting_updates_stay_in_their_workspace(self):
        self.assertIn("founderos_outbox.workspace_id = EXCLUDED.workspace_id", sync.OUTBOX_UPSERT_GUARD)

    def test_drafts_on_a_reply_task_are_conversation_replies(self):
        self.assertEqual(sync.sequence_key_for({"sequenceKey": "warehouses:1"}, is_reply_task=True), "reply")
        self.assertEqual(sync.sequence_key_for({"sequenceKey": "warehouses:1"}, is_reply_task=False), "warehouses:1")
        self.assertIsNone(sync.sequence_key_for({"sequenceKey": None}, is_reply_task=False))

    def test_unbound_client_projects_are_quarantined(self):
        names = {"proj-bv": "Client: Brightvale\n", "proj-x": "Client: Unknown\n", "proj-tfg": "TFG sales\n",
                 "proj-renamed": "Renamed by hand\nAI SDR work for X (TFG OS pod client-x)"}
        self.assertTrue(sync.quarantined({"projectId": "proj-renamed"}, self.PODS, names))
        self.assertFalse(sync.quarantined({"projectId": "proj-bv"}, self.PODS, names))
        self.assertTrue(sync.quarantined({"projectId": "proj-x"}, self.PODS, names))
        self.assertFalse(sync.quarantined({"projectId": "proj-tfg"}, self.PODS, names))
        self.assertFalse(sync.quarantined({}, self.PODS, names))

    def test_moving_a_task_withdraws_its_waiting_drafts_elsewhere(self):
        q = sync.withdraw_moved_query("THE-90", "w-bv")
        self.assertIn("status = 'rejected'", q)
        self.assertIn("source_ref = 'THE-90'", q)
        self.assertIn("workspace_id <> 'w-bv'", q)
        self.assertIn("status = 'pending'", q)

    def test_sequence_key_is_kept(self):
        got = sync.entries('[{"to": "a@b.co", "subject": "s", "body": "b", "sequenceKey": "warehouses:1"}]')
        self.assertEqual(got[0]["sequenceKey"], "warehouses:1")
        with self.assertRaises(ValueError):
            sync.entries('[{"to": "a@b.co", "subject": "s", "body": "b", "sequenceKey": "' + "x" * 101 + '"}]')


if __name__ == "__main__":
    unittest.main()
