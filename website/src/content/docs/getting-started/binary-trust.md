---
title: "Trusting the Binaries"
---

ctxloom and its companions (taskloom, ltk) ship as **unsigned binaries**. They
are open source, built by public CI from tagged commits, and every release
archive has a SHA256 in the release's `checksums.txt` — but they are not
signed with an Apple Developer ID or a Windows code-signing certificate. Your
operating system will treat them accordingly, and depending on how you
install, you may have extra steps before the binary runs.

## TL;DR by install method

| Method | Trust steps |
|---|---|
| **Homebrew** (`brew install ctxloom/tap/...`) | None — the casks clear macOS quarantine on install |
| **Install script** (`install.sh` / `install.ps1`) | Usually none — the script clears quarantine where it can; see below if blocked |
| **Manual download** | macOS and Windows both flag the file; manual steps below |
| **`go install` / build from source** | None — binaries you build locally are never quarantined |

If you want zero trust ceremony on macOS, prefer Homebrew — the install
script can delegate to it with `install.sh --brew`.

## macOS (Gatekeeper)

Anything downloaded by a browser or script gets the `com.apple.quarantine`
extended attribute; on macOS Sequoia and later, `com.apple.provenance` too.
Consequences for an unsigned binary:

- **Quarantine** triggers the *"cannot be opened because the developer cannot
  be verified"* dialog.
- **Provenance** can cause the kernel to kill the process outright — the
  symptom is a bare `zsh: killed ctxloom` with no dialog at all.

`install.sh` handles both automatically when it can: it removes the two
attributes and ad-hoc signs the binary (`codesign --force --sign -`). The
Homebrew casks do the same in a post-install hook. If a binary is still
blocked — or you downloaded an archive manually — run:

```bash
xattr -d com.apple.quarantine /usr/local/bin/ctxloom
xattr -d com.apple.provenance /usr/local/bin/ctxloom
codesign --force --sign - /usr/local/bin/ctxloom
```

(Repeat for `taskloom` and `ltk` if you installed them.) The GUI alternative:
**System Settings → Privacy & Security**, find the *"ctxloom was blocked"*
notice, click **Open Anyway**.

## Windows (SmartScreen / Mark of the Web)

Files downloaded from the internet carry the *Mark of the Web*. SmartScreen
may interpose a *"Windows protected your PC"* dialog the first time an
unsigned executable runs. Click **More info → Run anyway**, or clear the mark
up front:

```powershell
Unblock-File C:\Users\you\bin\ctxloom.exe
```

`install.ps1` downloads with `Invoke-WebRequest`, whose output generally does
not carry the mark when run from an existing PowerShell session, but
SmartScreen heuristics vary by system policy — the dialog is normal and safe
to accept once you've verified the checksum.

## Linux

No trust ceremony: there is no quarantine mechanism. The only requirement is
the executable bit, which the install script sets.

## Verifying what you run

Every release publishes `checksums.txt`. The install scripts verify archives
against it automatically. Manually:

```bash
sha256sum ctxloom_*_linux_amd64.tar.gz
grep linux_amd64 checksums.txt   # compare
```

The install scripts themselves are auditable and their SHA256s are printed in
each release's notes alongside VirusTotal links. When in doubt: download,
read, then run.

## Why not just sign the binaries?

Apple notarization and Windows code signing require paid developer accounts
and infrastructure that this pre-1.0 project doesn't carry yet. Signing (or
sigstore/cosign attestation) is on the roadmap; until then the trust model is
open source + reproducible-ish builds + checksums, with the steps above as
the cost.
