# Licenses of the OCR module

`tesseract.wasm.gz` is built by `scripts/ocr/build.sh` and embedded in the client. It contains:

| Component | Version | License | Text |
|---|---|---|---|
| Tesseract OCR | 5.5.4 | Apache-2.0 | `LICENSE.tesseract` |
| Leptonica | 1.87.0 | BSD-2-Clause | `LICENSE.leptonica` |
| wasi-libc (from wasi-sdk 34) | wasi-sdk 34 | Apache-2.0 WITH LLVM-exception OR Apache-2.0 OR MIT; its musl parts MIT | https://github.com/WebAssembly/wasi-libc/blob/main/LICENSE |
| libc++, libc++abi (from wasi-sdk 34) | LLVM 23 | Apache-2.0 WITH LLVM-exception | https://github.com/llvm/llvm-project/blob/main/LICENSE.TXT |
| `scripts/ocr/ocr_wasm.cpp`, `wasi_compat.c` | | MIT, as the rest of xlsft`s harness | `/LICENSE` |

Trained data is not embedded. The client fetches `tessdata_fast` 4.1.0 (`eng`, `rus`; Apache-2.0,
https://github.com/tesseract-ocr/tessdata_fast/blob/4.1.0/LICENSE) on first use and checks each file's SHA-256.

No GPL or AGPL code is included: Leptonica is built without its optional image libraries, Tesseract without libarchive,
libcurl and OpenMP.
