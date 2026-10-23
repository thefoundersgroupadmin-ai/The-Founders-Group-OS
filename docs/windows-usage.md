# Windows AI usage in TFG OS

Sign in to the OS with your own team email, then open **Usage → My AI accounts**.

## Claude requests made in the OS

After connecting your own Claude token, the Usage page includes token counts
from Claude requests you make in the OS. These requests execute on the OS host
using your account. This works from Windows, WSL, macOS, or any browser without
installing a laptop reporter. The page polls every 10 seconds.

The OS also attempts to read the connected account's session and weekly plan
percentages, cached for 60 seconds. A token that cannot read those percentages
still works for inference; the page says the percentages are unavailable instead
of inventing a remaining allowance. Token counts cover recorded execution logs,
not every interaction in the Claude website or app.

## Claude Code and Codex running on your Windows PC

1. Choose **Windows** under **This laptop's usage**.
2. Click **Connect this laptop** (or **Make a new install line**).
3. Copy the entire generated command into **Windows PowerShell** as your normal
   Windows user and press Enter. Do not share the command; it contains your
   laptop's upload token.
4. Refresh Usage after the installer finishes. The first upload runs immediately;
   subsequent uploads run every 10 minutes while Windows is on and you are signed in.

The reporter supports native Windows 10/11 on Intel/AMD 64-bit and ARM64. It reads
the current user's Claude Code and Codex session files, converts them into usage
totals locally, and uploads only usage numbers. No messages, notes, or source files
are uploaded. Claude's local unexpired OAuth access token may be used read-only
against Anthropic's usage endpoint for plan percentages; it is never uploaded to
TFG OS or refreshed by the reporter. Custom `CLAUDE_CONFIG_DIR` and `CODEX_HOME`
are honored when present in the scheduled task's environment.

The Windows reporter does not scan sessions inside WSL. OS-side Claude requests
still appear without it. For local WSL token counts, a separate Linux reporter or
explicit WSL source support is needed.

The install is per user, under `%LOCALAPPDATA%\TFG-OS\usage`. The upload token is
encrypted with Windows DPAPI for that user on that PC. The **TFG OS Usage** task
(suffixed with that user's Windows SID, to keep shared PCs' users separate)
runs without administrator privileges. `last-run.log` in that directory contains
the result of the most recent upload. A failed initial upload is reported as a
warning and retried by the scheduled task.

To stop accepting uploads immediately, click **Disconnect laptop** in the OS.
To remove the scheduled task and local reporter too, run this in PowerShell:

```powershell
& ([scriptblock]::Create((Invoke-WebRequest -UseBasicParsing 'https://os.thefoundersgroup.net/api/founderos/laptop/install.ps1').Content)) -Remove
```

## Hosting

`scripts/tfg-host.sh` builds macOS and Windows reporters for amd64 and arm64.
The Windows artifacts are `tfg-usage-windows-amd64.exe` and
`tfg-usage-windows-arm64.exe` under `~/.founderos/bin/laptop`.
The public `GET /api/founderos/laptop/install.ps1` endpoint contains no credentials;
binary downloads and uploads require a valid per-user laptop token.
The existing Cloudflare Access bypass for `/api/founderos/laptop/*` covers the
Windows installer as well. The browser-facing OS remains protected by Access.
