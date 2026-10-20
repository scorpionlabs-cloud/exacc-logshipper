#!/usr/bin/env bash
# Runs a real collector -> local landing zone -> shipper -> durable receiver test.
# Requires Linux/macOS, Go >=1.22, Python 3, curl. No OCI tenancy or network required.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d -t exacc-e2e-XXXXXXXX)"
RECEIVER_PID=""
cleanup(){ if [[ -n "$RECEIVER_PID" ]]; then kill "$RECEIVER_PID" 2>/dev/null || true;wait "$RECEIVER_PID" 2>/dev/null || true;fi; if [[ "${KEEP_E2E_FILES:-0}" != "1" ]]; then rm -rf "$TMP"; else echo "Keeping $TMP"; fi; }
trap cleanup EXIT
export EXACC_MANIFEST_HMAC_KEY="DEMO-HMAC-DO-NOT-USE-IN-PRODUCTION-12345"
export EXACC_RECEIVER_TOKEN="DEMO-TOKEN-DO-NOT-USE-IN-PRODUCTION-67890"
mkdir -p "$TMP/bin" "$TMP/config" "$TMP/run"
cd "$ROOT"
go build -o "$TMP/bin/exacc-collect" ./cmd/exacc-collect
go build -o "$TMP/bin/exacc-shipper" ./cmd/exacc-shipper
go build -o "$TMP/bin/exacc-receiver" ./cmd/exacc-receiver
export EXACC_E2E_TMP="$TMP" EXACC_E2E_ROOT="$ROOT"
python3 - <<'PY'
import json, os, pathlib, socket
root=pathlib.Path(os.environ['EXACC_E2E_ROOT'])
tmp=pathlib.Path(os.environ['EXACC_E2E_TMP'])
with socket.socket() as sock:
 sock.bind(('127.0.0.1',0)); port=sock.getsockname()[1]
(tmp/'port').write_text(str(port))
for name in ('collector','shipper','receiver'):
 d=json.loads((root/'configs/local'/f'{name}.json').read_text())
 raw=json.dumps(d).replace('/tmp/exacc-demo',str(tmp/'run')).replace('18181',str(port))
 (tmp/'config'/f'{name}.json').write_text(raw)
(tmp/'run').mkdir(exist_ok=True)
(tmp/'run'/'alert.log').write_text('2026-10-19 INFO instance startup\n2026-10-19 WARN checkpoint slow\n2026-10-19 ORA-00257 password=exampleSecret\n')
PY
PORT="$(cat "$TMP/port")"
start_receiver(){ "$TMP/bin/exacc-receiver" -config "$TMP/config/receiver.json" >"$TMP/receiver.log" 2>&1 & RECEIVER_PID="$!"; for i in $(seq 1 30);do if curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1;then return;fi;sleep 0.15;done;cat "$TMP/receiver.log";exit 1; }
get_count(){ curl -fsS -H "Authorization: Bearer $EXACC_RECEIVER_TOKEN" "http://127.0.0.1:$PORT/v1/telemetry/stats" | python3 -c 'import json,sys;print(json.load(sys.stdin)["committed_batches"])'; }
assert_count(){ actual="$(get_count)";if [[ "$actual" != "$1" ]];then echo "FAIL: expected $1 committed batches, got $actual" >&2;exit 1;fi;echo "PASS: committed batches = $1"; }
start_receiver
"$TMP/bin/exacc-collect" -config "$TMP/config/collector.json"
"$TMP/bin/exacc-shipper" -config "$TMP/config/shipper.json" -once
assert_count 1
if grep -Rq 'exampleSecret' "$TMP/run/receiver-data/batches";then echo 'FAIL: unredacted secret in receiver' >&2;exit 1;fi
echo 'PASS: redaction applied to receiver records'
"$TMP/bin/exacc-collect" -config "$TMP/config/collector.json"
"$TMP/bin/exacc-shipper" -config "$TMP/config/shipper.json" -once
assert_count 1
echo '2026-10-19 ERROR new incident ORA-00060' >> "$TMP/run/alert.log"
"$TMP/bin/exacc-collect" -config "$TMP/config/collector.json"
"$TMP/bin/exacc-shipper" -config "$TMP/config/shipper.json" -once
assert_count 2
kill "$RECEIVER_PID";wait "$RECEIVER_PID" 2>/dev/null || true;RECEIVER_PID=""
echo '2026-10-19 INFO receiver temporarily offline' >> "$TMP/run/alert.log"
"$TMP/bin/exacc-collect" -config "$TMP/config/collector.json"
if "$TMP/bin/exacc-shipper" -config "$TMP/config/shipper.json" -once;then echo 'FAIL: shipper succeeded while receiver stopped' >&2;exit 1;else echo 'PASS: receiver outage detected; shipper checkpoint not committed';fi
start_receiver
"$TMP/bin/exacc-shipper" -config "$TMP/config/shipper.json" -once
assert_count 3
# Simulate a landing-zone failure and test durable spool retry.
mv "$TMP/run/landing" "$TMP/run/landing-backup"
printf 'unavailable\n' > "$TMP/run/landing"
echo '2026-10-19 INFO storage outage recovery test' >> "$TMP/run/alert.log"
if "$TMP/bin/exacc-collect" -config "$TMP/config/collector.json";then echo 'FAIL: collector accepted unavailable object store' >&2;exit 1;else echo 'PASS: collector held batch in spool during storage failure';fi
pending=$(find "$TMP/run/collector-spool" -name '*.pending.json' | wc -l)
if [[ $pending -lt 1 ]];then echo 'FAIL: no durable pending spool' >&2;exit 1;fi
rm "$TMP/run/landing"
mv "$TMP/run/landing-backup" "$TMP/run/landing"
"$TMP/bin/exacc-collect" -config "$TMP/config/collector.json"
"$TMP/bin/exacc-shipper" -config "$TMP/config/shipper.json" -once
assert_count 4
# Deliberately erase shipper receipts and replay; receiver deduplicates by batch ID.
find "$TMP/run/shipper-state/processed" -type f -delete
"$TMP/bin/exacc-shipper" -config "$TMP/config/shipper.json" -once
assert_count 4
python3 - <<'PY'
import json,os,pathlib
p=pathlib.Path(os.environ['EXACC_E2E_TMP'])/'run'/'receiver-data'/'batches'
events=[]
for path in p.glob('*.json'):events+=json.loads(path.read_text())['events']
assert len(events)==6,len(events)
assert len(set(event['event_id'] for event in events))==6
assert any(event.get('error_code')=='ORA-00257' for event in events)
print('PASS: 6 events, unique event IDs, ORA code extraction')
PY
echo 'ALL E2E TESTS PASSED'
