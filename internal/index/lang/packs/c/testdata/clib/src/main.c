#include <stdio.h>
#include <clib/api.h>
#include "util.h"

int main(int argc, char **argv)
{
    const char *msg = "#include \"fake.h\"";
    printf("%lu %s\n", util_hash(argv[0], 3), msg);
    return argc > 1 ? 0 : 1;
}
