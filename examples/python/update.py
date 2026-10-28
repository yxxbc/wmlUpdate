#!/usr/bin/env python3
"""Run wml-update and consume its NDJSON event stream.

Usage:
    python3 update.py updater.json 1.4.2 [path/to/wml-update]

The updater itself performs installation. This example only demonstrates the
language-agnostic protocol: spawn argv directly, stream JSON lines, wait for a
terminal event and honor the exit code.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys


def main() -> int:
    if len(sys.argv) < 3:
        print(__doc__, file=sys.stderr)
        return 2

    config, current = sys.argv[1], sys.argv[2]
    binary = sys.argv[3] if len(sys.argv) > 3 else os.environ.get("WML_UPDATE", "wml-update")

    command = [binary, "--config", config, "--json", "update", "--current-version", current]
    process = subprocess.Popen(
        command,
        stdout=subprocess.PIPE,
        stderr=sys.stderr,  # human diagnostics, if any
        text=True,
        encoding="utf-8",
        bufsize=1,
    )

    terminal = None
    assert process.stdout is not None
    for line in process.stdout:
        line = line.strip()
        if not line:
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            print(f"invalid JSON from updater: {line!r}", file=sys.stderr)
            process.kill()
            return 1

        kind = event.get("type")
        if kind == "progress" and event.get("phase") == "download":
            print(f"downloading: {event.get('percent', 0):.0f}%")
        elif kind == "warning":
            print(f"warning: {event.get('message')}", file=sys.stderr)
        elif kind == "log":
            print(f"[{event.get('phase', '')}] {event.get('message')}")
        elif kind == "error":
            err = event.get("error") or {}
            print(f"update failed [{err.get('code')}]: {err.get('message')}", file=sys.stderr)
            terminal = event
        elif kind == "result":
            print(json.dumps(event.get("data"), indent=2, ensure_ascii=False))
            terminal = event

    exit_code = process.wait()
    if exit_code != 0:
        return exit_code
    if terminal is None:
        print("updater exited without a terminal event", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
