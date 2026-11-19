#!/bin/sh
# Makes the .ico files of the notification area from the SVGs here, with ImageMagick and librsvg (MAGICK says where
# magick is, if not on the path), 16 to 64 px. Every size is drawn at four times its size and scaled down, which keeps
# the edges sharpest. Run it in this folder.
set -eu
magick=${MAGICK:-magick}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

for name in pinger all-up losses outage no-network; do
    sizes=""
    for size in 16 20 24 32 40 48 64; do
        "$magick" -background none -density $((12 * size)) "$name.svg" -filter Lanczos -resize "${size}x${size}" "$tmp/$name-$size.png"
        sizes="$sizes $tmp/$name-$size.png"
    done
    # shellcheck disable=SC2086
    "$magick" $sizes "$name.ico"
done
