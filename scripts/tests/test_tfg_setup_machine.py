"""Tests for scripts/tfg-setup-machine.sh, run against temp dirs (no launchctl, no build).

Run: python3 -I scripts/tests/test_tfg_setup_machine.py
"""
import os
import plistlib
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent.parent
SCRIPT = REPO / "scripts" / "tfg-setup-machine.sh"


class SetupMachineTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        self.home, self.agents = root / "founderos", root / "LaunchAgents"
        self.env = {**os.environ, "FOUNDEROS_HOME": str(self.home), "LAUNCH_AGENTS_DIR": str(self.agents),
                    "TFG_SKIP_LAUNCHCTL": "1", "TFG_SKIP_BUILD": "1"}

    def tearDown(self):
        self.tmp.cleanup()

    def run_script(self, *args):
        return subprocess.run(["bash", str(SCRIPT), *args], env=self.env, capture_output=True, text=True, check=True).stdout

    def plists(self):
        return sorted(p.name for p in self.agents.glob("*.plist"))

    def fill(self, name, **keys):
        path = self.home / name
        path.write_text(path.read_text() + "".join(f"{k}={v}\n" for k, v in keys.items()))

    def test_first_run_copies_private_templates_and_loads_nothing(self):
        out = self.run_script()
        self.assertIn("created", out)
        for name in (".env", "planted.env"):
            self.assertEqual((self.home / name).stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.plists(), [])

    def test_never_overwrites_existing_keys(self):
        self.run_script()
        self.fill(".env", PAPERCLIP_BOARD_KEY="mine")
        self.run_script()
        self.assertIn("PAPERCLIP_BOARD_KEY=mine", (self.home / ".env").read_text())

    def test_jobs_load_once_their_keys_are_filled_with_this_machines_paths(self):
        self.run_script()
        self.fill(".env", PAPERCLIP_API_URL="https://pc.example", PAPERCLIP_BOARD_KEY="k", PAPERCLIP_COMPANY_ID="c")
        self.run_script()
        self.assertEqual(self.plists(), ["net.thefoundersgroup.paperclip-outbox.plist",
                                         "net.thefoundersgroup.paperclip-pipeline-sync.plist"])
        with open(self.agents / "net.thefoundersgroup.paperclip-outbox.plist", "rb") as f:
            p = plistlib.load(f)
        self.assertEqual(p["ProgramArguments"][-1], str(REPO / "scripts" / "sync-paperclip-outbox.py"))
        self.assertEqual(p["StartInterval"], 300)
        self.assertTrue(p["StandardOutPath"].startswith(str(self.home)))

    def test_dry_run_changes_nothing(self):
        out = self.run_script("--dry-run")
        self.assertIn("would run", out)
        self.assertFalse(self.home.exists())

    def test_dry_run_keeps_existing_jobs(self):
        self.run_script()
        self.fill(".env", FOUNDEROS_COLLECTOR_URL="http://x")
        self.run_script()
        (self.home / ".env").write_text("")
        self.run_script("--dry-run")
        self.assertEqual(self.plists(), ["net.thefoundersgroup.founderos-collector.plist"])

    def test_remove_deletes_the_jobs(self):
        self.run_script()
        self.fill(".env", FOUNDEROS_COLLECTOR_URL="http://x")
        self.run_script()
        self.assertEqual(self.plists(), ["net.thefoundersgroup.founderos-collector.plist"])
        self.run_script("--remove")
        self.assertEqual(self.plists(), [])


if __name__ == "__main__":
    unittest.main()
