#!/usr/bin/env bash
set -euo pipefail

printf "SMB username: "
read -r smb_user

printf "SMB password: "
stty -echo
read -r smb_pass
stty echo
printf "\n"

printf "SMB domain/workgroup (blank ok): "
read -r smb_domain

tmp_file="$(mktemp)"
trap 'rm -f "$tmp_file"' EXIT

{
  printf "username=%s\n" "$smb_user"
  printf "password=%s\n" "$smb_pass"
  if [ -n "$smb_domain" ]; then
    printf "domain=%s\n" "$smb_domain"
  fi
} > "$tmp_file"

cmd.exe /c wsl.exe -d Ubuntu -u root -- sh -lc \
  "mkdir -p /etc/samba/credentials && chmod 700 /etc/samba/credentials && cat > /etc/samba/credentials/quantum-team && chmod 600 /etc/samba/credentials/quantum-team" \
  < "$tmp_file" >/dev/null

echo "Wrote /etc/samba/credentials/quantum-team"
