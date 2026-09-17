#!/bin/bash
# Build local release archives and update the Homebrew formula. Does not publish.
set -euo pipefail
cd "$(dirname "$0")/.."
version="$(sed -n 's/^const version = "\([^"]*\)"/\1/p' main.go)"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo 'Expected a numeric release version in main.go' >&2
  exit 1
fi
mkdir -p dist
: > dist/THIRD_PARTY_LICENSES.txt
while read -r module module_dir; do
  [[ -n "$module_dir" ]] || continue
  for notice in "$module_dir"/LICENSE* "$module_dir"/COPYING*; do
    [[ -f "$notice" ]] || continue
    printf '\n=== %s / %s ===\n\n' "$module" "$(basename "$notice")" >> dist/THIRD_PARTY_LICENSES.txt
    cat "$notice" >> dist/THIRD_PARTY_LICENSES.txt
  done
done < <(go list -m -f '{{if not .Main}}{{.Path}} {{.Dir}}{{end}}' all)
for arch in arm64 amd64; do
  stage="dist/stage-$arch"
  mkdir -p "$stage"
  CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" go build -trimpath -buildvcs=false -ldflags='-s -w' -o "$stage/sweep" .
  cp README.md "$stage/README.md"
  cp dist/THIRD_PARTY_LICENSES.txt "$stage/THIRD_PARTY_LICENSES.txt"
  files=(sweep README.md THIRD_PARTY_LICENSES.txt)
  if [[ -f LICENSE ]]; then
    cp LICENSE "$stage/LICENSE"
    files+=(LICENSE)
  fi
  COPYFILE_DISABLE=1 tar -czf "dist/sweep_${version}_darwin_${arch}.tar.gz" -C "$stage" "${files[@]}"
done
(cd dist && shasum -a 256 "sweep_${version}_darwin_arm64.tar.gz" "sweep_${version}_darwin_amd64.tar.gz" > checksums.txt)
arm_sha="$(shasum -a 256 "dist/sweep_${version}_darwin_arm64.tar.gz" | cut -d ' ' -f 1)"
intel_sha="$(shasum -a 256 "dist/sweep_${version}_darwin_amd64.tar.gz" | cut -d ' ' -f 1)"
license_line=''
if [[ -f LICENSE ]] && head -1 LICENSE | grep -qx 'MIT License'; then
  license_line='  license "MIT"'
fi
cat > Formula/sweep.rb <<FORMULA
class Sweep < Formula
  desc "Interactive macOS disk cleaner with a cached file tree"
  homepage "https://github.com/jacobslunga/sweep"
  version "$version"
$license_line
  depends_on :macos

  on_arm do
    url "https://github.com/jacobslunga/sweep/releases/download/v${version}/sweep_${version}_darwin_arm64.tar.gz"
    sha256 "$arm_sha"
  end

  on_intel do
    url "https://github.com/jacobslunga/sweep/releases/download/v${version}/sweep_${version}_darwin_amd64.tar.gz"
    sha256 "$intel_sha"
  end

  def install
    bin.install "sweep"
  end

  test do
    assert_equal "sweep #{version}", shell_output("#{bin}/sweep --version").strip
  end
end
FORMULA
printf 'Packaged Sweep %s in dist/ and updated Formula/sweep.rb\n' "$version"
