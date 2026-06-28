# Sandbox Red-Team Checklist

This checklist documents the adversarial inputs the `exec` sandbox must reject or
classify as high/critical risk. The corresponding regression suite lives in
`internal/exec/sandbox_redteam_test.go`.

## Path confinement

- [x] `../../etc/passwd` → `ErrPathEscapes`
- [x] `../../../tmp/secret` → `ErrPathEscapes`
- [x] `sub/../../etc/passwd` → `ErrPathEscapes`
- [x] Absolute path outside the repo root → `ErrPathEscapes`
- [x] Symlink pointing outside the repo root → `ErrPathEscapes`

## Shell escape / sub-execution

- [x] `eval $(cmd)` → `RiskCritical`
- [x] `source /file` → `RiskCritical`
- [x] `echo $(cat /etc/passwd)` → `RiskCritical`
- [x] Backtick sub-shell → `RiskCritical`
- [x] `bash -c 'rm -rf /'` → `RiskCritical`
- [x] Other shell interpreters with `-c` (`sh`, `zsh`, `fish`, etc.) → `RiskCritical`

## Network commands

- [x] `curl`, `wget`, `ssh`, `scp`, `rsync`, `nc`, `ftp`, `telnet` → `RiskHigh`

## Wrapper peeling

- [x] `sudo rm -rf /` peels to `rm -rf /` → `RiskCritical`
- [x] `env ls` peels to `ls` → `RiskLow`
- [x] `time go test` peels to `go test` → `RiskLow`

## Tool traversal

- [x] `Read` tool with `../../etc/passwd` → denied before filesystem access

## Notes

- The sandbox returns `ErrPathEscapes` as a normal error so the dispatcher can
  surface it to the model as a tool result, never as a panic.
- Classification is conservative: unknown commands default to `RiskMedium` so the
  human-in-the-loop gate prompts rather than silently running untrusted code.
