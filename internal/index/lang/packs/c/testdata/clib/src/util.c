#include "util.h"
#include <string.h>

/* #include "removed.h" */

unsigned long
util_hash(const char *s, size_t n)
{
    unsigned long h = 5381;
    for (size_t i = 0; i < n; i++) {
        h = ((h << 5) + h) + (unsigned char)s[i];
    }
    return h;
}
