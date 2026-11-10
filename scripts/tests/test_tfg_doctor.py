"""Tests for scripts/tfg-doctor.py (the auto-repair doctor)."""
import importlib.util
import json
import os
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("tfg_doctor", Path(__file__).resolve().parent.parent / "tfg-doctor.py")
doc = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(doc)


class CronTest(unittest.TestCase):
    def test_weekday_routine_last_fire(self):
        # Tuesday 10 Nov 2026 17:00 UTC; London is on GMT.
        ref = datetime(2026, 11, 10, 17, 0, tzinfo=timezone.utc)
        got = doc.cron_last_fire("30 7 * * 1-5", "Europe/London", ref)
        self.assertEqual((got.day, got.hour, got.minute), (10, 7, 30))

    def test_before_todays_time_goes_back_a_day(self):
        ref = datetime(2026, 11, 10, 6, 0, tzinfo=timezone.utc)
        got = doc.cron_last_fire("30 7 * * 1-5", "Europe/London", ref)
        self.assertEqual(got.day, 9)

    def test_weekend_skips_to_friday(self):
        ref = datetime(2026, 11, 8, 12, 0, tzinfo=timezone.utc)  # Sunday
        got = doc.cron_last_fire("30 7 * * 1-5", "Europe/London", ref)
        self.assertEqual(got.day, 6)

    def test_weekly(self):
        ref = datetime(2026, 11, 10, 12, 0, tzinfo=timezone.utc)
        got = doc.cron_last_fire("0 8 * * 1", "Europe/London", ref)
        self.assertEqual(got.day, 9)

    def test_unknown_form_is_none(self):
        self.assertIsNone(doc.cron_last_fire("*/5 * * * *", "UTC", datetime.now(timezone.utc)))


class EnvLineTest(unittest.TestCase):
    def test_set_and_read(self):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / ".env"
            p.write_text("A=1\nFOUNDEROS_CAMPAIGN=*\n")
            doc.set_env_line(p, "FOUNDEROS_CAMPAIGN", "")
            doc.set_env_line(p, "NEW", "x")
            self.assertEqual(doc.env_value(p, "FOUNDEROS_CAMPAIGN"), "")
            self.assertEqual(doc.env_value(p, "A"), "1")
            self.assertEqual(doc.env_value(p, "NEW"), "x")


class HoldsTest(unittest.TestCase):
    def test_restores_file_and_env(self):
        with tempfile.TemporaryDirectory() as d:
            f, env = Path(d) / "halted", Path(d) / ".env"
            env.write_text("FOUNDEROS_CAMPAIGN=*\n")
            calls = []
            orig = (doc.restart_backend, doc.pc_sql, doc.notify, doc.LOG)
            doc.restart_backend = lambda: calls.append("restart")
            doc.pc_sql = lambda q: [["paused"]]
            doc.notify = lambda *a, **k: calls.append("notify")
            doc.LOG = Path(d) / "log.jsonl"
            try:
                got = doc.keep_holds({"files": {str(f): "paused\n"}, "env": {str(env): {"FOUNDEROS_CAMPAIGN": ""}},
                                      "paperclipRoutinesPaused": ["Daily outreach and follow-ups"]})
            finally:
                doc.restart_backend, doc.pc_sql, doc.notify, doc.LOG = orig
            self.assertTrue(f.exists())
            self.assertEqual(doc.env_value(env, "FOUNDEROS_CAMPAIGN"), "")
            self.assertEqual(len(got), 2)
            self.assertIn("restart", calls)

    def test_nothing_to_restore(self):
        with tempfile.TemporaryDirectory() as d:
            f = Path(d) / "halted"
            f.write_text("x")
            orig = doc.pc_sql
            doc.pc_sql = lambda q: [["paused"]]
            try:
                self.assertEqual(doc.keep_holds({"files": {str(f): "x"}, "paperclipRoutinesPaused": ["R"]}), [])
            finally:
                doc.pc_sql = orig


class RepairResultTest(unittest.TestCase):
    def test_collects_finished_agent_result(self):
        with tempfile.TemporaryDirectory() as d:
            logf = Path(d) / "r.log"
            logf.write_text('working...\nDOCTOR_RESULT {"status": "fixed", "summary": "restarted the web app"}\n')
            orig = (doc.LOCK, doc.LOG, doc.notify)
            doc.LOCK, doc.LOG = Path(d) / "repair.lock", Path(d) / "log.jsonl"
            sent = []
            doc.notify = lambda *a, **k: sent.append(a)
            doc.save_json(doc.LOCK, {"pid": 999999, "check": "web", "started": doc.iso(doc.now()), "log": str(logf)})
            state = {"checks": {"web": {"label": "OS web app"}}}
            try:
                self.assertFalse(doc.collect_repair(state))
            finally:
                lock = doc.LOCK
                doc.LOCK, doc.LOG, doc.notify = orig
            self.assertEqual(state["checks"]["web"]["ai"]["status"], "fixed")
            self.assertEqual(state["checks"]["web"]["aiMisses"], 0)
            self.assertFalse(lock.exists())
            self.assertEqual(len(sent), 1)

    def test_missing_result_counts_as_failed(self):
        with tempfile.TemporaryDirectory() as d:
            logf = Path(d) / "r.log"
            logf.write_text("crashed\n")
            orig = (doc.LOCK, doc.LOG)
            doc.LOCK, doc.LOG = Path(d) / "repair.lock", Path(d) / "log.jsonl"
            doc.save_json(doc.LOCK, {"pid": 999999, "check": "site", "started": doc.iso(doc.now()), "log": str(logf)})
            state = {"checks": {}}
            try:
                doc.collect_repair(state)
            finally:
                doc.LOCK, doc.LOG = orig
            self.assertEqual(state["checks"]["site"]["ai"]["status"], "failed")
            self.assertEqual(state["checks"]["site"]["aiMisses"], 1)


class AllowListTest(unittest.TestCase):
    def test_agent_never_gets_blanket_or_destructive_permissions(self):
        joined = " ".join(doc.ALLOWED)
        for bad in ("sudo", "--force", "reset --hard", "rm ", "bypass", "Bash(*)", "Bash(python3:*)"):
            self.assertNotIn(bad, joined)


if __name__ == "__main__":
    unittest.main()
