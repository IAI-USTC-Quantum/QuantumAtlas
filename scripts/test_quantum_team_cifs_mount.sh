#!/usr/bin/env bash
set -u

mountpoint_dir=/mnt/team_cifs
base_credentials=/etc/samba/credentials/quantum-team
tmp_credentials=/tmp/quantum-team-creds-test

mkdir -p "$mountpoint_dir"

try_mount() {
  local server="$1"
  local domain="$2"
  local vers="$3"
  local sec="$4"

  grep -v '^domain=' "$base_credentials" > "$tmp_credentials"
  if [ -n "$domain" ]; then
    printf 'domain=%s\n' "$domain" >> "$tmp_credentials"
  fi
  chmod 600 "$tmp_credentials"

  if mountpoint -q "$mountpoint_dir"; then
    umount "$mountpoint_dir" || return 1
  fi

  local opts="credentials=$tmp_credentials,uid=1000,gid=1000,iocharset=utf8,vers=$vers,noperm"
  if [ -n "$sec" ]; then
    opts="$opts,sec=$sec"
  fi

  printf 'trying server=%s domain=%s vers=%s sec=%s ... ' "$server" "${domain:-<blank>}" "$vers" "${sec:-<default>}"
  if mount -t cifs "//$server/Team" "$mountpoint_dir" -o "$opts" >/tmp/cifs-test.err 2>&1; then
    echo "OK"
    findmnt -T "$mountpoint_dir" -o TARGET,SOURCE,FSTYPE,OPTIONS
    exit 0
  fi

  tr '\n' ' ' </tmp/cifs-test.err
  echo
}

for server in 10.100.158.17 28.0.0.87 Quantum.local; do
  for domain in "" Quantum WORKGROUP .; do
    for vers in 3.1.1 3.0 2.1; do
      for sec in "" ntlmssp ntlmv2; do
        try_mount "$server" "$domain" "$vers" "$sec"
      done
    done
  done
done

echo "No CIFS mount attempt succeeded."
exit 1
