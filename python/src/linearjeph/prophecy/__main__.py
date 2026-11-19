"""python -m linearjeph.prophecy scan [dir] [--send] [--json] [--out file]
python -m linearjeph.prophecy import <events|customers> <file.csv|file.json>
Keys: LINEARJEPH_INGEST_KEY (scan --send), LINEARJEPH_SECRET_KEY (import). Endpoint: LINEARJEPH_ENDPOINT (default https://linearjeph.cc)."""
import csv
import io
import json
import os
import sys
import time

from . import ProphecyError, import_history, init, scan, send_manifest

USAGE = __doc__


def load_rows(path, kind):
    with io.open(path, "r", encoding="utf-8-sig", newline="") as f:
        if path.lower().endswith(".csv"):
            return [{k.strip(): v for k, v in row.items() if k and v not in (None, "")} for row in csv.DictReader(f)]
        j = json.load(f)
    return j if isinstance(j, list) else (j.get(kind) or j.get("rows") or [])


def main(argv=None):
    argv = list(sys.argv[1:] if argv is None else argv)
    args = [a for a in argv if not a.startswith("--")]
    flag = lambda n: "--" + n in argv  # noqa: E731
    if not args or args[0] not in ("scan", "import"):
        sys.stderr.write(USAGE + "\n")
        return 2
    init()
    if args[0] == "scan":
        d = args[1] if len(args) > 1 else "."
        t0 = time.time()
        m = scan(d)
        if "--out" in argv and argv.index("--out") + 1 < len(argv):
            with open(argv[argv.index("--out") + 1], "w") as f:
                json.dump(m, f, indent=2)
        if flag("json"):
            print(json.dumps(m, indent=2))
        sys.stderr.write("scanned %s in %d ms: %d lines, %d routes, %d dependencies, %d features, %d integrations, "
                         "%d suspected secrets (counted, never sent)\n" % (
                             os.path.abspath(d), (time.time() - t0) * 1000, m["loc"], len(m["routes"]), len(m["dependencies"]),
                             len(m["features"]), len(m["integrations"]), m["security"]["secrets_suspected"]))
        if flag("send"):
            ok = send_manifest(m)
            sys.stderr.write("manifest sent\n" if ok else "manifest NOT sent (check LINEARJEPH_INGEST_KEY and the endpoint)\n")
            return 0 if ok else 1
        return 0
    if len(args) < 3 or args[1] not in ("events", "customers"):
        sys.stderr.write(USAGE + "\n")
        return 2
    try:
        rows = load_rows(args[2], args[1])
        r = import_history(args[1], rows, on_progress=lambda p: sys.stderr.write(
            "imported %(done)d/%(total)d (accepted %(accepted)d, rejected %(rejected)d)\n" % p))
    except (ProphecyError, OSError, ValueError) as e:
        sys.stderr.write("error: %s\n" % e)
        return 1
    sys.stderr.write("done: %d accepted, %d rejected\n" % (r["accepted"], r["rejected"]))
    for e in r["errors"]:
        sys.stderr.write("  %s\n" % json.dumps(e))
    return 1 if r["rejected"] and not r["accepted"] else 0


if __name__ == "__main__":
    sys.exit(main())
