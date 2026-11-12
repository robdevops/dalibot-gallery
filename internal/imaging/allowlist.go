package imaging

/*
#cgo pkg-config: vips
#include <stdlib.h>
#include <vips/vips.h>

// Block every loader, then allow the JPEG ones again. The order matters:
// vips_operation_block_set applies to the named class and all its subclasses.
static void gallery_allow_only_jpeg(void) {
	vips_operation_block_set("VipsForeignLoad", TRUE);
	vips_operation_block_set("VipsForeignLoadJpeg", FALSE);
}

static void gallery_block_set(const char *name, int state) {
	vips_operation_block_set(name, state ? TRUE : FALSE);
}

// gallery_probe asks libvips to open a buffer with whatever loader it can
// find, reading the header only. Returns 0 if a loader accepted it.
static int gallery_probe(const void *buf, size_t len) {
	VipsImage *im = vips_image_new_from_buffer(buf, len, "", NULL);
	if (im == NULL) {
		vips_error_clear();
		return -1;
	}
	g_object_unref(im);
	return 0;
}
*/
import "C"

import (
	"runtime"
	"unsafe"
)

// allowOnlyJPEG installs the loader allowlist: after it, libvips refuses to
// open anything but JPEG, whatever the bytes claim to be.
func allowOnlyJPEG() {
	C.gallery_allow_only_jpeg()
}

// blockSet blocks or unblocks a libvips operation class by name. Only tests
// use it directly (to show that a loader works until the allowlist blocks it).
func blockSet(class string, blocked bool) {
	name := C.CString(class)
	defer C.free(unsafe.Pointer(name))
	state := C.int(0)
	if blocked {
		state = 1
	}
	C.gallery_block_set(name, state)
}

// probe reports whether libvips itself finds a loader that accepts data. It
// does not run Validate; it exists so tests can check the allowlist directly.
func probe(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	defer runtime.KeepAlive(data)
	return C.gallery_probe(unsafe.Pointer(&data[0]), C.size_t(len(data))) == 0
}
