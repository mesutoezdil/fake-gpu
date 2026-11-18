/*
 * fakehip: the memory and device part of the HIP runtime API, backed by host
 * memory and the fake-gpu device state. Declarations match hip_runtime_api.h,
 * so code written against HIP compiles unchanged when it includes this header
 * instead. Kernels are not supported: nothing here launches or compiles one.
 */
#ifndef FAKEHIP_H
#define FAKEHIP_H

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef enum hipError_t {
  hipSuccess = 0,
  hipErrorInvalidValue = 1,
  hipErrorOutOfMemory = 2,
  hipErrorNotInitialized = 3,
  hipErrorNoDevice = 100,
  hipErrorInvalidDevice = 101,
  hipErrorNotSupported = 801,
  hipErrorUnknown = 999
} hipError_t;

typedef enum hipMemcpyKind {
  hipMemcpyHostToHost = 0,
  hipMemcpyHostToDevice = 1,
  hipMemcpyDeviceToHost = 2,
  hipMemcpyDeviceToDevice = 3,
  hipMemcpyDefault = 4
} hipMemcpyKind;

typedef int hipDevice_t;

hipError_t hipInit(unsigned int flags);
hipError_t hipGetDeviceCount(int *count);
hipError_t hipSetDevice(int deviceId);
hipError_t hipGetDevice(int *deviceId);
hipError_t hipDeviceGet(hipDevice_t *device, int ordinal);
hipError_t hipDeviceGetName(char *name, int len, hipDevice_t device);
hipError_t hipDeviceTotalMem(size_t *bytes, hipDevice_t device);
hipError_t hipDeviceSynchronize(void);

hipError_t hipMalloc(void **ptr, size_t size);
hipError_t hipFree(void *ptr);
hipError_t hipHostMalloc(void **ptr, size_t size, unsigned int flags);
hipError_t hipHostFree(void *ptr);
hipError_t hipMemcpy(void *dst, const void *src, size_t sizeBytes, hipMemcpyKind kind);
hipError_t hipMemset(void *dst, int value, size_t sizeBytes);
hipError_t hipMemGetInfo(size_t *free, size_t *total);

hipError_t hipGetLastError(void);
hipError_t hipPeekAtLastError(void);
const char *hipGetErrorName(hipError_t error);
const char *hipGetErrorString(hipError_t error);

#ifdef __cplusplus
}
#endif
#endif
