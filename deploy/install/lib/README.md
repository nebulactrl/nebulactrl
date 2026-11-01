# Installer libraries

Shell libraries `install.sh` sources. Each file only defines functions (no side
effects when sourced, no `exit`), is idempotent when its function runs, and is
inlined into the script the control plane serves at `/install.sh`.

| File | Function | Owner |
|---|---|---|
| `hardening.sh` | `nc_hardening_step`: Lynis audit and the curated safe fixes | host hardening |

`install.sh` marks the place of each library with `#@include lib/<name>.sh`.
