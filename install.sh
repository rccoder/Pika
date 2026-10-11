#!/bin/sh
set -eu

fail() { printf '%s\n' "$*" >&2; exit 1; }
case "$(uname -s)" in
  Darwin) pika_os=darwin ;;
  Linux) pika_os=linux ;;
  *) fail 'Pika supports macOS and Linux.' ;;
esac
case "$(uname -m)" in
  x86_64|amd64) pika_arch=amd64 ;;
  arm64|aarch64) pika_arch=arm64 ;;
  *) fail 'Unsupported CPU architecture.' ;;
esac
command -v curl >/dev/null || fail 'Please install curl first.'
command -v tar >/dev/null || fail 'Please install tar first.'
pika_dir=${PIKA_INSTALL_DIR:-"$HOME/.local/bin"}
pika_tmp=$(mktemp -d)
trap 'rm -rf "$pika_tmp"' EXIT HUP INT TERM
if [ -n "${PIKA_VERSION:-}" ]; then
  pika_tag=$PIKA_VERSION
else
  pika_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' https://github.com/rccoder/Pika/releases/latest)
  pika_tag=${pika_url##*/}
fi
case "$pika_tag" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) fail 'No published Pika release found, or invalid PIKA_VERSION.' ;;
esac
pika_asset="pika_${pika_tag}_${pika_os}_${pika_arch}.tar.gz"
pika_base="https://github.com/rccoder/Pika/releases/download/$pika_tag"
curl -fSL --retry 3 "$pika_base/$pika_asset" -o "$pika_tmp/$pika_asset"
curl -fsSL --retry 3 "$pika_base/SHA256SUMS" -o "$pika_tmp/SHA256SUMS"
pika_expected=$(awk -v asset="$pika_asset" '$2 == asset {print $1}' "$pika_tmp/SHA256SUMS")
[ -n "$pika_expected" ] || fail 'Release checksum missing.'
if command -v sha256sum >/dev/null; then
  pika_actual=$(sha256sum "$pika_tmp/$pika_asset" | awk '{print $1}')
elif command -v shasum >/dev/null; then
  pika_actual=$(shasum -a 256 "$pika_tmp/$pika_asset" | awk '{print $1}')
else
  fail 'Please install sha256sum or shasum.'
fi
[ "$pika_actual" = "$pika_expected" ] || fail 'Checksum mismatch; installation stopped.'
tar -xzf "$pika_tmp/$pika_asset" -C "$pika_tmp" pika
mkdir -p "$pika_dir"
install -m 755 "$pika_tmp/pika" "$pika_dir/pika"
printf 'Installed Pika %s to %s/pika\n' "$pika_tag" "$pika_dir"
case ":$PATH:" in
  *":$pika_dir:"*) ;;
  *) printf 'Add this line to ~/.zshrc or ~/.bashrc, then reopen your terminal:\nexport PATH="%s:$PATH"\n' "$pika_dir" ;;
esac
command -v ffmpeg >/dev/null || printf 'Install ffmpeg before downloading music; see the README.\n'
