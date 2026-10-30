# Fixed-source observation and new-factor manual control

Current status: **source patches only; no compiled exporter, generated policy or
successful A/B/A exists**. Rust/cargo were absent. The single installed A launch
was blocked by the host; it was not retried or escalated. This is the continuation
for the new platform-defaults factor, not a request to rerun the old independent
terminal experiment.

`fixed-source-evidence.json` preserves official tag/release metadata and complete
source/static policy files with hashes. `independent-evidence.json` preserves
unaltered original records. `installed-baseline-evidence.json` is an immutable
snapshot of the controller/config/payload/results **as actually run**; its embedded
controller predates later review hardening and is not the current source snapshot.
`increment.json` identifies final workspace bytes. `policy-provenance.json` keeps
all four complete-policy output categories explicitly unavailable.

## Reproducible preparation when an existing offline Rust toolchain is available

Prerequisites: an already available compatible Rust toolchain and locked dependencies
in a **test-owned offline Cargo cache**. Neither was installed in this task. Empty
cache or unavailable compiler is a build blocker, not a reason to install/upgrade
anything globally. The following commands were **not executed** beyond patch
application dry-run; use only newly created directories. Keep the build log and
all source/patch/binary digests with the later result.

```sh
task_dir=$(mktemp -d /private/tmp/devbridge-platform-export.XXXXXX)
task_source="$task_dir/codex-758ef40f50c1a458425c7cfbf1eb12cbc07af0b0"
tar -xzf /private/tmp/devbridge-platform-source/source.tar.gz -C "$task_dir"
patch -p1 -d "$task_source" -i /Users/phoenix/Documents/devbridge/docs/architecture/evidence/codex-platform-defaults/observe-cli-policy.patch
mkdir "$task_dir/cargo-home"
```

Populate only this new cache from already available offline dependencies as needed;
do not copy credentials or global Cargo configuration. Then:

```sh
CARGO_HOME="$task_dir/cargo-home" CARGO_TARGET_DIR="$task_dir/target" cargo build --locked --offline --manifest-path "$task_source/codex-rs/Cargo.toml" -p codex-cli --bin codex > "$task_dir/build.log" 2>&1
```

Stop if the build fails. Do not substitute a different tag, source or installed
binary. Record the successful build before using its exporter:

```sh
python3 - "$task_dir" <<'PY'
import hashlib, json, pathlib, subprocess, sys
root = pathlib.Path(sys.argv[1])
source = root / 'codex-758ef40f50c1a458425c7cfbf1eb12cbc07af0b0'
patch = pathlib.Path('/Users/phoenix/Documents/devbridge/docs/architecture/evidence/codex-platform-defaults/observe-cli-policy.patch')
paths = [pathlib.Path('/private/tmp/devbridge-platform-source/source.tar.gz'), patch,
         source / 'codex-rs/cli/src/debug_sandbox.rs', root / 'build.log', root / 'target/debug/codex']
record = {'commit': '758ef40f50c1a458425c7cfbf1eb12cbc07af0b0',
          'purpose': 'observation only; no candidate patch applied',
          'build': 'cargo build --locked --offline --manifest-path SOURCE/codex-rs/Cargo.toml -p codex-cli --bin codex',
          'cargoHome': str(root / 'cargo-home'), 'targetDir': str(root / 'target'),
          'rustc': subprocess.check_output(['rustc', '--version'], text=True),
          'files': {str(p): hashlib.sha256(p.read_bytes()).hexdigest() for p in paths}}
(root / 'build-record.json').write_text(json.dumps(record, indent=2) + '\n')
PY
```

This record is auditable local provenance, not cryptographic source-build
attestation. The probe therefore always labels a supplied exporter's source claim
`SELF_REPORTED_NOT_BUILD_VERIFIED`; its declared commit alone is never proof.

## Minimal four-operation A/B/A

Use the **observation-only build**, not a build with the removal candidate already
applied. This command never installs the exporter. It generates a new fixture,
config and evidence directory under `/private/tmp`; all writes stay there. No
network, model, app-server Thread/Turn, UI or device operation is requested.

```sh
python3 /Users/phoenix/Documents/devbridge/scripts/probe_codex_platform_defaults.py \
  --executable /Users/phoenix/.nvm/versions/node/v23.6.0/bin/codex \
  --native-binary /Users/phoenix/.nvm/versions/node/v23.6.0/lib/node_modules/@openai/codex/node_modules/@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex \
  --exporter "$task_dir/target/debug/codex" \
  --output "/private/tmp/devbridge-platform-aba-$(date +%Y%m%d-%H%M%S)-$$"
```

Append `--dry-run` first to inspect the boundary without creating fixtures or
invoking any helper. Do not rerun the already blocked installed-only mode in the
same host context. No automatic privilege retry exists. All subprocess waits are
bounded to 30 seconds. A helper startup or positive-control failure stops the
series. Raw original export is retained even when validation rejects it.

A and A′ use the same original generator argv. B removes exactly four full tmp
READ/WRITE lines from the exported `-p` value; every other byte/definition and
command stays unchanged. Commands are executed only through absolute system
`/usr/bin/sandbox-exec`. The controller resets starting bytes without replacing
target inodes, and keeps markers/payload/config/child environment identical.
Allowed app READ and scratch WRITE must work in every group; excluded tmp READ
and WRITE must change success → permission denial → success. Generic command
failure, missing tool, failed startup or invalid records never count as DENY.

The controller validates policy cwd, command, network/profile and exact child
environment. The fixed-source arg0 startup may prepend one test-owned alias
subdirectory to PATH; only that precise exception is allowed and then kept fixed.
An unrecognized difference fails closed for inspection; do not silently alter
runtime grants or the expected boundary to make it pass.

Every successful run would retain full `original-generator-export.json`, each
`policy.sbpl` and `argv.json`, the actual exported/applied environment, config,
payload, external pre/post observations and hashes. A positive mechanism result
would concern this exported-policy experiment only, not installed helper repair.

## Candidate validation remains a separate gate

`remove-implicit-temp-grants.patch` applies to the pristine fixed source, independent
of the observer patch. It changes the platform defaults globally for both profiles,
without a blacklist or global deny. The included generator regression retains
explicit temporary workspace read/write parameters and rejects broad static tmp
allows. To build/test this candidate later, use another new source copy and the
same offline/toolchain restrictions:

```sh
CARGO_HOME="$task_dir/cargo-home" CARGO_TARGET_DIR="$task_dir/candidate-target" cargo test --locked --offline --manifest-path "$task_candidate_source/codex-rs/Cargo.toml" -p codex-sandboxing minimal_defaults_require_explicit_temporary_workspace_grants
```

Here `task_candidate_source` must be a distinct new extraction with the candidate
patch applied; it is intentionally not the observer source used for original A.
This generator regression is not enforcement acceptance. Only after an executable
candidate and valid positive controls exist should the original unchanged 72-case
matrix run against the candidate binary in a new evidence directory. Keep its
result separate from the original 26-failure matrix and from the 4-operation
mechanism probe. Alias, outside symlink and after-policy-created target checks
must be additional cases, not replacements for old IDs. No such candidate run
was performed this round. No runtime capability flag may be enabled from any of
these offline-only results.
