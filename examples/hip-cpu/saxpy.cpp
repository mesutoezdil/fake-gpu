// A HIP kernel that really runs: y = a*x + y, repeated, then checked.
// Built with the HIP CPU Runtime it executes on the processor; the same
// source builds for a real AMD GPU with hipcc.
#include <hip/hip_runtime.h>

#include <cstdio>
#include <cstdlib>
#include <vector>

__global__ void saxpy(float a, const float* x, float* y, size_t n) {
    const size_t i = blockIdx.x * blockDim.x + threadIdx.x;
    if (i < n) y[i] = a * x[i] + y[i];
}

int main(int argc, char** argv) {
    const size_t n = argc > 1 ? std::strtoull(argv[1], nullptr, 10) : (size_t{1} << 22);
    const int rounds = argc > 2 ? std::atoi(argv[2]) : 50;
    const size_t bytes = n * sizeof(float);

    std::vector<float> x(n, 1.0f), y(n, 2.0f);
    float *dx = nullptr, *dy = nullptr;
    if (hipMalloc(&dx, bytes) != hipSuccess || hipMalloc(&dy, bytes) != hipSuccess) {
        std::fprintf(stderr, "saxpy: device allocation failed\n");
        return 1;
    }
    hipMemcpy(dx, x.data(), bytes, hipMemcpyHostToDevice);
    hipMemcpy(dy, y.data(), bytes, hipMemcpyHostToDevice);

    const unsigned block = 256;
    for (int r = 0; r < rounds; ++r) {
        hipLaunchKernelGGL(saxpy, dim3((n + block - 1) / block), dim3(block), 0, 0, 2.0f, dx, dy, n);
        // On a GPU, launches on one stream run in order. The HIP CPU Runtime
        // lets consecutive launches overlap, and each round reads what the
        // previous one wrote, so wait for every round before starting the next.
        hipDeviceSynchronize();
    }
    hipMemcpy(y.data(), dy, bytes, hipMemcpyDeviceToHost);
    hipFree(dx);
    hipFree(dy);

    const float want = 2.0f + 2.0f * rounds;
    for (size_t i = 0; i < n; ++i) {
        if (y[i] != want) {
            std::fprintf(stderr, "saxpy: y[%zu] = %f, want %f\n", i, y[i], want);
            return 1;
        }
    }
    std::printf("saxpy ok: %zu elements, %d rounds, y = %.0f\n", n, rounds, want);
    return 0;
}
