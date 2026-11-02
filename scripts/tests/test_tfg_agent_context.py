import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / "tfg-agent-context.py"


class NoUsagePauseTest(unittest.TestCase):
    def test_a_leftover_pause_file_does_not_block_agents(self):
        # The 50% usage pause was removed on 2 Nov 2026 at the owner's request.
        with tempfile.TemporaryDirectory() as home:
            Path(home, ".founderos").mkdir()
            Path(home, ".founderos/ai-paused").write_text("Codex weekly usage reached 67%\n")
            result = subprocess.run([sys.executable, "-I", str(SCRIPT), "--check-usage-only"],
                                    capture_output=True, text=True, env={"HOME": home})
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(result.stdout), {"allowed": True})


if __name__ == "__main__":
    unittest.main()
