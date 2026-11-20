#!/bin/sh
# Makes the icons of the notification area from the SVGs here, with ImageMagick and librsvg (MAGICK says where magick
# is, if not on the path): the .ico files of Windows, 16 to 64 px, and the PNG files of Linux, 64 px. Every size is
# drawn at four times its size and scaled down, which keeps the edges sharpest. Run it in this folder.
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
    # Linux takes a PNG and scales it to its panel
    "$magick" -background none -density 768 "$name.svg" -filter Lanczos -resize 64x64 -strip "$name.png"
done

# the icon of the program, which go-winres makes the Windows resource of, see ../winres/winres.json
"$magick" -background none -density 3072 pinger.svg -filter Lanczos -resize 256x256 -strip program.png

# the icons of the page, as Watcher CRM and NMS have them: the browser takes the SVG, or the ICO where it cannot,
# a phone the PNG on its home screen, square, as it rounds the corners itself
web=../../web/static
sed '/^<!--/,/^-->/d' pinger.svg >"$web/favicon.svg"
sizes=""
for size in 16 32 48; do
    "$magick" -background none -density $((12 * size)) pinger.svg -filter Lanczos -resize "${size}x${size}" "$tmp/favicon-$size.png"
    sizes="$sizes $tmp/favicon-$size.png"
done
# shellcheck disable=SC2086
"$magick" $sizes "$web/favicon.ico"
"$magick" -background "#2563eb" -density 2160 pinger.svg -filter Lanczos -resize 180x180 -flatten -strip "$web/apple-touch-icon.png"
