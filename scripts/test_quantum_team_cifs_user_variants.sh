#!/usr/bin/env bash
set -u

mountpoint_dir=/mnt/team_cifs
base_credentials=/etc/samba/credentials/quantum-team
tmp_credentials=/tmp/quantum-team-creds-user-test

mkdir -p "$mountpoint_dir"

base_user="$(sed -n 's/^username=//p' "$base_credentials" | head -n 1)"
password_line="$(sed -n '/^password=/p' "$base_credentials" | head -n 1)"

write_credentials() {
  local user="$1"
  local domain="$2"
  {
    printf 'username=%s\n' "$user"
    printf '%s\n' "$password_line"
    if [ -n "$domain" ]; then
      printf 'domain=%s\n' "$domain"
    fi
  } > "$tmp_credentials"
  chmod 600 "$tmp_credentials"
}

try_mount() {
  local unc="$1"
  local extra="$2"
  local user="$3"
  local domain="$4"

  write_credentials "$user" "$domain"

  if mountpoint -q "$mountpoint_dir"; then
    umount "$mountpoint_dir" || return 1
  fi

  local opts="credentials=$tmp_credentials,uid=1000,gid=1000,iocharset=utf8,vers=3.1.1,noperm,sec=ntlmssp"
  if [ -n "$extra" ]; then
    opts="$opts,$extra"
  fi

  printf 'trying unc=%s user=%s domain=%s extra=%s ... ' "$unc" "$user" "${domain:-<blank>}" "${extra:-<none>}"
  if mount -t cifs "$unc" "$mountpoint_dir" -o "$opts" >/tmp/cifs-user-test.err 2>&1; then
    echo "OK"
    findmnt -T "$mountpoint_dir" -o TARGET,SOURCE,FSTYPE,OPTIONS
    exit 0
  fi

  tr '\n' ' ' </tmp/cifs-user-test.err
  echo
}

for unc in "//10.100.158.17/Team" "//Quantum/Team"; do
  for extra in "" "ip=10.100.158.17" "addr=10.100.158.17"; do
    for user in "$base_user" "Quantum\\$base_user" "WORKGROUP\\$base_user" ".\\$base_user" "$base_user@Quantum"; do
      for domain in "" Quantum WORKGROUP .; do
        try_mount "$unc" "$extra" "$user" "$domain"
      done
    done
  done
done

echo "No username/path variant succeeded."
exit 1
