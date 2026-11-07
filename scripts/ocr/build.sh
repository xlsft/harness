#!/usr/bin/env bash
# Builds Tesseract and Leptonica for WASI (wasm32-wasip1) with wasi-sdk, producing the OCR module that
# packages/client/documents runs under wazero. No JavaScript runtime, no image codecs (the client passes grey pixels),
# no threads, no exceptions, the legacy engine left out (tessdata_fast is LSTM only).
#
# Usage: scripts/ocr/build.sh [output.wasm]
#   OCR_WORK   where sources, the toolchain and objects go (default: a new temporary directory, removed at the end
#              unless OCR_KEEP=1). Needs about 1 GB.
#   JOBS       parallel compiles (default: CPU count).
#
# Every download is pinned by version and SHA-256. The script prints the size and SHA-256 of the module it writes.
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
OUT=${1:-$ROOT/packages/client/documents/ocrwasm/tesseract.wasm}

WASI_SDK_VERSION=34.0
WASI_SDK_TAG=wasi-sdk-34
TESSERACT_VERSION=5.5.4
TESSERACT_SHA256=a8dbf8b8c8ee427ff6ad1d99116502feaf59440af1adb893caf60a6576826cf4
LEPTONICA_VERSION=1.87.0
LEPTONICA_SHA256=c73363397f96eb1295602bf44d708a994ad42046c791bf03ea0505d829bdb6a7

case "$(uname -s)-$(uname -m)" in
Darwin-arm64) SDK_HOST=arm64-macos; SDK_SHA256=9c59398106b417f8f14913380fdf0097a8cc0ff4af9eb3ce0065a859e88d49e9 ;;
Darwin-x86_64) SDK_HOST=x86_64-macos; SDK_SHA256=87d27fa8adc68dee59bfbf2e22a6d34ef717c34d6bf1d8af2a56fc929d9ce0eb ;;
Linux-x86_64) SDK_HOST=x86_64-linux; SDK_SHA256=b761e3a0721dbae9c09a0059e5fdb2bf917d1b4a8a7b430fb3b5aafb0984b2c4 ;;
Linux-aarch64 | Linux-arm64) SDK_HOST=arm64-linux; SDK_SHA256=f7e243dff54d60bcc576e94d6166b69f410f2500ae4a9ceef34315be10e77971 ;;
*) echo "no pinned wasi-sdk for $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac

if command -v nproc >/dev/null; then CPUS=$(nproc); else CPUS=$(sysctl -n hw.ncpu); fi
JOBS=${JOBS:-$CPUS}

if [ -z "${OCR_WORK:-}" ]; then
	OCR_WORK=$(mktemp -d "${TMPDIR:-/tmp}/ocr-build.XXXXXX")
	if [ "${OCR_KEEP:-}" != 1 ]; then trap 'rm -rf "$OCR_WORK"' EXIT; fi
fi
mkdir -p "$OCR_WORK/dl" "$OCR_WORK/src" "$OCR_WORK/obj"
cd "$OCR_WORK"

sha256_stdin() { if command -v sha256sum >/dev/null; then sha256sum | cut -d' ' -f1; else shasum -a 256 | cut -d' ' -f1; fi; }
sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }

fetch() { # url file sha256
	local f="dl/$2"
	if [ ! -f "$f" ] || [ "$(sha256 "$f")" != "$3" ]; then
		curl -fsSL --retry 3 -o "$f.part" "$1"
		mv "$f.part" "$f"
	fi
	local got
	got=$(sha256 "$f")
	if [ "$got" != "$3" ]; then echo "checksum mismatch for $2: got $got, want $3" >&2; exit 1; fi
}

fetch "https://github.com/WebAssembly/wasi-sdk/releases/download/$WASI_SDK_TAG/wasi-sdk-$WASI_SDK_VERSION-$SDK_HOST.tar.gz" \
	"wasi-sdk-$WASI_SDK_VERSION-$SDK_HOST.tar.gz" "$SDK_SHA256"
fetch "https://github.com/tesseract-ocr/tesseract/archive/refs/tags/$TESSERACT_VERSION.tar.gz" \
	"tesseract-$TESSERACT_VERSION.tar.gz" "$TESSERACT_SHA256"
fetch "https://github.com/DanBloomberg/leptonica/releases/download/$LEPTONICA_VERSION/leptonica-$LEPTONICA_VERSION.tar.gz" \
	"leptonica-$LEPTONICA_VERSION.tar.gz" "$LEPTONICA_SHA256"

SDK=$OCR_WORK/wasi-sdk-$WASI_SDK_VERSION-$SDK_HOST
[ -d "$SDK" ] || tar xzf "dl/wasi-sdk-$WASI_SDK_VERSION-$SDK_HOST.tar.gz"
TESS=$OCR_WORK/src/tesseract-$TESSERACT_VERSION
LEPT=$OCR_WORK/src/leptonica-$LEPTONICA_VERSION
[ -d "$TESS" ] || tar xzf "dl/tesseract-$TESSERACT_VERSION.tar.gz" -C src
[ -d "$LEPT" ] || tar xzf "dl/leptonica-$LEPTONICA_VERSION.tar.gz" -C src

CC="$SDK/bin/clang"
CXX="$SDK/bin/clang++"
AR="$SDK/bin/llvm-ar"
TARGET=(--target=wasm32-wasip1 --sysroot="$SDK/share/wasi-sysroot")
# -O2 with SIMD128 lets the dot products vectorise; wazero compiles SIMD to native vector instructions.
OPT=(-O2 -msimd128 -fno-exceptions -ffunction-sections -fdata-sections -DNDEBUG)

# Generated headers: no image libraries, no optional features.
GEN=$OCR_WORK/gen
mkdir -p "$GEN/tesseract" "$GEN/lept"
cat >"$GEN/lept/config_auto.h" <<'EOF'
/* Leptonica for xlsft`s harness OCR: pixels come from the host, so no codecs. */
#define HAVE_LIBJPEG 0
#define HAVE_LIBTIFF 0
#define HAVE_LIBPNG 0
#define HAVE_LIBZ 0
#define HAVE_LIBGIF 0
#define HAVE_LIBUNGIF 0
#define HAVE_LIBWEBP 0
#define HAVE_LIBWEBP_ANIM 0
#define HAVE_LIBJP2K 0
#define HAVE_FMEMOPEN 0
#define HAVE_FSTATAT 0
#define HAVE_DIRFD 0
EOF
sed -e 's/@APPLE_UNIVERSAL_BUILD@/0/' -e 's/@ENDIANNESS@/L_LITTLE_ENDIAN/' "$LEPT/src/endianness.h.in" >"$GEN/lept/endianness.h"
cat >"$GEN/config_auto.h" <<EOF
/* Tesseract for xlsft\`s harness OCR. */
#define PACKAGE_VERSION "$TESSERACT_VERSION"
#define GRAPHICS_DISABLED 1
#define FAST_FLOAT 1
#define DISABLED_LEGACY_ENGINE 1
/* Keep line images as Leptonica pixes instead of PNG bytes: there is no PNG codec here. */
#define TESSERACT_IMAGEDATA_AS_PIX 1
EOF
IFS=. read -r TMAJ TMIN TMIC <<<"$TESSERACT_VERSION"
sed -e "s/@GENERIC_MAJOR_VERSION@/$TMAJ/" -e "s/@GENERIC_MINOR_VERSION@/$TMIN/" -e "s/@GENERIC_MICRO_VERSION@/$TMIC/" \
	-e "s/@PACKAGE_VERSION@/$TESSERACT_VERSION/" "$TESS/include/tesseract/version.h.in" >"$GEN/tesseract/version.h"

# Tesseract sources from its own CMake lists: the core sets, minus the legacy engine, the x86/ARM SIMD files and the
# ScrollView debugging viewer (sockets; GRAPHICS_DISABLED).
tess_set() { awk -v s="set($1" '$0 == s {on=1; next} on && /^\)/ {on=0} on {gsub(/[ \t]/, ""); if ($0 ~ /\.cpp$/) print}' "$TESS/cmake/SourceLists.cmake"; }
TESS_SRC=$(for s in API ARCH CCMAIN CCSTRUCT CCUTIL CLASSIFY CUTIL DICT LSTM TEXTORD WORDREC; do tess_set "TESSERACT_SRC_$s"; done | sort -u)
LEGACY=$(tess_set TESSERACT_SRC_LEGACY | sort -u)
TESS_SRC=$(comm -23 <(echo "$TESS_SRC") <(echo "$LEGACY"))

# Objects are kept per configuration, so a change of flags or headers recompiles everything.
OBJ=obj/$(cat "$GEN/config_auto.h" "$GEN/lept/config_auto.h" "$HERE/wasi_compat.h" "$HERE/build.sh" | sha256_stdin | cut -c1-16)
compile_c() { # src obj
	"$CC" "${TARGET[@]}" "${OPT[@]}" -std=gnu11 -DHAVE_CONFIG_H -D_WASI_EMULATED_PROCESS_CLOCKS -D_WASI_EMULATED_SIGNAL \
		-include "$HERE/wasi_compat.h" -I"$GEN/lept" -I"$LEPT/src" -w -c "$1" -o "$2"
}
compile_cxx() { # src obj
	"$CXX" "${TARGET[@]}" "${OPT[@]}" -std=c++17 -DHAVE_CONFIG_H -D_WASI_EMULATED_PROCESS_CLOCKS -D_WASI_EMULATED_SIGNAL \
		-I"$GEN" -I"$GEN/lept" -I"$TESS/include" -I"$LEPT/src" $(for d in "$TESS"/src/*/; do printf -- '-I%s ' "$d"; done) \
		-w -c "$1" -o "$2"
}
export -f compile_c compile_cxx
export CC CXX GEN LEPT TESS HERE OBJ
export TARGET_S="${TARGET[*]}" OPT_S="${OPT[*]}"

echo "compiling Leptonica $LEPTONICA_VERSION"
mkdir -p $OBJ/lept $OBJ/tess
ls "$LEPT"/src/*.c | xargs -P "$JOBS" -n 1 bash -c 'TARGET=($TARGET_S); OPT=($OPT_S); o=$OBJ/lept/$(basename "$0" .c).o; [ -f "$o" ] || compile_c "$0" "$o"'
echo "compiling Tesseract $TESSERACT_VERSION ($(echo "$TESS_SRC" | wc -l | tr -d ' ') files)"
echo "$TESS_SRC" | xargs -P "$JOBS" -n 1 bash -c 'TARGET=($TARGET_S); OPT=($OPT_S); o=$OBJ/tess/$(echo "$0" | tr / _ | sed "s/\.cpp$/.o/"); [ -f "$o" ] || compile_cxx "$TESS/$0" "$o"'
compile_cxx "$HERE/ocr_wasm.cpp" $OBJ/ocr_wasm.o
compile_c "$HERE/wasi_compat.c" $OBJ/wasi_compat.o

rm -f $OBJ/liblept.a $OBJ/libtess.a
"$AR" rcs $OBJ/liblept.a $OBJ/lept/*.o
"$AR" rcs $OBJ/libtess.a $OBJ/tess/*.o

echo "linking"
mkdir -p "$(dirname "$OUT")"
# A reactor: _initialize runs constructors, then the host calls the exports. The 1 MiB stack sits at the bottom of
# linear memory, which grows on demand up to the limit the host sets; the module never imports memory.
"$CXX" "${TARGET[@]}" "${OPT[@]}" -mexec-model=reactor -o "$OUT.tmp" $OBJ/ocr_wasm.o $OBJ/wasi_compat.o $OBJ/libtess.a $OBJ/liblept.a \
	-lwasi-emulated-process-clocks -lwasi-emulated-signal \
	-Wl,--gc-sections -Wl,--strip-all -Wl,-z,stack-size=1048576 -Wl,--initial-memory=33554432
mv "$OUT.tmp" "$OUT"
gzip -9 -n -c "$OUT" >"$OUT.gz"
sha256 "$OUT" >"$OUT.sha256"
echo "built $OUT: $(wc -c <"$OUT" | tr -d ' ') bytes, sha256 $(sha256 "$OUT")"
echo "compressed $OUT.gz: $(wc -c <"$OUT.gz" | tr -d ' ') bytes (the client embeds this one)"
