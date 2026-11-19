import subprocess
import sys

binary = sys.argv[1]
cases = [
    (engine, enabled, mode, case)
    for engine in ("interpreter", "compiler")
    for enabled in ("false", "true")
    for mode in ("direct", "cancel")
    for case in ("call", "start")
]
cases += [
    (engine, "false", "direct", case)
    for engine in ("interpreter", "compiler")
    for case in ("start-trap", "init-trap")
]

failed = False
for engine, enabled, mode, case in cases:
    args = [binary, engine, enabled, mode, case]
    try:
        result = subprocess.run(args, capture_output=True, text=True, timeout=5)
    except subprocess.TimeoutExpired:
        print(f"engine={engine} enabled={enabled} mode={mode} case={case} EXTERNAL_TIMEOUT", flush=True)
        failed = True
        continue
    print(f"exit={result.returncode} {result.stdout.strip()}", flush=True)
    if result.stderr:
        print(f"stderr={result.stderr.strip()}", flush=True)
    output = result.stdout
    if result.returncode != 0:
        failed = True
    elif case in ("call", "start"):
        expected = "execution_stopped=true" if enabled == "true" else "execution_stopped=false within=750ms"
        failed |= "close_returned=true close_error=<nil>" not in output or expected not in output
    else:
        expected = "result_module_nil=true" if case == "start-trap" else "result_module_nil=false"
        failed |= (
            expected not in output
            or 'error="<nil>"' in output
            or "close_error=<nil> explicit_memory_alias_alive=true" not in output
        )

print("RESULT: FAIL" if failed else "RESULT: PASS", flush=True)
sys.exit(1 if failed else 0)
