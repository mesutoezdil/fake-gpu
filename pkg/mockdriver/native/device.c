// SPDX-License-Identifier: AGPL-3.0-only
//
// mock-device NAME MAJOR MINOR
//
// Registers one character device through CUSE, so /dev/kfd and the DRM
// nodes can really be opened. Opening is all it does: reads and writes fail
// with EOPNOTSUPP and every ioctl with ENOTTY, so nothing can mistake it for
// a GPU that executes work. libdrm queries are answered by drm.c instead.
#define FUSE_USE_VERSION 31
#include <cuse_lowlevel.h>
#include <errno.h>
#include <fuse_lowlevel.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

enum { MAX_MAJOR = 4095, MAX_MINOR = 255, MAX_NAME = 80 };

static void on_open(fuse_req_t req, struct fuse_file_info *fi) {
    fuse_reply_open(req, fi);
}

static void on_read(fuse_req_t req, size_t size, off_t off, struct fuse_file_info *fi) {
    (void)size, (void)off, (void)fi;
    fuse_reply_err(req, EOPNOTSUPP);
}

static void on_write(fuse_req_t req, const char *buf, size_t size, off_t off, struct fuse_file_info *fi) {
    (void)buf, (void)size, (void)off, (void)fi;
    fuse_reply_err(req, EOPNOTSUPP);
}

static void on_ioctl(fuse_req_t req, int cmd, void *arg, struct fuse_file_info *fi, unsigned flags,
                     const void *in, size_t in_size, size_t out_size) {
    (void)cmd, (void)arg, (void)fi, (void)flags, (void)in, (void)in_size, (void)out_size;
    fuse_reply_err(req, ENOTTY);
}

// number parses a whole decimal argument no larger than max.
static int number(const char *s, unsigned long max, unsigned *out) {
    char *end;
    errno = 0;
    unsigned long v = strtoul(s, &end, 10);
    if (errno || end == s || *end || v > max)
        return -1;
    *out = (unsigned)v;
    return 0;
}

int main(int argc, char **argv) {
    if (argc != 4) {
        fprintf(stderr, "usage: mock-device NAME MAJOR MINOR\n");
        return 2;
    }
    const char *name = argv[1];
    unsigned major, minor;
    if (number(argv[2], MAX_MAJOR, &major) || number(argv[3], MAX_MINOR, &minor) ||
        strlen(name) > MAX_NAME || strchr(name, '/'))
        return 2;

    char devname[sizeof "DEVNAME=" + MAX_NAME];
    snprintf(devname, sizeof devname, "DEVNAME=%s", name);
    const char *info[] = {devname};
    const struct cuse_info ci = {
        .dev_major = major,
        .dev_minor = minor,
        .dev_info_argc = 1,
        .dev_info_argv = info,
    };
    const struct cuse_lowlevel_ops ops = {
        .open = on_open,
        .read = on_read,
        .write = on_write,
        .ioctl = on_ioctl,
    };
    char *fuse_args[] = {argv[0], "-f"}; // stay in the foreground: the agent supervises us
    return cuse_lowlevel_main(2, fuse_args, &ci, &ops, NULL);
}
