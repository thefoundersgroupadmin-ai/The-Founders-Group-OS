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


if __name__ == "__main__":
    unittest.main()
