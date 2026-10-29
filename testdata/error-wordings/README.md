# Wordings the diagnostics match, captured by hand

Captured with `container CLI version 1.2.2` (Homebrew formula `1.2.2_1`) on 2026-08-23, unless a file's name or header says another version. These are wordings that running the compose corpus did not trigger, so each was captured outside the corpus. How each one is captured is in `testdata/real-cli-output.md`, in the section on the wordings the diagnostics match.

| File | Wording captured | Diagnosis it gave |
|---|---|---|
| `rootfs-resolve.txt` | `failed to resolve '…' in rootfs` | OPSM-107 |
| `port-in-use-duplicate-publish.txt` | `Address already in use` | OPSM-201 (**the wording match**) |
| `vzerror-shared-named-volume.txt` | `VZErrorDomain` + `Code=2` + `storage device attachment is invalid` | OPSM-103 |
| `platform-image-index-no-arm64.txt` | `Error: platform linux/arm64` | OPSM-412 |
| `platform-unsupported-fresh-pull-141.txt` | `Error: unsupported platform Platform(` + `_rawArch: "arm64"` (a first pull of an image that is not local; `_rawArch` holds the arch that is missing) | OPSM-412 |
| `port-attempt-53.txt` | (not reached) | — |
| `port-attempt-loopback.txt` | (not reached) | — |
| `volume-in-use.txt` | `in use` | the runtime's wording (`container volume delete` run directly) |
| `volume-in-use-via-opossum.txt` | `in use` | **opossum's warning** (`could not remove volume …`) |
| `image-in-use-not-reached.txt` | (not reached this way) | — |
| `build-disk-full.txt` | `No space left on device` (**printed by busybox**) | the build's disk-full hint (**path only**) |
| `build-cache-path-only.txt` | `unable to read root manifest` (**written by us**) | the build's cache hint (**path only**) |
| `build-resource-path-only.txt` | `rpc error: code = Unavailable` (**written by us**) | the build's resource hint (**path only**) |
| `corpus-cs2-old-wording.txt` | `does not support required platforms` | OPSM-412 (from the corpus run) |
| `corpus-atlas-old-wording.txt` | the same | the same |
| `doctor-inputs.txt` | `system df`'s RECLAIMABLE / `builder status`'s state and memory | `doctor`'s storage and builder warnings |

## Why the attempts that did not reach are kept

**`port-attempt-53.txt`**: publishing 53, which the runtime's own DNS holds, is caught first by the pre-flight check. The same file has the `netstat` output showing a listener on the wildcard address.

**Note**: in `port-attempt-loopback.txt`, the `lsof` and opossum lines are raw output, but the `ワイルドカード listen: 成功` ("wildcard listen: succeeded") line is a **message from the probe program itself**, not from the runtime or opossum. The file also has no evidence that `SO_REUSEADDR` is the mechanism (it does not print the socket options). The conclusion (this path is not reached) does not depend on the mechanism, but **read which lines are raw output and which are explanation separately**.

**`port-attempt-loopback.txt`**: the pre-flight's blind spot is real: a TCP listener that holds only the loopback address is not seen by the wildcard probe `net.Listen("tcp4", ":port")`, because Go sets `SO_REUSEADDR`. The same file has **`lsof` output showing the listener was there** and **the probe's output not seeing it**. **But the runtime does not fail either, and the container starts.**

So "the pre-flight has a blind spot" and "the blind spot causes a failure" are different things, and **this path is not reached**. The duplicate publish is the one that did reach. **Attempts that did not reach are kept too**, so that the next person who tries the same way does not spend time on it.

## The three "path only" captures

Writing the wording into `RUN`'s output does make the hint appear, so **the wiring is live**. But what triggered it is **a string we wrote**, not one `container` printed. **If upstream rewords it, these three stay green.** They do not show that upstream still uses the wording.
