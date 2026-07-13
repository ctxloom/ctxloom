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
| **Homebrew** (`brew install ctxloom/tap/...`) | The casks clear quarantine only. They do **not** remove `com.apple.provenance` or ad-hoc sign, so on Sequoia+ you may still need the `codesign` step below |
| **`install.sh`** (macOS/Linux) | Usually none — it clears quarantine *and* provenance and ad-hoc signs on macOS |
| **`install.ps1`** (Windows) | It clears nothing; it only prints the `Unblock-File` command. Run that yourself if SmartScreen interposes |
| **Manual download** | macOS and Windows both flag the file; manual steps below |
| **`go install` / build from source** | None — binaries you build locally are never quarantined |

On macOS, `install.sh` is the path with the least ceremony, not Homebrew: it is
the only installer that handles `com.apple.provenance`, which is the attribute
behind the silent kill described below. Homebrew is still a fine way to install
`ctxloom` itself if you prefer it (and `install.sh --brew` will delegate to it),
with two caveats: you may have to ad-hoc sign afterwards, and the `taskloom` and
`ltk` casks are **not published for prerelease tags** — every pre-1.0 release is
one, so `brew install ctxloom/tap/taskloom` and `.../ltk` will currently fail.
`install.sh --brew` warns and skips when they do.

## macOS (Gatekeeper)

Anything downloaded by a browser or script gets the `com.apple.quarantine`
extended attribute; on macOS Sequoia and later, `com.apple.provenance` too.
Consequences for an unsigned binary:

- **Quarantine** triggers the *"cannot be opened because the developer cannot
  be verified"* dialog.
- **Provenance** can cause the kernel to kill the process outright — the
  symptom is a bare `zsh: killed ctxloom` with no dialog at all.

`install.sh` handles both when it can: it removes the two attributes and ad-hoc
signs the binary (`codesign --force --sign -`), skipping whichever step's tool
is missing. The Homebrew casks do **less** — every cask's post-install hook runs
exactly one command, `xattr -dr com.apple.quarantine`. No cask touches
`com.apple.provenance` and no cask signs anything, so a brew-installed binary
can still be killed outright on Sequoia+. If a binary is blocked or killed — or
you downloaded an archive manually — run:

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

`install.ps1` never clears the mark itself — it calls neither `Unblock-File` nor
anything else that strips the zone stream; at the end of a run it just prints
the command above for you to run. In practice its `Invoke-WebRequest` download
generally does not carry the mark when run from an existing PowerShell session,
but SmartScreen heuristics vary by system policy, so if the dialog appears,
clear the mark yourself. Because the script does not verify the `ctxloom`
archive's checksum either, check the hash yourself first (see below) rather than
accepting the dialog on trust.

## Linux

No trust ceremony: there is no quarantine mechanism. The only requirement is
the executable bit, which the install script sets.

## Verifying what you run

Every release publishes `checksums.txt`. What the install scripts do with it
differs by platform, and neither one fails closed:

- **`install.sh`** fetches `checksums.txt` and verifies each archive against it.
  A genuine **mismatch aborts the install**. But verification *degrades* rather
  than failing: if `checksums.txt` can't be fetched, if it has no entry for the
  archive, or if neither `sha256sum` nor `shasum` is on the box, the script logs
  a warning and installs anyway. Watch for that warning.
- **`install.ps1`** does **not verify the `ctxloom` archive at all** — it
  downloads, extracts, and installs it with no hash check. Checksum verification
  on Windows exists only for the companions (`taskloom`, `ltk`), and a mismatch
  there skips that companion rather than aborting. On Windows the primary binary
  is the unverified one; verify it yourself with the command below.

Manually:

```bash
sha256sum ctxloom_*_linux_amd64.tar.gz
grep linux_amd64 checksums.txt   # compare
```

```powershell
(Get-FileHash ctxloom_*_windows_amd64.zip -Algorithm SHA256).Hash
Select-String -Path checksums.txt -Pattern windows_amd64   # compare
```

The install scripts themselves are auditable and their SHA256s are printed in
each release's notes alongside VirusTotal links. When in doubt: download,
read, then run.

## Why not just sign the binaries?

Apple notarization and Windows code signing require paid developer accounts
and infrastructure that this pre-1.0 project doesn't carry yet. Signing (or
sigstore/cosign attestation) is on the roadmap; until then the trust model is
open source, public CI from tagged commits, and published checksums — with the
steps above as the cost.

Reproducible builds are **not** part of that model today. The `taskloom` and
`ltk` builds are built with `-trimpath` and a pinned `mod_timestamp`; the four
`ctxloom` build variants have neither, so you cannot currently rebuild the
flagship binary from a tag and expect to reproduce the released artifact
bit-for-bit. Making the `ctxloom` builds reproducible is on the same roadmap.
