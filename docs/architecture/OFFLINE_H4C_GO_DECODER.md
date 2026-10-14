# H4C — Hermetic Go FFmpeg execution

## Scope

H4C removes host executable resolution from Go media subprocesses when the
agent is explicitly constructed for hermetic Full Edge. The legacy constructor
and zero-value decoder configuration retain the prior `PATH` lookup behavior.
This change does not wire Runtime Pack provisioning into the desktop app and
does not certify Full Edge readiness.

## Execution contract

- `agent.NewHermetic` is the explicit opt-in. It accepts only `ModeEdge` and a
  non-zero `processing.VerifiedFFmpeg` capability; it does not inspect an
  environment variable to choose the mode.
- The capability binds an absolute executable path to a canonical pack root
  and expected SHA-256. The path must remain inside that root, contain no
  symlink component, resolve to a regular executable file, and match the digest.
- The decoder and clip encoder revalidate the file immediately before each
  process start. Hermetic commands use the absolute path directly and receive
  a minimal environment (`PATH`, locale only); they do not call `LookPath` or
  fall back to `VideoFFmpegPath`, Homebrew, or a system executable.
- `VideoFFmpegPath` remains the legacy selection for `agent.New`; it is ignored
  by the hermetic decoder and clipper once a verified capability is supplied.
- The expected digest is an integrity check, not a signature. The caller must
  obtain it from the Runtime Pack manifest after that manifest has been
  authenticated by a trusted release pin. No trusted release pin or real
  approved Runtime Pack is currently wired to `agent.NewHermetic`.

## Compatibility and readiness

Existing `agent.New` callers and performance tools continue using legacy
resolution. Hermetic mode cannot silently fall back: a missing or invalid
capability is an error before the subprocess starts. Runtime integrity does
not establish that PyTorch, the Vision Worker, models, or inference are ready.
`MODEL_REDISTRIBUTION_APPROVED=false` remains unchanged.

The executable is rehashed immediately before passing its absolute path to
`os/exec`. Go's portable `exec.Cmd` still starts by pathname, so a process with
write access to the pack could race replacement between the last verification
and `Start`. The release integration must make the activated pack immutable to
the daemon's threat boundary or adopt descriptor-based execution before making
a broader adversarial race-resistance claim.

## Synthetic coverage

Tests use small shell fixtures in temporary directories only. They cover a
fake host `ffmpeg` in `PATH`, empty hermetic configuration, absent path,
external symlink, bad SHA-256, non-executable file, post-verification
tampering, pipeline propagation, and legacy `PATH` compatibility. These tests
prove executable selection, not functional FFmpeg decoding or release-pack
provenance.

## Remaining gates

1. Integrate an authenticated release-manifest pin into the explicit
   `NewHermetic` construction path (tracked by #155).
2. Provide authorized FFmpeg/FFprobe artifacts and verified provenance.
3. Run the actual bundled binaries and certify the complete Runtime Pack
   offline on supported platforms before declaring readiness.
