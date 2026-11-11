//go:build darwin

package power

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <IOKit/pwr_mgt/IOPMLib.h>

static IOReturn wmdCreateAssertion(const char *name, IOPMAssertionID *id) {
	CFStringRef n = CFStringCreateWithCString(kCFAllocatorDefault, name, kCFStringEncodingUTF8);
	IOReturn r = IOPMAssertionCreateWithName(kIOPMAssertionTypePreventUserIdleSystemSleep,
		kIOPMAssertionLevelOn, n, id);
	CFRelease(n);
	return r;
}
*/
import "C"

import (
	"fmt"
	"sync"
	"unsafe"
)

const supported = true

type iokitAssertion struct {
	mu       sync.Mutex
	id       C.IOPMAssertionID
	released bool
}

// PreventIdleSleep creates a PreventUserIdleSystemSleep assertion with the
// given name; it appears in `pmset -g assertions` under this process.
func PreventIdleSleep(name string) (Assertion, error) {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	var id C.IOPMAssertionID
	if r := C.wmdCreateAssertion(cname, &id); r != C.kIOReturnSuccess {
		return nil, fmt.Errorf("IOPMAssertionCreateWithName failed: 0x%x", uint32(r))
	}
	return &iokitAssertion{id: id}, nil
}

func (a *iokitAssertion) Release() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.released {
		return nil
	}
	a.released = true
	if r := C.IOPMAssertionRelease(a.id); r != C.kIOReturnSuccess {
		return fmt.Errorf("IOPMAssertionRelease failed: 0x%x", uint32(r))
	}
	return nil
}
