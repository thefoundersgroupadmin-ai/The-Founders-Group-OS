"""Tests for scripts/sync-website-enquiries.py."""
import importlib.util
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("web_enq", Path(__file__).resolve().parent.parent / "sync-website-enquiries.py")
m = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(m)


class PlanTest(unittest.TestCase):
    def test_contact(self):
        c, touch, title, body = m.plan({"form-name": "contact", "name": "Ann Lee", "company": "Acme", "email": "Ann@Acme.com",
                                        "team": "2 to 4", "message": "Founder still sells", "received": "2026-11-11T10:00:00Z", "page": "/contact.html"})
        self.assertEqual(c["id"], "fc-web-ann-acme-com")
        self.assertEqual(c["email"], "ann@acme.com")
        self.assertEqual((c["status"], c["relationship"]), ("engaged", "warm"))
        self.assertEqual(title, "Website enquiry: Ann Lee, Acme")
        self.assertIn("Founder still sells", body)
        self.assertIn("/contact.html", touch)

    def test_quote_accept_is_hot_with_quote_details(self):
        c, _, title, body = m.plan({"form-name": "quote-accept", "email": "b@c.io", "company": "C", "package": "Core", "reps": "6",
                                    "quoteRef": "Q261111-123", "quoteMonthOne": "$15,000", "quoteMonthly": "$10,500", "quoteValidUntil": "2026-12-11"})
        self.assertEqual(c["relationship"], "hot")
        self.assertIn("Send the services agreement", c["next"])
        self.assertTrue(title.startswith("Wants to proceed with a quote"))
        self.assertIn("Q261111-123", body)

    def test_sql_escapes(self):
        stmts = m.statements("ws", "lead:x:abcd1234", {"email": "o'neil@x.com", "name": "O'Neil", "received": "2026-11-11T10:00:00Z"})
        self.assertIn("'O''Neil'", stmts[0])
        self.assertIn("fc-web-o-neil-x-com-abcd1234", stmts[1])


if __name__ == "__main__":
    unittest.main()
