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

    def test_missing_or_unofficial_usage_is_unknown(self):
        with tempfile.TemporaryDirectory() as root:
            Path(root, "usage.jsonl").write_text('{"used_percent": 4}\n')
            self.assertIsNone(module.latest_weekly_usage(root))


if __name__ == "__main__":
    unittest.main()
