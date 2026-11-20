// SPDX-License-Identifier: AGPL-3.0-only
//
// libdrm_amdgpu.so.1 for mock GPUs: the handful of queries capacity readers
// (HAMi, device plugins) make, answered from the agent's rendered state.
// There is no DRM ioctl and no compute behind it. A handle only opens on a
// node the CUSE provider registered, never /dev/null, a plain file or a real
// GPU, and only while the GPU is healthy.
#include <amdgpu.h>
#include <amdgpu_drm.h>
#include <errno.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/sysmacros.h>

enum { MAX_CARDS = 64, RENDER_BASE = 128 };

// A handle is the address of its card's slot; nothing else is kept.
static struct card { int unused; } cards[MAX_CARDS];

static const char *state_root(void) {
    const char *root = getenv("AMD_DRIVER_MOCK_ROOT");
    return root ? root : "/var/lib/fake-gpu";
}

static FILE *open_state(const char *fmt, int card) {
    char rel[256], path[1024];
    snprintf(rel, sizeof rel, fmt, card);
    snprintf(path, sizeof path, "%s/%s", state_root(), rel);
    return fopen(path, "r");
}

// kfd_property reads one value of the card's KFD topology node.
static int kfd_property(int card, const char *key, uint64_t *out) {
    FILE *f = open_state("sys/class/kfd/kfd/topology/nodes/%d/properties", card + 1);
    if (!f)
        return -ENODEV;
    char line[512], name[128], value[128];
    int rc = -ENODATA;
    while (fgets(line, sizeof line, f)) {
        if (sscanf(line, "%127s %127s", name, value) != 2 || strcmp(name, key))
            continue;
        char *end;
        errno = 0;
        unsigned long long v = strtoull(value, &end, 0);
        if (!errno && !*end) {
            *out = v;
            rc = 0;
        }
        break;
    }
    fclose(f);
    return rc;
}

// smi_line reads line n (1-based) of the card's AMD SMI snapshot.
static int smi_line(int card, int n, char *buf, size_t size) {
    FILE *f = open_state("smi/gpu%d", card);
    if (!f)
        return -ENODATA;
    int ok = 1;
    for (int i = 0; i < n && ok; i++)
        ok = fgets(buf, (int)size, f) != NULL;
    fclose(f);
    if (!ok)
        return -ENODATA;
    buf[strcspn(buf, "\r\n")] = 0;
    return 0;
}

// healthy: the card is rendered with compute units and no fatal RAS error.
static int healthy(int card) {
    FILE *f = open_state("sys/class/kfd/kfd/topology/nodes/%d/ras/fatal_error", card + 1);
    if (f) {
        int fatal = 0, ok = fscanf(f, "%d", &fatal) == 1;
        fclose(f);
        if (!ok || fatal)
            return 0;
    }
    uint64_t simds;
    return kfd_property(card, "simd_count", &simds) == 0 && simds > 0;
}

static int card_of(amdgpu_device_handle dev) {
    for (int i = 0; i < MAX_CARDS; i++)
        if ((void *)dev == &cards[i])
            return i;
    return -1;
}

// provider_major is the dynamic major the CUSE provider got, or 0.
static unsigned provider_major(void) {
    char path[1024];
    snprintf(path, sizeof path, "%s/device-major", state_root());
    FILE *f = fopen(path, "r");
    unsigned major = 0;
    if (f) {
        if (fscanf(f, "%u", &major) != 1)
            major = 0;
        fclose(f);
    }
    return major;
}

// card_minor maps a cardN (0..63) or renderD (128..191) minor to N.
static int card_minor(unsigned minor) {
    if (minor < MAX_CARDS)
        return (int)minor;
    if (minor >= RENDER_BASE && minor < RENDER_BASE + MAX_CARDS)
        return (int)(minor - RENDER_BASE);
    return -1;
}

int amdgpu_device_initialize(int fd, uint32_t *major_version, uint32_t *minor_version,
                             amdgpu_device_handle *dev) {
    if (!major_version || !minor_version || !dev)
        return -EINVAL;
    *dev = NULL;
    struct stat st;
    if (fstat(fd, &st))
        return -errno;
    unsigned expected = provider_major();
    if (!expected || !S_ISCHR(st.st_mode) || major(st.st_rdev) != expected)
        return -ENODEV;
    int card = card_minor(minor(st.st_rdev));
    if (card < 0 || !healthy(card))
        return -ENODEV;
    *dev = (amdgpu_device_handle)&cards[card];
    *major_version = 3;
    *minor_version = 0;
    fprintf(stderr, "amd-driver-mock: libdrm initialize card%d (CUSE provider, library backend)\n", card);
    return 0;
}

int amdgpu_device_deinitialize(amdgpu_device_handle dev) {
    return card_of(dev) < 0 ? -EINVAL : 0;
}

// live resolves a handle to a healthy card, or a negative errno.
static int live(amdgpu_device_handle dev, const void *out) {
    int card = card_of(dev);
    if (card < 0 || !out)
        return -EINVAL;
    return healthy(card) ? card : -ENODEV;
}

int amdgpu_query_gpu_info(amdgpu_device_handle dev, struct amdgpu_gpu_info *info) {
    int card = live(dev, info);
    if (card < 0)
        return card;
    uint64_t simds, per_cu, device;
    if (kfd_property(card, "simd_count", &simds) || kfd_property(card, "simd_per_cu", &per_cu) ||
        !per_cu || simds % per_cu || kfd_property(card, "device_id", &device))
        return -ENODATA;
    memset(info, 0, sizeof *info);
    info->cu_active_number = (uint32_t)(simds / per_cu);
    info->asic_id = (uint32_t)device;
    info->family_id = 0; // the family is not modelled
    return 0;
}

// The two queries have different UAPI structures. A short request gets a
// prefix of its own structure, never a reinterpretation of the other.
static int vram_gtt(uint64_t vram, unsigned size, void *out) {
    struct drm_amdgpu_info_vram_gtt info = {0};
    if (!size || size > sizeof info)
        return -EINVAL;
    info.vram_size = vram;
    info.vram_cpu_accessible_size = vram;
    memcpy(out, &info, size);
    return 0;
}

static int memory(int card, uint64_t vram, unsigned size, void *out) {
    struct drm_amdgpu_memory_info info = {0};
    if (!size || size > sizeof info)
        return -EINVAL;
    char line[512];
    unsigned long long used_mib;
    if (smi_line(card, 1, line, sizeof line) || sscanf(line, "%*u %*u %*u %*u %llu", &used_mib) != 1 ||
        used_mib > vram >> 20)
        return -ENODATA;
    info.vram.total_heap_size = vram;
    info.vram.usable_heap_size = vram;
    info.vram.heap_usage = used_mib << 20;
    info.vram.max_allocation = vram - info.vram.heap_usage;
    // All profile VRAM counts as CPU accessible; GTT is not modelled. Neither
    // says anything about host memory.
    info.cpu_accessible_vram = info.vram;
    memcpy(out, &info, size);
    return 0;
}

int amdgpu_query_info(amdgpu_device_handle dev, unsigned id, unsigned size, void *out) {
    int card = live(dev, out);
    if (card < 0)
        return card;
    if (id != AMDGPU_INFO_MEMORY && id != AMDGPU_INFO_VRAM_GTT)
        return -EOPNOTSUPP;
    uint64_t vram;
    if (kfd_property(card, "local_mem_size", &vram))
        return -ENODATA;
    return id == AMDGPU_INFO_VRAM_GTT ? vram_gtt(vram, size, out) : memory(card, vram, size, out);
}

int amdgpu_query_firmware_version(amdgpu_device_handle dev, unsigned type, unsigned ip, unsigned index,
                                  uint32_t *version, uint32_t *feature) {
    (void)dev, (void)type, (void)ip, (void)index, (void)version, (void)feature;
    return -EOPNOTSUPP;
}

const char *amdgpu_get_marketing_name(amdgpu_device_handle dev) {
    static _Thread_local char name[256];
    int card = card_of(dev);
    if (card < 0 || !healthy(card) || smi_line(card, 2, name, sizeof name))
        return NULL;
    return name;
}
