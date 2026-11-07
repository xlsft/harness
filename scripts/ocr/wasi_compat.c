/* Stubs for functions wasi-libc does not have; see wasi_compat.h. Leptonica refers to them for temporary files and
 * for showing debug images, which the OCR module never does: each fails with ENOSYS rather than trapping. */
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>

#include "wasi_compat.h"

int mkstemp(char *pattern) {
  (void)pattern;
  errno = ENOSYS;
  return -1;
}

FILE *tmpfile(void) {
  errno = ENOSYS;
  return NULL;
}

int system(const char *command) {
  (void)command;
  errno = ENOSYS;
  return -1;
}
