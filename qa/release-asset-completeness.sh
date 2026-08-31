#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 RELEASE_ASSET_DIR" >&2
  exit 64
fi

asset_dir=$1
if [[ ! -d "$asset_dir" || -L "$asset_dir" ]]; then
  echo "release asset directory must be a real directory: $asset_dir" >&2
  exit 1
fi

shopt -s nullglob
archives=("$asset_dir"/*.tar.gz)
checksums=("$asset_dir"/*.tar.gz.sha256)
apks=("$asset_dir"/*.apk)

if (( ${#apks[@]} != 0 )); then
  echo "refusing to publish Android APKs: signed Android release packaging is not enabled" >&2
  exit 1
fi

if (( ${#archives[@]} == 0 )); then
  echo "no release archives found" >&2
  exit 1
fi

for path in "$asset_dir"/*; do
  [[ -e "$path" || -L "$path" ]] || continue
  if [[ -L "$path" || ! -f "$path" ]]; then
    echo "release asset must be a regular non-symlink file: $path" >&2
    exit 1
  fi
  case "$(basename "$path")" in
    *.tar.gz|*.tar.gz.sha256) ;;
    *)
      echo "unexpected release asset: $path" >&2
      exit 1
      ;;
  esac
done

for archive in "${archives[@]}"; do
  sum="$archive.sha256"
  if [[ ! -f "$sum" || -L "$sum" || ! -s "$sum" ]]; then
    echo "missing checksum for $(basename "$archive")" >&2
    exit 1
  fi
done

for sum in "${checksums[@]}"; do
  archive=${sum%.sha256}
  if [[ ! -f "$archive" || -L "$archive" ]]; then
    echo "orphan checksum without archive: $(basename "$sum")" >&2
    exit 1
  fi
  expected_name=$(basename "$archive")
  recorded_name=$(awk 'NR==1 { sub(/^\*/, "", $2); print $2 }' "$sum")
  if [[ "$recorded_name" != "$expected_name" ]]; then
    echo "checksum filename mismatch in $(basename "$sum"): expected $expected_name, got ${recorded_name:-<empty>}" >&2
    exit 1
  fi
  if [[ $(wc -l < "$sum") -ne 1 ]]; then
    echo "checksum file must contain exactly one record: $(basename "$sum")" >&2
    exit 1
  fi
  (cd "$asset_dir" && sha256sum -c "$(basename "$sum")")
done

if (( ${#archives[@]} != ${#checksums[@]} )); then
  echo "release archive/checksum count mismatch" >&2
  exit 1
fi

printf '%s\n' "${checksums[@]##*/}" | LC_ALL=C sort | while IFS= read -r sum; do
  cat "$asset_dir/$sum"
done > "$asset_dir/SHA256SUMS"

test -s "$asset_dir/SHA256SUMS"
echo "release asset completeness: PASS (${#archives[@]} archives)"
