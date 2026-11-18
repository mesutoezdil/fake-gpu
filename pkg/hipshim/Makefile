# libfakehip.so: HIP device and memory calls backed by the fake-gpu state.
CC ?= gcc
CFLAGS ?= -O2 -Wall -Wextra -Werror -fPIC

libfakehip.so: fakehip.c fakehip.h
	$(CC) $(CFLAGS) -shared -Wl,-soname,libfakehip.so -o $@ fakehip.c -lpthread

clean:
	rm -f libfakehip.so

.PHONY: clean
