// SPDX-License-Identifier: AGPL-3.0-only
//
// libamd_smi.so.27: the AMD SMI ABI 27 calls AMD's GPU Agent, its metrics
// exporter and discovery clients such as HAMi make, answered from the
// snapshots the agent writes (pkg/smistate). Every other ABI function is
// generated into unsupported.c and returns AMDSMI_STATUS_NOT_SUPPORTED.
#include "amdsmi.h"
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

enum {
    MAX_GPUS = 64,
    GPU_BASE = 0x1000, // processor handle of GPU i is GPU_BASE + i
    SOCKET = 0xA000,   // the one socket handle
};

// Model constants of the MI300X the exporter dashboards were built against.
enum {
    VENDOR_ID = 0x1002,
    DEVICE_ID = 0x74a1,
    COMPUTE_UNITS = 304,
    GFX_TARGET = 90402,
    MAX_GFX_MHZ = 2100,
    MAX_MEM_MHZ = 1300,
    PCIE_WIDTH = 16,
    PCIE_GEN = 5,
};

// snapshot is one GPU's state file. The status row is optional: snapshots
// of older agents end after the PCI address.
struct snapshot {
    int temp, power, cap, util, used_mib, total_mib, ecc, gfx_mhz, mem_mhz;
    char name[128], uuid[128], bdf[64], status[32];
    int throttled, retired;
};

static int gpu_count;
static int initialized;
// Calls of one thread see one snapshot; threads never share it.
static _Thread_local struct snapshot gpu;
static _Thread_local int gpu_index;

static const char *state_dir(void) {
    const char *dir = getenv("MOCK_AMDSMI_STATE_DIR");
    return dir ? dir : "/var/lib/fake-gpu/smi";
}

static FILE *open_state(const char *name) {
    char path[512];
    snprintf(path, sizeof path, "%s/%s", state_dir(), name);
    return fopen(path, "r");
}

static int read_snapshot(int i, struct snapshot *s) {
    char name[16];
    snprintf(name, sizeof name, "gpu%d", i);
    FILE *f = open_state(name);
    if (!f)
        return 0;
    int fields = fscanf(f, "%d %d %d %d %d %d %d %d %d\n%127[^\n]\n%127[^\n]\n%63[^\n]", &s->temp, &s->power,
                        &s->cap, &s->util, &s->used_mib, &s->total_mib, &s->ecc, &s->gfx_mhz, &s->mem_mhz, s->name,
                        s->uuid, s->bdf);
    if (fields != 12 || fscanf(f, "\n%31s %d %d", s->status, &s->throttled, &s->retired) != 3) {
        strcpy(s->status, "healthy");
        s->throttled = s->retired = 0;
    }
    fclose(f);
    return fields == 12;
}

static int index_of(amdsmi_processor_handle h) {
    uintptr_t v = (uintptr_t)h;
    return initialized && v >= GPU_BASE && v < (uintptr_t)GPU_BASE + (uintptr_t)gpu_count ? (int)(v - GPU_BASE) : -1;
}

static amdsmi_processor_handle handle_of(int i) { return (amdsmi_processor_handle)(uintptr_t)(GPU_BASE + i); }

// load makes gpu and gpu_index describe h, or says why it cannot.
static amdsmi_status_t load(amdsmi_processor_handle h, const void *out) {
    gpu_index = index_of(h);
    if (gpu_index < 0 || !out)
        return AMDSMI_STATUS_INVAL;
    return read_snapshot(gpu_index, &gpu) ? AMDSMI_STATUS_SUCCESS : AMDSMI_STATUS_NOT_FOUND;
}

#define LOAD(h, out)                                                                                                   \
    do {                                                                                                               \
        amdsmi_status_t rc_ = load((h), (out));                                                                        \
        if (rc_ != AMDSMI_STATUS_SUCCESS)                                                                              \
            return rc_;                                                                                                \
    } while (0)

static int xgmi_down(void) { return !strcmp(gpu.status, "xgmi_link_down"); }

static int parse_bdf(const char *s, unsigned *domain, unsigned *bus, unsigned *dev, unsigned *fn) {
    return sscanf(s, "%x:%x:%x.%x", domain, bus, dev, fn) == 4;
}

// --- Lifecycle and enumeration -------------------------------------------

amdsmi_status_t amdsmi_init(uint64_t flags) {
    (void)flags;
    FILE *f = open_state("count");
    if (!f)
        return AMDSMI_STATUS_NOT_FOUND;
    int ok = fscanf(f, "%d", &gpu_count) == 1;
    fclose(f);
    if (!ok || gpu_count < 1 || gpu_count > MAX_GPUS)
        return AMDSMI_STATUS_INVAL;
    initialized = 1;
    fprintf(stderr, "fake-gpu: AMD SMI ABI 27 backend initialized\n");
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_shut_down(void) {
    initialized = 0;
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_socket_handles(uint32_t *count, amdsmi_socket_handle *sockets) {
    if (!count)
        return AMDSMI_STATUS_INVAL;
    if (sockets) {
        if (*count < 1)
            return AMDSMI_STATUS_OUT_OF_RESOURCES;
        sockets[0] = (amdsmi_socket_handle)(uintptr_t)SOCKET;
    }
    *count = 1;
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_socket_info(amdsmi_socket_handle socket, size_t len, char *name) {
    if ((uintptr_t)socket != SOCKET || !name || !len)
        return AMDSMI_STATUS_INVAL;
    snprintf(name, len, "AMD mock socket");
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_processor_handles(amdsmi_socket_handle socket, uint32_t *count,
                                             amdsmi_processor_handle *handles) {
    if ((uintptr_t)socket != SOCKET || !count)
        return AMDSMI_STATUS_INVAL;
    if (handles) {
        if (*count < (uint32_t)gpu_count)
            return AMDSMI_STATUS_OUT_OF_RESOURCES;
        for (int i = 0; i < gpu_count; i++)
            handles[i] = handle_of(i);
    }
    *count = (uint32_t)gpu_count;
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_processor_type(amdsmi_processor_handle h, processor_type_t *type) {
    LOAD(h, type);
    *type = AMDSMI_PROCESSOR_TYPE_AMD_GPU;
    return AMDSMI_STATUS_SUCCESS;
}

// --- Identity ------------------------------------------------------------

amdsmi_status_t amdsmi_get_gpu_device_bdf(amdsmi_processor_handle h, amdsmi_bdf_t *bdf) {
    LOAD(h, bdf);
    memset(bdf, 0, sizeof *bdf);
    unsigned domain, bus, dev, fn;
    if (!parse_bdf(gpu.bdf, &domain, &bus, &dev, &fn))
        return AMDSMI_STATUS_INVAL;
    bdf->domain_number = domain;
    bdf->bus_number = bus;
    bdf->device_number = dev;
    bdf->function_number = fn;
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_gpu_bdf_id(amdsmi_processor_handle h, uint64_t *id) {
    LOAD(h, id);
    unsigned domain, bus, dev, fn;
    if (!parse_bdf(gpu.bdf, &domain, &bus, &dev, &fn))
        return AMDSMI_STATUS_INVAL;
    *id = (uint64_t)domain << 32 | (uint64_t)bus << 8 | (uint64_t)dev << 3 | fn;
    return AMDSMI_STATUS_SUCCESS;
}

// Discovery clients look a GPU up by address. The answer comes from the same
// snapshots as enumeration; an unknown or missing GPU never resolves.
amdsmi_status_t amdsmi_get_processor_handle_from_bdf(amdsmi_bdf_t bdf, amdsmi_processor_handle *handle) {
    if (!handle)
        return AMDSMI_STATUS_INVAL;
    *handle = NULL;
    if (!initialized)
        return AMDSMI_STATUS_NOT_INIT;
    for (int i = 0; i < gpu_count; i++) {
        amdsmi_bdf_t candidate = {0};
        if (amdsmi_get_gpu_device_bdf(handle_of(i), &candidate) == AMDSMI_STATUS_SUCCESS &&
            candidate.as_uint == bdf.as_uint) {
            *handle = handle_of(i);
            return AMDSMI_STATUS_SUCCESS;
        }
    }
    return AMDSMI_STATUS_NOT_FOUND;
}

amdsmi_status_t amdsmi_get_gpu_device_uuid(amdsmi_processor_handle h, unsigned int *len, char *uuid) {
    LOAD(h, len);
    unsigned need = (unsigned)strlen(gpu.uuid) + 1;
    if (uuid && *len < need) {
        *len = need;
        return AMDSMI_STATUS_OUT_OF_RESOURCES;
    }
    if (uuid)
        snprintf(uuid, *len, "%s", gpu.uuid);
    *len = need;
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_gpu_asic_info(amdsmi_processor_handle h, amdsmi_asic_info_t *info) {
    LOAD(h, info);
    memset(info, 0, sizeof *info);
    snprintf(info->market_name, sizeof info->market_name, "%s", gpu.name);
    snprintf(info->vendor_name, sizeof info->vendor_name, "AMD");
    info->vendor_id = VENDOR_ID;
    info->device_id = DEVICE_ID;
    info->oam_id = (uint32_t)gpu_index;
    info->num_of_compute_units = COMPUTE_UNITS;
    info->target_graphics_version = GFX_TARGET;
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_gpu_board_info(amdsmi_processor_handle h, amdsmi_board_info_t *info) {
    LOAD(h, info);
    memset(info, 0, sizeof *info);
    snprintf(info->model_number, sizeof info->model_number, "%s", gpu.name);
    snprintf(info->product_name, sizeof info->product_name, "%s", gpu.name);
    snprintf(info->product_serial, sizeof info->product_serial, "MOCK-%04d", gpu_index);
    snprintf(info->manufacturer_name, sizeof info->manufacturer_name, "AMD");
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_gpu_kfd_info(amdsmi_processor_handle h, amdsmi_kfd_info_t *info) {
    LOAD(h, info);
    memset(info, 0, sizeof *info);
    info->kfd_id = (uint64_t)gpu_index + 1; // KFD node 0 is the CPU
    info->node_id = (uint32_t)gpu_index + 1;
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_gpu_enumeration_info(amdsmi_processor_handle h, amdsmi_enumeration_info_t *info) {
    LOAD(h, info);
    memset(info, 0, sizeof *info);
    info->drm_render = 128 + (uint32_t)gpu_index;
    info->drm_card = (uint32_t)gpu_index;
    info->hsa_id = (uint32_t)gpu_index;
    info->hip_id = (uint32_t)gpu_index;
    snprintf(info->hip_uuid, sizeof info->hip_uuid, "%s", gpu.uuid);
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_gpu_driver_info(amdsmi_processor_handle h, amdsmi_driver_info_t *info) {
    LOAD(h, info);
    memset(info, 0, sizeof *info);
    snprintf(info->driver_name, sizeof info->driver_name, "amdgpu");
    snprintf(info->driver_version, sizeof info->driver_version, "mock");
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_pcie_info(amdsmi_processor_handle h, amdsmi_pcie_info_t *info) {
    LOAD(h, info);
    memset(info, 0, sizeof *info);
    info->pcie_static.max_pcie_width = PCIE_WIDTH;
    info->pcie_static.max_pcie_speed = 32; // GT/s
    info->pcie_static.pcie_interface_version = PCIE_GEN;
    info->pcie_metric.pcie_width = PCIE_WIDTH;
    info->pcie_metric.pcie_speed = 32000; // MT/s
    return AMDSMI_STATUS_SUCCESS;
}

// --- Telemetry -----------------------------------------------------------

amdsmi_status_t amdsmi_get_gpu_metrics_info(amdsmi_processor_handle h, amdsmi_gpu_metrics_t *m) {
    LOAD(h, m);
    memset(m, 0, sizeof *m);
    m->common_header.structure_size = sizeof *m;
    m->common_header.format_revision = 1;
    m->common_header.content_revision = 4;
    // Hotspot and memory run 6 above and 4 below edge, as the sysfs tree says.
    m->temperature_edge = (uint16_t)gpu.temp;
    m->temperature_hotspot = (uint16_t)(gpu.temp + 6);
    m->temperature_mem = (uint16_t)(gpu.temp - 4);
    m->average_gfx_activity = (uint16_t)gpu.util;
    m->average_socket_power = (uint16_t)gpu.power;
    m->current_socket_power = (uint16_t)gpu.power;
    m->current_gfxclk = (uint16_t)gpu.gfx_mhz;
    for (unsigned i = 0; i < AMDSMI_MAX_NUM_GFX_CLKS; i++)
        m->current_gfxclks[i] = (uint16_t)gpu.gfx_mhz;
    m->current_uclk = (uint16_t)gpu.mem_mhz;
    m->pcie_link_width = PCIE_WIDTH;
    m->pcie_link_speed = 320; // 0.1 GT/s units
    m->throttle_status = (uint32_t)gpu.throttled;
    m->indep_throttle_status = (uint64_t)gpu.throttled;
    // One link to every peer, all up unless this GPU's links are down.
    for (int i = 0; i < AMDSMI_MAX_NUM_XGMI_LINKS && i < gpu_count - 1; i++)
        m->xgmi_link_status[i] = !xgmi_down();
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_gpu_activity(amdsmi_processor_handle h, amdsmi_engine_usage_t *usage) {
    LOAD(h, usage);
    memset(usage, 0, sizeof *usage);
    usage->gfx_activity = (uint32_t)gpu.util;
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_temp_metric(amdsmi_processor_handle h, amdsmi_temperature_type_t type,
                                       amdsmi_temperature_metric_t metric, int64_t *value) {
    (void)type;
    LOAD(h, value);
    if (metric != AMDSMI_TEMP_CURRENT)
        return AMDSMI_STATUS_NOT_SUPPORTED;
    *value = (int64_t)gpu.temp * 1000; // millidegrees
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_power_info(amdsmi_processor_handle h, amdsmi_power_info_t *info) {
    LOAD(h, info);
    memset(info, 0, sizeof *info);
    info->socket_power = (uint32_t)gpu.power;
    info->current_socket_power = (uint32_t)gpu.power;
    info->average_socket_power = (uint32_t)gpu.power;
    info->power_limit = (uint32_t)gpu.cap;
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_power_cap_info(amdsmi_processor_handle h, uint32_t sensor, amdsmi_power_cap_info_t *info) {
    (void)sensor;
    LOAD(h, info);
    memset(info, 0, sizeof *info);
    info->power_cap = (uint64_t)gpu.cap * 1000000; // microwatts
    info->default_power_cap = info->power_cap;
    info->max_power_cap = info->power_cap;
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_clock_info(amdsmi_processor_handle h, amdsmi_clk_type_t type, amdsmi_clk_info_t *info) {
    LOAD(h, info);
    if (type != AMDSMI_CLK_TYPE_GFX && type != AMDSMI_CLK_TYPE_MEM)
        return AMDSMI_STATUS_NOT_SUPPORTED;
    memset(info, 0, sizeof *info);
    int mem = type == AMDSMI_CLK_TYPE_MEM;
    info->clk = (uint32_t)(mem ? gpu.mem_mhz : gpu.gfx_mhz);
    info->min_clk = info->clk;
    info->max_clk = mem ? MAX_MEM_MHZ : MAX_GFX_MHZ;
    return AMDSMI_STATUS_SUCCESS;
}

// --- Memory --------------------------------------------------------------

amdsmi_status_t amdsmi_get_gpu_memory_total(amdsmi_processor_handle h, amdsmi_memory_type_t type, uint64_t *bytes) {
    LOAD(h, bytes);
    if (type != AMDSMI_MEM_TYPE_VRAM)
        return AMDSMI_STATUS_NOT_SUPPORTED;
    *bytes = (uint64_t)gpu.total_mib << 20;
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_gpu_memory_usage(amdsmi_processor_handle h, amdsmi_memory_type_t type, uint64_t *bytes) {
    LOAD(h, bytes);
    if (type != AMDSMI_MEM_TYPE_VRAM)
        return AMDSMI_STATUS_NOT_SUPPORTED;
    *bytes = (uint64_t)gpu.used_mib << 20;
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_gpu_vram_usage(amdsmi_processor_handle h, amdsmi_vram_usage_t *usage) {
    LOAD(h, usage);
    memset(usage, 0, sizeof *usage);
    usage->vram_total = (uint32_t)gpu.total_mib;
    usage->vram_used = (uint32_t)gpu.used_mib;
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_gpu_vram_info(amdsmi_processor_handle h, amdsmi_vram_info_t *info) {
    LOAD(h, info);
    memset(info, 0, sizeof *info);
    info->vram_type = AMDSMI_VRAM_TYPE_HBM;
    info->vram_size = (uint64_t)gpu.total_mib;
    snprintf(info->vram_vendor, sizeof info->vram_vendor, "AMD mock");
    return AMDSMI_STATUS_SUCCESS;
}

// --- Faults --------------------------------------------------------------

amdsmi_status_t amdsmi_get_gpu_total_ecc_count(amdsmi_processor_handle h, amdsmi_error_count_t *count) {
    LOAD(h, count);
    memset(count, 0, sizeof *count);
    count->uncorrectable_count = (uint64_t)gpu.ecc;
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_get_gpu_ecc_count(amdsmi_processor_handle h, amdsmi_gpu_block_t block,
                                         amdsmi_error_count_t *count) {
    LOAD(h, count);
    if (block != AMDSMI_GPU_BLOCK_UMC)
        return AMDSMI_STATUS_NOT_SUPPORTED;
    memset(count, 0, sizeof *count);
    count->uncorrectable_count = (uint64_t)gpu.ecc;
    return AMDSMI_STATUS_SUCCESS;
}

// Retired pages mirror ras/gpu_vram_bad_pages: one reserved 4 KiB page per
// record, at the addresses the sysfs list shows.
amdsmi_status_t amdsmi_get_gpu_bad_page_info(amdsmi_processor_handle h, uint32_t *count,
                                             amdsmi_retired_page_record_t *records) {
    LOAD(h, count);
    uint32_t n = (uint32_t)gpu.retired;
    if (records) {
        if (n > *count)
            n = *count;
        for (uint32_t i = 0; i < n; i++) {
            records[i].page_address = (uint64_t)(0x1000 + i) << 12;
            records[i].page_size = 4096;
            records[i].status = AMDSMI_MEM_PAGE_STATUS_RESERVED;
        }
    }
    *count = n;
    return AMDSMI_STATUS_SUCCESS;
}

amdsmi_status_t amdsmi_gpu_xgmi_error_status(amdsmi_processor_handle h, amdsmi_xgmi_status_t *status) {
    LOAD(h, status);
    *status = xgmi_down() ? AMDSMI_XGMI_STATUS_ERROR : AMDSMI_XGMI_STATUS_NO_ERRORS;
    return AMDSMI_STATUS_SUCCESS;
}

// Processor-hot and socket-thermal throttling are modelled. Every other field
// keeps the all-ones value the ABI defines as "not supported", except the
// power and voltage-regulator ones, which read as never throttling.
amdsmi_status_t amdsmi_get_violation_status(amdsmi_processor_handle h, amdsmi_violation_status_t *v) {
    LOAD(h, v);
    memset(v, 0xFF, sizeof *v);
    uint8_t on = (uint8_t)gpu.throttled;
    v->active_prochot_thrm = v->active_socket_thrm = on;
    v->per_prochot_thrm = v->per_socket_thrm = on ? 100 : 0;
    v->active_ppt_pwr = v->active_vr_thrm = v->active_hbm_thrm = 0;
    v->per_ppt_pwr = v->per_vr_thrm = v->per_hbm_thrm = 0;
    return AMDSMI_STATUS_SUCCESS;
}
