# Release: build, check and verify

Stage 5. Goal: a stranger can check that the binary they downloaded is the one you built.

## Build it (you)

```powershell
cd gateway
.\release.ps1 -Key $env:USERPROFILE\.gateway-release\release_key
```

It stops on the first failure, in this order: `go mod verify`, `govulncheck`, tests, then builds 6 binaries (Windows, Linux, macOS; amd64 and arm64), then checksums, then the signature. Everything lands in `gateway\dist\`.

## Verify it (anyone)

Needs only `ssh-keygen`, which Windows, macOS and Linux ship. Run in the folder with the downloaded files:

```bash
ssh-keygen -Y verify -f allowed_signers -I release -n gateway-release -s SHA256SUMS.sig < SHA256SUMS
sha256sum -c --ignore-missing SHA256SUMS
```

Expect `Good "gateway-release" signature`. On Windows use `cmd`, not PowerShell: PowerShell changes the line endings of piped text and the check fails. Windows hash check: `certutil -hashfile gateway-windows-amd64.exe SHA256`, then compare to the line in `SHA256SUMS`.

**Trust the right key.** `allowed_signers` in the download is only a convenience. Compare its key with the one published in the repo (`gateway/release/allowed_signers`) or on your profile. A key that arrives with the file proves nothing.

## What is in `dist\`

| File | What |
|---|---|
| `gateway-<os>-<arch>` | The binary. No install, no libraries |
| `SHA256SUMS` | Hash of every file |
| `SHA256SUMS.sig` | Your signature over that list |
| `allowed_signers` | The public key to check it with |
| `DEPENDENCIES.txt` | Go version and every module with its pinned hash (10 modules) |

## What was checked

- **Go toolchain pinned** to `go1.27.3` in `go.mod`. The installed 1.27.0 had 10 known standard-library vulnerabilities, including ones in `net/http` and `crypto/tls`. `govulncheck` (pinned to v1.9.0) now reports none.
- **Same source gives the same bytes.** Rebuilding `linux/amd64` on the same machine gave an identical hash. Not tested on a second machine.
- **Signature works both ways.** A good file verifies; changing one byte of `SHA256SUMS` fails.
- **The built Windows binary was started and used.** It refuses a request with no key (401), lists providers with the key (200), and blocks an injection prompt (403).

## What to remember

1. **The private key is the whole trust.** It lives at `%USERPROFILE%\.gateway-release\release_key` with no passphrase, so the release script can run alone. Anyone who copies that file can sign as you. Keep it off cloud-synced folders and out of the repo. Add a passphrase if you can type it at each release.
2. **A key can be lost.** If it leaks, publish a new one and say so loudly. There is no revocation service here.
3. **Vulnerability scans age.** A clean result today says nothing about next month. Run the release script again before each release; it re-runs the scan.
4. **Reproducible is not proven.** Same machine only. A second person building and getting the same hash is the real test.

## Not done

- **One-command install for a stranger.** There is no place the files are published yet, so no install command exists. Once they sit on a GitHub release, the check above is the second command.
- **macOS and Linux binaries were built, not run.** Only the Windows one was started.
- **No SBOM standard format.** `DEPENDENCIES.txt` is a plain list, not SPDX or CycloneDX.
- **No second-machine rebuild.**
