//go:build zello && linux && arm

/* Runtime firmware loading for AD8DP's md380_vocoder.
 *
 * The library as distributed is linked against firmware.o and ram.o, which
 * objcopy-embed the MD380 firmware image into the artifact. Waypoint must not
 * ship that: the blob is licensed and may not be redistributed, so every .deb
 * and every image built that way would carry it.
 *
 * The bytes and the symbols are separable. md380_vocoder.o's references to
 * firmware functions (ambe_encode_thing2 and friends) are resolved at link time
 * from md380tools' symbols file, which is addresses only and carries no code.
 * The bytes those addresses point at can therefore be mapped at run time from a
 * file the operator supplies, which is what this does.
 *
 * md380_init() is then just the mprotect the library already performs.
 */
#include <sys/mman.h>
#include <fcntl.h>
#include <unistd.h>
#include <stdio.h>
#include <string.h>
#include <errno.h>

#define FW_ADDR   ((void*)0x0800c000)
#define FW_LEN    0xf2c00      /* D002.032 is exactly this long */
#define RAM_ADDR  ((void*)0x20000000)
#define RAM_LEN   0x20000
#define TCRAM_ADDR ((void*)0x10000000)
#define TCRAM_LEN 0x20000

static int map_file(const char *path, void *addr, size_t len) {
    int fd = open(path, O_RDONLY);
    if (fd < 0) { fprintf(stderr, "open %s: %s\n", path, strerror(errno)); return -1; }
    void *p = mmap(addr, len, PROT_READ|PROT_WRITE, MAP_PRIVATE|MAP_FIXED, fd, 0);
    close(fd);
    if (p == MAP_FAILED) { fprintf(stderr, "mmap %s: %s\n", path, strerror(errno)); return -1; }
    if (p != addr) { fprintf(stderr, "mmap %s landed at %p, wanted %p\n", path, p, addr); return -1; }
    return 0;
}

int wp_md380_load(const char *fwpath, const char *rampath) {
    if (mmap(TCRAM_ADDR, TCRAM_LEN, PROT_READ|PROT_WRITE,
             MAP_PRIVATE|MAP_ANONYMOUS|MAP_FIXED, -1, 0) == MAP_FAILED) {
        fprintf(stderr, "mmap tcram: %s\n", strerror(errno));
        return -1;
    }
    if (map_file(fwpath, FW_ADDR, FW_LEN)) return -2;
    if (map_file(rampath, RAM_ADDR, RAM_LEN)) return -3;
    return 0;
}
