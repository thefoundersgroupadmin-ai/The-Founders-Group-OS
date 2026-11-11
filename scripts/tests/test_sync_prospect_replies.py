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


class ThreadTest(unittest.TestCase):
    """Codex: replies are credited to the email they answer, by thread."""

    def test_thread_headers_name_the_outbox_email_nearest_first(self):
        irt = "<tfg.pc-the-3-665e.1.ab12cd@bv.example>"
        refs = "<other@gmail.com> <tfg.pc-the-3-111a.0.0a0b0c@bv.example> <tfg.pc-the-3-665e.1.ab12cd@bv.example>"
        self.assertEqual(sync.thread_outbox_ids(irt, refs), ["pc-the-3-665e", "pc-the-3-111a"])
        self.assertEqual(sync.thread_outbox_ids("", "<abc@gmail.com>"), [])
        self.assertEqual(sync.thread_outbox_ids("<tfg.x'; drop table y.1.ab12@z>", ""), [])

    def test_a_thread_only_matches_this_pods_sent_email(self):
        q = sync.thread_query("w-a", ["o2", "o1"])
        self.assertIn("o.workspace_id='w-a'", q)
        self.assertIn("o.status='sent'", q)
        self.assertLess(q.index("when 'o2' then 0"), q.index("when 'o1' then 1"))
        self.assertIsNone(sync.thread_query("w-a", []))

    def test_without_a_thread_the_latest_email_before_the_reply_counts(self):
        q = sync.latest_sent_query("w-a", "Ana@Acme.example", "c1", "2026-11-09T10:00:00+00:00")
        self.assertIn("lower(o.to_address)='ana@acme.example'", q)
        self.assertIn("o.sent_at <= '2026-11-09T10:00:00+00:00'", q)
        self.assertIn("order by o.sent_at desc limit 1", q)

    def test_the_claim_records_what_the_reply_answered(self):
        q = sync.message_claim_query("client-a", "<m1@x>", "ana@acme.example", "", "o2", True)
        self.assertIn("'o2', true", q)
        self.assertEqual(q.count(";"), 0)

    def test_a_forwarded_reply_still_belongs_to_the_original_company(self):
        contacts = [("c-ana", "w", "Acme", "ana@acme.example"), ("c-bo", "w", "Acme", "bo@acme.example"),
                    ("c-x", "w", "Other", "x@other.example")]
        self.assertEqual(sorted(sync.thread_company(contacts, "c-ana")), ["c-ana", "c-ana", "c-bo"])
        self.assertEqual(sync.thread_company(contacts, "c-gone"), ["c-gone"])
        self.assertIn("message_id='<m1>'", sync.claim_age_query("client-a", "<m1>"))

    def test_dates_and_task_keys(self):
        self.assertEqual(sync.received_iso("Mon, 09 Nov 2026 10:00:00 +0000"), "2026-11-09T10:00:00+00:00")
        self.assertEqual(sync.received_iso("not a date"), "")
        k = sync.reply_task_key("client-a", "<m1@x>")
        self.assertEqual(k, sync.reply_task_key("client-a", "<m1@x>"))
        self.assertNotEqual(k, sync.reply_task_key("client-b", "<m1@x>"))
        self.assertLessEqual(len(sync.reply_task_key("client-a", "<" + "x" * 900 + ">")), 255)


class SingleInstanceTest(unittest.TestCase):
    def test_a_second_run_exits(self):
        import tempfile
        from pathlib import Path
        p = Path(tempfile.mkdtemp()) / "x.lock"
        first = sync.single_instance(p)
        with self.assertRaises(SystemExit):
            sync.single_instance(p)
        first.close()

if __name__ == "__main__":
    unittest.main()


class ReportsAndIdentityTest(unittest.TestCase):
    """Bounces, complaints, auto-replies and sender verification (gap list, 11 Nov 2026)."""

    def msg(self, raw):
        import email as _email
        return _email.message_from_string(raw)

    def test_hard_bounce(self):
        m = self.msg('From: Mail Delivery Subsystem <mailer-daemon@googlemail.com>\nSubject: Delivery Status Notification (Failure)\n'
                     'Content-Type: multipart/report; report-type=delivery-status; boundary="b"\n\n--b\nContent-Type: text/plain\n\nAddress not found\n'
                     '--b\nContent-Type: message/delivery-status\n\nReporting-MTA: dns; googlemail.com\n\nFinal-Recipient: rfc822; gone@acme.example\nAction: failed\nStatus: 5.1.1\n--b--\n')
        self.assertEqual(sync.delivery_report(m), ("bounce", "gone@acme.example", True))

    def test_delay_is_not_a_bounce(self):
        m = self.msg('From: mailer-daemon@x.example\nContent-Type: multipart/report; report-type=delivery-status; boundary="b"\n\n'
                     '--b\nContent-Type: message/delivery-status\n\nFinal-Recipient: rfc822; slow@acme.example\nAction: delayed\nStatus: 4.4.1\n--b--\n')
        self.assertIsNone(sync.delivery_report(m))

    def test_complaint(self):
        m = self.msg('From: feedback@yahoo.example\nContent-Type: multipart/report; report-type=feedback-report; boundary="b"\n\n'
                     '--b\nContent-Type: message/feedback-report\n\nFeedback-Type: abuse\nOriginal-Rcpt-To: <angry@acme.example>\n--b--\n')
        self.assertEqual(sync.delivery_report(m), ("complaint", "angry@acme.example", True))

    def test_out_of_office(self):
        m = self.msg("From: a@acme.example\nAuto-Submitted: auto-replied\nSubject: Re: hello\n\nAway")
        self.assertTrue(sync.auto_reply(m, "Re: hello"))
        self.assertTrue(sync.auto_reply(self.msg("From: a@acme.example\n\nx"), "Out of Office: back Monday"))
        self.assertEqual(sync.auto_reply(self.msg("From: a@acme.example\n\nx"), "Re: your email"), "")

    def test_sender_verification(self):
        ok = self.msg("From: a@acme.example\nAuthentication-Results: mx.google.com; dkim=pass header.i=@acme.example; spf=pass; dmarc=pass (p=NONE) header.from=acme.example\n\nx")
        bad = self.msg("From: a@acme.example\nAuthentication-Results: mx.google.com; spf=fail smtp.mailfrom=evil.example; dmarc=fail header.from=acme.example\n\nx")
        none = self.msg("From: a@acme.example\n\nx")
        self.assertTrue(sync.sender_verified(ok, "a@acme.example"))
        self.assertFalse(sync.sender_verified(bad, "a@acme.example"))
        self.assertIsNone(sync.sender_verified(none, "a@acme.example"))


class VerificationHardeningTest(unittest.TestCase):
    def msg(self, raw):
        import email as _email
        return _email.message_from_string(raw)

    def test_lookalike_and_lower_headers_dont_verify(self):
        lookalike = self.msg("From: a@acme.example\nAuthentication-Results: mx.google.com; dkim=pass header.i=@notacme.example; dmarc=pass header.from=notacme.example\n\nx")
        self.assertFalse(sync.sender_verified(lookalike, "a@acme.example"))
        forged_lower = self.msg("From: a@acme.example\nAuthentication-Results: mx.google.com; spf=fail smtp.mailfrom=evil.example; dmarc=fail header.from=acme.example\n"
                                "Authentication-Results: fake; dmarc=pass header.from=acme.example\n\nx")
        self.assertFalse(sync.sender_verified(forged_lower, "a@acme.example"))
        sub = self.msg("From: a@acme.example\nAuthentication-Results: mx.google.com; dkim=pass header.i=@mail.acme.example\n\nx")
        self.assertTrue(sync.sender_verified(sub, "a@acme.example"))
