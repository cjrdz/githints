#include <assert.h>
#include "../src/util.h"

int main(void)
{
    assert(util_hash("a", 1) != 0);
    return 0;
}
