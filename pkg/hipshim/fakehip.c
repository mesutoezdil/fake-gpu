// fakehip: HIP device and memory calls on top of the fake-gpu state directory.
//
// Device memory is anonymous host memory mapped without reservation, so a
// 100 GB hipMalloc succeeds on a laptop and costs RAM only where it is
// touched. Each process publishes what it holds per GPU in
// <state>/hip/<host>-<pid>; the agent adds that to the VRAM it reports, which
// makes allocations visible in sysfs, amd-smi and the exporter. The file stays
// locked while the process lives, so the agent can tell a crash from a client.
#define _GNU_SOURCE
#include "fakehip.h"

#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/file.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>

#define MAX_GPUS 64

typedef struct block {
  void *ptr;
  size_t size;
  int gpu;
  struct block *next;
} block_t;

static pthread_mutex_t lock = PTHREAD_MUTEX_INITIALIZER;
static int ready;                      // 1 once the state directory was read
static int gpu_count;                  // devices visible to this process
static int physical[MAX_GPUS];         // visible ordinal -> fake-gpu index
static size_t total_bytes[MAX_GPUS];   // by visible ordinal
static size_t used_bytes[MAX_GPUS];    // held by this process
static char names[MAX_GPUS][128];
static block_t *blocks;                // ponytail: linked list, O(n) free; hash it if a workload holds thousands of live blocks
static int usage_fd = -1;
static _Thread_local int current;      // device selected with hipSetDevice
static _Thread_local hipError_t last = hipSuccess;

static const char *state_dir(void) {
  const char *dir = getenv("FAKE_GPU_STATE_DIR");
  return dir ? dir : "/var/lib/fake-gpu";
}

static hipError_t fail(hipError_t e) {
  if (e != hipSuccess)
    last = e;
  return e;
}

// publish rewrites this process's usage file. Called with the lock held.
static void publish(void) {
  if (usage_fd < 0)
    return;
  char text[MAX_GPUS * 40];
  int n = 0;
  for (int i = 0; i < gpu_count; i++)
    if (used_bytes[i])
      n += snprintf(text + n, sizeof(text) - n, "%d %zu\n", physical[i], used_bytes[i]);
  if (ftruncate(usage_fd, 0) == 0 && n > 0 && pwrite(usage_fd, text, n, 0) != n)
    fprintf(stderr, "fakehip: cannot publish usage: %s\n", strerror(errno));
}

static void retire(void) {
  char path[512], host[64] = "host";
  gethostname(host, sizeof(host) - 1);
  snprintf(path, sizeof(path), "%s/hip/%s-%d", state_dir(), host, (int)getpid());
  unlink(path);
}

// load reads the device list once. Called with the lock held.
static hipError_t load(void) {
  if (ready)
    return gpu_count ? hipSuccess : hipErrorNoDevice;
  ready = 1;
  char path[512];
  snprintf(path, sizeof(path), "%s/smi/count", state_dir());
  FILE *f = fopen(path, "r");
  int all = 0;
  if (!f || fscanf(f, "%d", &all) != 1 || all < 1 || all > MAX_GPUS) {
    if (f)
      fclose(f);
    return hipErrorNoDevice;
  }
  fclose(f);

  // HIP_VISIBLE_DEVICES narrows and reorders the devices, as in real HIP.
  int chosen[MAX_GPUS], n = 0;
  const char *visible = getenv("HIP_VISIBLE_DEVICES");
  if (visible && *visible) {
    char *copy = strdup(visible), *save = NULL;
    for (char *tok = strtok_r(copy, ",", &save); tok && n < MAX_GPUS; tok = strtok_r(NULL, ",", &save)) {
      int idx = atoi(tok);
      if (idx >= 0 && idx < all)
        chosen[n++] = idx;
    }
    free(copy);
  } else {
    for (; n < all; n++)
      chosen[n] = n;
  }

  for (int i = 0; i < n; i++) {
    snprintf(path, sizeof(path), "%s/smi/gpu%d", state_dir(), chosen[i]);
    if (!(f = fopen(path, "r")))
      continue;
    int v[9];
    char name[128] = "";
    if (fscanf(f, "%d %d %d %d %d %d %d %d %d\n%127[^\n]", &v[0], &v[1], &v[2], &v[3], &v[4], &v[5], &v[6],
               &v[7], &v[8], name) == 10) {
      physical[gpu_count] = chosen[i];
      total_bytes[gpu_count] = (size_t)v[5] << 20; // sixth value is total VRAM in MiB
      snprintf(names[gpu_count], sizeof(names[0]), "%s", name);
      gpu_count++;
    }
    fclose(f);
  }
  if (!gpu_count)
    return hipErrorNoDevice;

  char host[64] = "host";
  gethostname(host, sizeof(host) - 1);
  snprintf(path, sizeof(path), "%s/hip", state_dir());
  mkdir(path, 0777);
  snprintf(path, sizeof(path), "%s/hip/%s-%d", state_dir(), host, (int)getpid());
  usage_fd = open(path, O_RDWR | O_CREAT | O_CLOEXEC, 0644);
  if (usage_fd >= 0) {
    flock(usage_fd, LOCK_EX | LOCK_NB);
    atexit(retire);
  } // without a writable state directory the calls still work, unreported
  return hipSuccess;
}

#define ENTER()                                                                                              \
  pthread_mutex_lock(&lock);                                                                                 \
  hipError_t init_status = load();                                                                           \
  if (init_status != hipSuccess) {                                                                           \
    pthread_mutex_unlock(&lock);                                                                             \
    return fail(init_status);                                                                                \
  }
#define LEAVE(e)                                                                                             \
  do {                                                                                                       \
    pthread_mutex_unlock(&lock);                                                                             \
    return fail(e);                                                                                          \
  } while (0)

hipError_t hipInit(unsigned int flags) {
  (void)flags;
  ENTER();
  LEAVE(hipSuccess);
}

hipError_t hipGetDeviceCount(int *count) {
  if (!count)
    return fail(hipErrorInvalidValue);
  pthread_mutex_lock(&lock);
  load();
  *count = gpu_count; // zero devices is an answer, not an error
  pthread_mutex_unlock(&lock);
  return hipSuccess;
}

hipError_t hipSetDevice(int id) {
  ENTER();
  if (id < 0 || id >= gpu_count)
    LEAVE(hipErrorInvalidDevice);
  current = id;
  LEAVE(hipSuccess);
}

hipError_t hipGetDevice(int *id) {
  if (!id)
    return fail(hipErrorInvalidValue);
  ENTER();
  *id = current;
  LEAVE(hipSuccess);
}

hipError_t hipDeviceGet(hipDevice_t *device, int ordinal) {
  if (!device)
    return fail(hipErrorInvalidValue);
  ENTER();
  if (ordinal < 0 || ordinal >= gpu_count)
    LEAVE(hipErrorInvalidDevice);
  *device = ordinal;
  LEAVE(hipSuccess);
}

hipError_t hipDeviceGetName(char *name, int len, hipDevice_t device) {
  if (!name || len <= 0)
    return fail(hipErrorInvalidValue);
  ENTER();
  if (device < 0 || device >= gpu_count)
    LEAVE(hipErrorInvalidDevice);
  snprintf(name, (size_t)len, "%s", names[device]);
  LEAVE(hipSuccess);
}

hipError_t hipDeviceTotalMem(size_t *bytes, hipDevice_t device) {
  if (!bytes)
    return fail(hipErrorInvalidValue);
  ENTER();
  if (device < 0 || device >= gpu_count)
    LEAVE(hipErrorInvalidDevice);
  *bytes = total_bytes[device];
  LEAVE(hipSuccess);
}

hipError_t hipDeviceSynchronize(void) {
  ENTER();
  LEAVE(hipSuccess);
}

hipError_t hipMemGetInfo(size_t *free_bytes, size_t *total) {
  ENTER();
  if (total)
    *total = total_bytes[current];
  if (free_bytes)
    *free_bytes = total_bytes[current] - used_bytes[current];
  LEAVE(hipSuccess);
}

hipError_t hipMalloc(void **ptr, size_t size) {
  if (!ptr)
    return fail(hipErrorInvalidValue);
  *ptr = NULL;
  ENTER();
  if (size == 0)
    LEAVE(hipSuccess); // HIP hands back a null pointer for an empty request
  if (size > total_bytes[current] - used_bytes[current])
    LEAVE(hipErrorOutOfMemory);
  block_t *b = malloc(sizeof(*b));
  void *mem = mmap(NULL, size, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS | MAP_NORESERVE, -1, 0);
  if (!b || mem == MAP_FAILED) {
    free(b);
    if (mem != MAP_FAILED)
      munmap(mem, size);
    LEAVE(hipErrorOutOfMemory);
  }
  *b = (block_t){.ptr = mem, .size = size, .gpu = current, .next = blocks};
  blocks = b;
  used_bytes[current] += size;
  publish();
  *ptr = mem;
  LEAVE(hipSuccess);
}

hipError_t hipFree(void *ptr) {
  if (!ptr)
    return hipSuccess;
  ENTER();
  for (block_t **at = &blocks; *at; at = &(*at)->next) {
    block_t *b = *at;
    if (b->ptr != ptr)
      continue;
    *at = b->next;
    used_bytes[b->gpu] -= b->size;
    munmap(b->ptr, b->size);
    free(b);
    publish();
    LEAVE(hipSuccess);
  }
  LEAVE(hipErrorInvalidValue);
}

hipError_t hipHostMalloc(void **ptr, size_t size, unsigned int flags) {
  (void)flags;
  if (!ptr)
    return fail(hipErrorInvalidValue);
  *ptr = size ? malloc(size) : NULL;
  return fail(size && !*ptr ? hipErrorOutOfMemory : hipSuccess);
}

hipError_t hipHostFree(void *ptr) {
  free(ptr);
  return hipSuccess;
}

hipError_t hipMemcpy(void *dst, const void *src, size_t n, hipMemcpyKind kind) {
  if (n && (!dst || !src))
    return fail(hipErrorInvalidValue);
  if (kind < hipMemcpyHostToHost || kind > hipMemcpyDefault)
    return fail(hipErrorInvalidValue);
  memmove(dst, src, n); // device memory is host memory here, so every direction is one copy
  return hipSuccess;
}

hipError_t hipMemset(void *dst, int value, size_t n) {
  if (n && !dst)
    return fail(hipErrorInvalidValue);
  memset(dst, value, n);
  return hipSuccess;
}

hipError_t hipGetLastError(void) {
  hipError_t e = last;
  last = hipSuccess;
  return e;
}

hipError_t hipPeekAtLastError(void) { return last; }

const char *hipGetErrorName(hipError_t e) {
  switch (e) {
  case hipSuccess: return "hipSuccess";
  case hipErrorInvalidValue: return "hipErrorInvalidValue";
  case hipErrorOutOfMemory: return "hipErrorOutOfMemory";
  case hipErrorNotInitialized: return "hipErrorNotInitialized";
  case hipErrorNoDevice: return "hipErrorNoDevice";
  case hipErrorInvalidDevice: return "hipErrorInvalidDevice";
  case hipErrorNotSupported: return "hipErrorNotSupported";
  default: return "hipErrorUnknown";
  }
}

const char *hipGetErrorString(hipError_t e) { return hipGetErrorName(e); }
