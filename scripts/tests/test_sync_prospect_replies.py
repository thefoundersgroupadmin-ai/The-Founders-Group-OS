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


class PodRepliesTest(unittest.TestCase):
    """Client pods (docs/tfg/CLIENT-PODS.md): TFG's inbox never handles a pod's
    contacts; each pod's own mailbox does, with that pod's context only."""
    POD = {"slug": "client-brightvale", "client_name": "Brightvale", "sender_name": "Sam Reed",
           "workspace_id": "w-bv", "agent_id": "ag-bv", "project_id": "proj-bv"}

    def test_hq_contacts_exclude_pods_and_pod_contacts_are_only_the_pods(self):
        self.assertIn("NOT IN (SELECT workspace_id FROM founderos_client_pods)", sync.HQ_CONTACTS_QUERY)
        q = sync.pod_contacts_query("w-bv")
        self.assertIn("workspace_id='w-bv'", q)
        self.assertNotIn("NOT IN", q)

    def test_pod_mailbox_keys(self):
        env = {"POD_CLIENT_BRIGHTVALE_HOST": "imap.bv.example", "POD_CLIENT_BRIGHTVALE_USER": "sam@bv.example",
               "POD_CLIENT_BRIGHTVALE_PASS": "p", "INBOX_1_HOST": "imap.tfg"}
        self.assertEqual(sync.pod_mailbox(env, "client-brightvale"),
                         {"host": "imap.bv.example", "port": 993, "user": "sam@bv.example", "pass": "p"})
        self.assertIsNone(sync.pod_mailbox(env, "client-other"))

    def test_a_reply_matches_its_exact_sender_and_engages_the_whole_company(self):
        # Codex M1 review #8: a colleague's reply stops follow-ups to everyone there.
        contacts = [("c1", "w", "Brightvale", "priya@brightvale.example"), ("c2", "w", "Brightvale", "tom@brightvale.example"),
                    ("c3", "w", "Oakfield", "jo@oakfield.example")]
        self.assertEqual(sync.reply_contact(contacts, "tom@brightvale.example")[0], "c2")
        self.assertEqual(sync.reply_contact(contacts, "ceo@brightvale.example")[0], "c1")
        self.assertEqual(sorted(sync.company_contacts(contacts, "ceo@brightvale.example")), ["c1", "c2"])
        self.assertIsNone(sync.reply_contact(contacts, "x@elsewhere.example"))

    def test_opt_outs_are_recognised(self):
        for text in ("No thanks", "Please unsubscribe me", "not interested, remove me from your list", "STOP"):
            self.assertTrue(sync.is_opt_out(text), text)
        for text in ("Thanks, Wednesday works", "Not now, maybe in Q2. What does it cost?"):
            self.assertFalse(sync.is_opt_out(text), text)

    def test_a_pod_reply_task_carries_only_the_pods_context(self):
        task = sync.pod_reply_task(self.POD, "Priya Shah <priya@brightvale.example>", "Brightvale Logistics",
                                   "Re: stock counts", "Mon, 9 Nov", "Wednesday works. Can a human call me?")
        text = task["title"] + task["description"]
        for want in ("Sam Reed", "Brightvale", "Wednesday works", '"outbox"', "human"):
            self.assertIn(want, text)
        for leak in ("Nader", "Founders Group", "owner handover", "founderos"):
            self.assertNotIn(leak, text)
        self.assertEqual((task["assigneeAgentId"], task["projectId"]), ("ag-bv", "proj-bv"))



class FinalReviewRepliesTest(unittest.TestCase):
    """Codex final review #2, #3."""

    def test_seen_ids_are_per_mailbox_and_keep_old_entries(self):
        seen = {"pod/client-a/<m1@x>", "pod/client-b/<m2@x>", "<legacy@x>"}
        self.assertEqual(sync.seen_for(seen, "client-a"), {"<m1@x>", "<legacy@x>"})
        self.assertNotIn("<m1@x>", sync.seen_for(seen, "client-b"))

    def test_each_message_is_recorded_once_per_pod(self):
        q = sync.message_claim_query("client-a", "<m1@x>", "ana@acme.example", "")
        self.assertIn("on conflict (pod_id, message_id) do nothing", q)
        self.assertEqual(q.count(";"), 0, "one statement: psql must print only the routing state")
        self.assertIn("slug='client-a'", q)

    def test_a_colleague_gets_their_own_contact(self):
        cid, name, company = sync.sender_contact("c-ana", "Ben@Acme.example", "Acme")
        self.assertTrue(cid.startswith("c-ana-") and cid != "c-ana")
        self.assertEqual((name, company), ("Ben", "Acme"))
        self.assertEqual(cid, sync.sender_contact("c-ana", "ben@acme.example", "Acme")[0])
        self.assertIn("'ben@acme.example'", sync.sender_contact_query("w", cid, name, "Ben@Acme.example", company))

    def test_after_a_takeover_new_messages_go_to_the_person(self):
        q = sync.taken_over_query("w-a", ["c1", "c2"])
        self.assertIn("taken_over_at is not null", q)
        h = sync.person_handoff_query("client-a", "<m9@x>", "c1", "ana@acme.example", "Re: pricing", "Can we talk?")
        self.assertIn("'msg:client-a:<m9@x>'", h)
        self.assertIn("on conflict (issue_id) do nothing", h)

if __name__ == "__main__":
    unittest.main()
