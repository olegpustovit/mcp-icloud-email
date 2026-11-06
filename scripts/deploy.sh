#!/usr/bin/env bash
set -Eeuo pipefail

cd "$(dirname "$0")/.."
[[ "$(git branch --show-current)" == main ]]
git merge-base --is-ancestor HEAD origin/main
[[ -z "$(git status --porcelain --untracked-files=no)" ]]

if [[ -f .envrc ]]; then
  set -a
  source .envrc
  set +a
fi
[[ -n "${ICLOUD_EMAIL:-}" && -n "${ICLOUD_PASSWORD:-}" ]]

version=$(git rev-parse --short HEAD)
cd web
npm ci
npm test
npm run build
cd ..
go test -race ./imap ./internal/components/mailsync ./cmd/mail
go test ./...
go vet ./...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=$version" -o "/tmp/mail-server-$version" ./cmd/mail
scp "/tmp/mail-server-$version" oracle-vm:/home/snowbear/ci/mail/mail-server.next

deployed=0
rollback() {
  local status=$?
  trap - ERR
  if [[ $deployed == 1 ]]; then
    ssh oracle-vm bash -s <<'REMOTE' || true
set -euo pipefail
cd /home/snowbear/ci/mail
cp -p mail-server mail-server.failed
cp -p mail-server.previous mail-server.rollback
mv -f mail-server.rollback mail-server
smdctl restart mail
REMOTE
  fi
  exit "$status"
}
trap rollback ERR

ssh oracle-vm bash -s <<'REMOTE'
set -euo pipefail
cd /home/snowbear/ci/mail
cp -p mail-server mail-server.previous
mv mail-server.next mail-server
smdctl restart mail
REMOTE
deployed=1

ssh oracle-vm bash -s <<'REMOTE'
set -euo pipefail
smdctl status mail | grep -F 'Status:  active (running)'
curl -fsS http://127.0.0.1:4290/healthz >/dev/null
curl -fsS http://127.0.0.1:4290/healthz/sync >/dev/null
for attempt in {1..12}; do
  if ss -tnp | grep mail-server | grep -q ':993'; then exit 0; fi
  sleep 5
done
echo 'S0 FAIL: no iCloud IMAP sync socket' >&2
exit 1
REMOTE
curl -fsS https://mail.microapps.space/ >/dev/null
curl -fsS https://mail.microapps.space/healthz/sync

sleep 600
python3 scripts/check-sync-parity.py
trap - ERR
echo "Deploy PASS: $version"
