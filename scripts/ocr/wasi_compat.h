/* Declarations wasi-libc leaves out that Leptonica refers to on paths the OCR module never takes (temporary files,
 * debug display). The stubs in wasi_compat.c fail, so a call reports an error instead of trapping. */
#ifndef OCR_WASI_COMPAT_H
#define OCR_WASI_COMPAT_H
#ifdef __cplusplus
extern "C" {
#endif
int mkstemp(char *pattern);
#ifdef __cplusplus
}
#endif
#endif
