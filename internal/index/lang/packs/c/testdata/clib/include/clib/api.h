#ifndef CLIB_API_H
#define CLIB_API_H

#include <stddef.h>

#define CLIB_VERSION "1.2.0"

typedef struct clib_ctx clib_ctx;

clib_ctx *clib_open(const char *path);
void clib_close(clib_ctx *ctx);

#endif
