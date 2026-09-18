#!/bin/sh
# Install the latest strap release for this machine.
#
#   curl -fsSL https://raw.githubusercontent.com/stevemurr/strap/main/install.sh | sh
#
# STRAP_VERSION pins a tag (default: latest). STRAP_BIN chooses the install
# directory (default: the first writable of ~/.local/bin, /usr/local/bin).
set -eu

repo=stevemurr/strap
version=${STRAP_VERSION:-latest}

os=$(uname -s)
arch=$(uname -m)
case "$os" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "strap: unsupported OS $os" >&2; exit 1 ;;
esac
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "strap: unsupported architecture $arch" >&2; exit 1 ;;
esac
if [ "$os" = linux ] && [ "$arch" != amd64 ]; then
  echo "strap: releases cover linux/amd64 and macOS; build from source for linux/$arch" >&2
  exit 1
fi

if [ "$version" = latest ]; then
  version=$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
  [ -n "$version" ] || { echo "strap: could not determine the latest release" >&2; exit 1; }
fi

name="strap_${version}_${os}_${arch}"
url="https://github.com/$repo/releases/download/$version/$name.tar.gz"

bindir=${STRAP_BIN:-}
if [ -z "$bindir" ]; then
  for candidate in "$HOME/.local/bin" /usr/local/bin; do
    if [ -d "$candidate" ] && [ -w "$candidate" ]; then bindir=$candidate; break; fi
  done
fi
[ -n "$bindir" ] || { mkdir -p "$HOME/.local/bin"; bindir="$HOME/.local/bin"; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "strap: downloading $version for $os/$arch"
curl -fsSL "$url" | tar -xz -C "$tmp"
for cmd in strap strap-eval; do
  [ -f "$tmp/$name/$cmd" ] || continue
  install -m 0755 "$tmp/$name/$cmd" "$bindir/$cmd"
  echo "strap: installed $bindir/$cmd"
done

case ":$PATH:" in
  *":$bindir:"*) ;;
  *) echo "strap: add $bindir to PATH to use it" >&2 ;;
esac
echo "strap: point it at a model with ~/.config/strap/models.json (see the README)"
