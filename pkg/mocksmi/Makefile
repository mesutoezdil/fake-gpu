# libamd_smi.so: AMD SMI ABI 27 served from the fake-gpu state directory.
# One library for every consumer: AMD's GPU Agent, the exporter, and anything
# else that loads libamd_smi.
CC ?= gcc
CFLAGS ?= -O2 -fPIC

libamd_smi.so: mock.c unsupported.c amdsmi.h version.map
	$(CC) $(CFLAGS) -shared -Wl,-soname,libamd_smi.so.27 -Wl,--version-script=version.map \
		-o $@ mock.c unsupported.c

clean:
	rm -f libamd_smi.so

.PHONY: clean
