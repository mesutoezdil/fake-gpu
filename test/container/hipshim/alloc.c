// Contract test for libfakehip against a fixture state directory:
// eight 192 GiB devices, of which HIP_VISIBLE_DEVICES=5,2 exposes two.
#include "fakehip.h"

#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#define GIB ((size_t)1 << 30)

static void usage_file_holds(const char *want) {
  char path[512], host[64], got[256] = "";
  gethostname(host, sizeof(host));
  snprintf(path, sizeof(path), "%s/hip/%s-%d", getenv("FAKE_GPU_STATE_DIR"), host, (int)getpid());
  FILE *f = fopen(path, "r");
  assert(f);
  size_t n = fread(got, 1, sizeof(got) - 1, f);
  got[n] = 0;
  fclose(f);
  if (strcmp(got, want)) {
    fprintf(stderr, "usage file holds \"%s\", want \"%s\"\n", got, want);
    abort();
  }
}

int main(void) {
  int count = -1, dev = -1;
  assert(hipGetDeviceCount(&count) == hipSuccess && count == 2);
  assert(hipSetDevice(2) == hipErrorInvalidDevice && hipGetLastError() == hipErrorInvalidDevice);
  assert(hipGetLastError() == hipSuccess);
  assert(hipSetDevice(1) == hipSuccess && hipGetDevice(&dev) == hipSuccess && dev == 1);

  char name[64];
  size_t total = 0, free_bytes = 0;
  assert(hipDeviceGetName(name, sizeof(name), 0) == hipSuccess && !strcmp(name, "AMD Instinct MI300X"));
  assert(hipDeviceTotalMem(&total, 1) == hipSuccess && total == 192 * GIB);
  assert(hipDeviceTotalMem(&total, 7) == hipErrorInvalidDevice);

  // 100 GiB on a laptop: mapped, never touched, so it costs no RAM.
  void *big = NULL, *small = NULL, *none = (void *)1;
  assert(hipMalloc(&big, 100 * GIB) == hipSuccess && big);
  assert(hipMemGetInfo(&free_bytes, &total) == hipSuccess && free_bytes == 92 * GIB);
  assert(hipMalloc(&none, 93 * GIB) == hipErrorOutOfMemory && none == NULL);
  assert(hipMalloc(&none, 0) == hipSuccess && none == NULL);
  usage_file_holds("2 107374182400\n"); // visible device 1 is fake-gpu card 2

  // The other device has its own budget, and data survives a round trip.
  assert(hipSetDevice(0) == hipSuccess && hipMalloc(&small, 4096) == hipSuccess);
  char in[32] = "device memory is host memory", out[32] = "";
  assert(hipMemcpy(small, in, sizeof(in), hipMemcpyHostToDevice) == hipSuccess);
  assert(hipMemset((char *)small + 2048, 0x5a, 16) == hipSuccess && ((char *)small)[2050] == 0x5a);
  assert(hipMemcpy(out, small, sizeof(out), hipMemcpyDeviceToHost) == hipSuccess && !strcmp(in, out));
  assert(hipMemcpy(out, small, 4, (hipMemcpyKind)77) == hipErrorInvalidValue);
  usage_file_holds("5 4096\n2 107374182400\n");

  assert(hipFree(big) == hipSuccess && hipFree(big) == hipErrorInvalidValue && hipFree(NULL) == hipSuccess);
  assert(hipSetDevice(1) == hipSuccess && hipMemGetInfo(&free_bytes, NULL) == hipSuccess && free_bytes == 192 * GIB);
  assert(hipFree(small) == hipSuccess);
  usage_file_holds("");
  assert(!strcmp(hipGetErrorName(hipErrorOutOfMemory), "hipErrorOutOfMemory"));
  puts("fakehip: devices, visibility, limits, accounting, copies and errors PASS");
  return 0;
}
