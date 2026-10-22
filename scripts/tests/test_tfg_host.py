"""Tests for scripts/tfg-host.sh and scripts/tfg-backup.sh, against temp dirs (no launchctl, no real database).

Run: python3 -I scripts/tests/test_tfg_host.py
"""
import os
import plistlib
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent.parent
HOST = REPO / "scripts" / "tfg-host.sh"
BACKUP = REPO / "scripts" / "tfg-backup.sh"


class HostTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        self.agents = root / "LaunchAgents"
        self.paperclip = root / "paperclip"
        (self.paperclip / "server" / "src").mkdir(parents=True)
        (self.paperclip / "server" / "src" / "index.ts").write_text("")
        self.env = {**os.environ, "LAUNCH_AGENTS_DIR": str(self.agents), "FOUNDEROS_HOME": str(root / "fh"),
                    "TFG_SKIP_LAUNCHCTL": "1", "TFG_NODE": "/usr/bin/true", "TFG_PAPERCLIP_DIR": str(self.paperclip)}

    def tearDown(self):
        self.tmp.cleanup()

    def run_host(self, *args, check=True):
        return subprocess.run(["bash", str(HOST), *args], env=self.env, capture_output=True, text=True, check=check)

    def load(self, name):
        with open(self.agents / f"net.thefoundersgroup.{name}.plist", "rb") as f:
            return plistlib.load(f)

    def test_install_writes_the_jobs(self):
        if not (REPO / "frontend" / "build" / "index.js").exists():
            self.skipTest("no production build")
        self.run_host()
        web = self.load("tfg-os-web")
        self.assertEqual(web["EnvironmentVariables"]["HOST"], "127.0.0.1")
        self.assertEqual(web["EnvironmentVariables"]["ORIGIN"], "https://os.thefoundersgroup.net")
        self.assertTrue(web["EnvironmentVariables"]["BACKEND_INTERNAL_URL"].startswith("http://127.0.0.1:"))
        self.assertTrue(web["KeepAlive"])
        self.assertEqual(self.load("tfg-os-stack")["EnvironmentVariables"]["BUSINESSOS_HEADLESS"], "1")
        self.assertEqual(self.load("tfg-keep-awake")["ProgramArguments"][0], "/usr/bin/caffeinate")
        self.assertEqual(self.load("tfg-backup")["StartCalendarInterval"], {"Hour": 3, "Minute": 15})
        pc = self.load("tfg-paperclip")
        self.assertEqual(pc["WorkingDirectory"], str(self.paperclip / "server"))
        self.assertEqual(pc["ProgramArguments"][1:], ["--import", "tsx", "src/index.ts"])
        # With a production build it runs that instead.
        # server/dist alone does not run in the monorepo; the runtime bundle does.
        (self.paperclip / "server" / "dist").mkdir()
        (self.paperclip / "server" / "dist" / "index.js").write_text("")
        self.run_host()
        self.assertEqual(self.load("tfg-paperclip")["ProgramArguments"][1:], ["--import", "tsx", "src/index.ts"])
        (self.paperclip / "server" / "dist-runtime").mkdir()
        (self.paperclip / "server" / "dist-runtime" / "index.mjs").write_text("")
        self.run_host()
        self.assertEqual(self.load("tfg-paperclip")["ProgramArguments"][1:], ["dist-runtime/index.mjs"])
        self.run_host("--remove")
        self.assertEqual(list(self.agents.glob("*.plist")), [])

    def test_dry_run_writes_nothing(self):
        out = self.run_host("--dry-run").stdout
        self.assertIn("https://os.thefoundersgroup.net", out)
        self.assertFalse(self.agents.exists())

    def test_no_paperclip_job_without_paperclip(self):
        if not (REPO / "frontend" / "build" / "index.js").exists():
            self.skipTest("no production build")
        self.env["TFG_PAPERCLIP_DIR"] = str(self.paperclip / "missing")
        self.run_host()
        self.assertFalse((self.agents / "net.thefoundersgroup.tfg-paperclip.plist").exists())


class BackupTest(unittest.TestCase):
    def test_dumps_privately_and_keeps_the_newest(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            fake = root / "pg_dump"
            fake.write_text('#!/bin/sh\nfor a; do case "$a" in --file=*) echo dump > "${a#--file=}";; esac; done\n')
            fake.chmod(0o755)
            out = root / "backups"
            out.mkdir()
            for i in range(3):
                (out / f"tfg-os-2000010{i}-000000.dump").write_text("old")
                os.utime(out / f"tfg-os-2000010{i}-000000.dump", (1000 + i, 1000 + i))
            env = {**os.environ, "TFG_BACKUP_DIR": str(out), "TFG_BACKUP_KEEP": "2", "TFG_PG_DUMP": str(fake),
                   "DATABASE_URL": "postgres://x@127.0.0.1/db"}
            subprocess.run(["bash", str(BACKUP)], env=env, capture_output=True, text=True, check=True)
            dumps = sorted(p.name for p in out.glob("tfg-os-*.dump"))
            self.assertEqual(len(dumps), 2)
            self.assertIn("tfg-os-20000102-000000.dump", dumps)
            new = [p for p in out.glob("tfg-os-*.dump") if not p.name.startswith("tfg-os-2000")][0]
            self.assertEqual(new.stat().st_mode & 0o777, 0o600)
            self.assertEqual(out.stat().st_mode & 0o777, 0o700)


if __name__ == "__main__":
    unittest.main()
