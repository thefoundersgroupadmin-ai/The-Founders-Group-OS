import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
import os

spec = importlib.util.spec_from_file_location("agent_context", Path(__file__).resolve().parents[1] / "tfg-agent-context.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class UsageSnapshotTest(unittest.TestCase):
    def test_uses_latest_official_event_across_durable_sessions(self):
        with tempfile.TemporaryDirectory() as root:
            for filename, timestamp, percent in [("new.jsonl", "2026-10-26T14:00:00Z", 52), ("old.jsonl", "2026-10-26T13:00:00Z", 9)]:
                Path(root, filename).write_text(json.dumps({"timestamp": timestamp, "payload": {"rate_limits": {"secondary": {"used_percent": percent}}}}) + "\n")
            self.assertEqual(module.latest_weekly_usage(root), 52)

    def test_discovers_current_native_runner_sessions(self):
        with tempfile.TemporaryDirectory() as root:
            sessions = Path(root, ".paperclip/instances/default/runtime/paperclip-runner/durable-sessions/a/codex-home/sessions")
            sessions.mkdir(parents=True)
            (sessions / "usage.jsonl").write_text(json.dumps({"timestamp": "2026-10-26T22:00:00Z", "payload": {"rate_limits": {"secondary": {"used_percent": 50}}}}) + "\n")
            self.assertEqual(module.latest_weekly_usage(module.codex_session_roots(root)), 50)

    def test_resume_is_explicit_bounded_and_later_pause_wins(self):
        with tempfile.TemporaryDirectory() as root:
            config=Path(root,'.founderos');config.mkdir()
            now=datetime.now(timezone.utc)
            pause=config/'ai-paused';pause.write_text('cutoff')
            os.utime(pause,(now.timestamp()-60,now.timestamp()-60))
            grant={'authorizedAt':(now-timedelta(seconds=1)).isoformat(),'expiresAt':(now+timedelta(hours=1)).isoformat(),'reason':'owner said keep going after cutoff'}
            (config/'ai-resume.json').write_text(json.dumps(grant))
            self.assertTrue(module.explicit_resume_active(root,now))
            self.assertFalse(module.explicit_resume_active(root,now+timedelta(hours=2)))
            os.utime(pause,(now.timestamp(),now.timestamp()))
            self.assertFalse(module.explicit_resume_active(root,now))

    def test_the_owners_go_ahead_holds_until_the_next_crossing(self):
        # Codex review, 2 Nov: Python only knew the 4-hour lease.
        with tempfile.TemporaryDirectory() as root:
            config = Path(root, '.founderos'); config.mkdir()
            sessions = Path(root, '.codex/sessions'); sessions.mkdir(parents=True)
            at = datetime(2026, 11, 2, 18, tzinfo=timezone.utc)
            (config / 'ai-goahead.json').write_text(json.dumps({'authorizedAt': at.isoformat(), 'by': 'owner'}))
            def reading(pct, resets):
                (sessions / 'u.jsonl').write_text(json.dumps({"timestamp": "2026-11-03T00:00:00Z", "payload": {"rate_limits": {"secondary": {"used_percent": pct, "resets_at": resets.timestamp()}}}}) + "\n")
            reading(67, at + timedelta(days=1))  # the week it was given in
            self.assertTrue(module.explicit_resume_active(root, at + timedelta(hours=10)))
            reading(30, at + timedelta(days=8))  # next week, below the cutoff
            self.assertTrue(module.explicit_resume_active(root, at + timedelta(days=3)))
            reading(55, at + timedelta(days=8))  # next week crosses: ended, lease too
            (config / 'ai-resume.json').write_text(json.dumps({'authorizedAt': (at + timedelta(days=3)).isoformat(), 'expiresAt': (at + timedelta(days=3, hours=4)).isoformat(), 'reason': 'lease'}))
            self.assertFalse(module.explicit_resume_active(root, at + timedelta(days=3, hours=1)))
            pause = config / 'ai-paused'; pause.write_text('later pause')
            later = (at + timedelta(days=3, minutes=30)).timestamp()
            os.utime(pause, (later, later))
            reading(30, at + timedelta(days=8))
            self.assertFalse(module.explicit_resume_active(root, at + timedelta(days=3, hours=1)))

    def test_a_finished_weekly_window_is_unknown(self):
        # 2 Nov: after the reset the newest snapshot still described last week.
        with tempfile.TemporaryDirectory() as root:
            Path(root, "u.jsonl").write_text(json.dumps({"timestamp": "2026-11-01T03:00:00Z", "payload": {"rate_limits": {"secondary": {"used_percent": 67, "resets_at": 1793615084}}}}) + "\n")
            self.assertIsNone(module.latest_weekly_usage(root, now=datetime(2026, 11, 2, 17, tzinfo=timezone.utc)))
            self.assertEqual(module.latest_weekly_usage(root, now=datetime(2026, 11, 2, 9, tzinfo=timezone.utc)), 67)

    def test_missing_or_unofficial_usage_is_unknown(self):
        with tempfile.TemporaryDirectory() as root:
            Path(root, "usage.jsonl").write_text('{"used_percent": 4}\n')
            self.assertIsNone(module.latest_weekly_usage(root))


if __name__ == "__main__":
    unittest.main()
