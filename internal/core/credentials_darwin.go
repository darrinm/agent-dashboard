//go:build darwin && cgo

package core

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>
#include <string.h>

static CFMutableDictionaryRef agentQuery(const char *account) {
    CFMutableDictionaryRef q = CFDictionaryCreateMutable(NULL, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    CFStringRef a = CFStringCreateWithCString(NULL, account, kCFStringEncodingUTF8);
    CFDictionarySetValue(q, kSecClass, kSecClassGenericPassword);
    CFDictionarySetValue(q, kSecAttrService, CFSTR("com.darrinm.agents.remote"));
    CFDictionarySetValue(q, kSecAttrAccount, a);
    CFRelease(a);
    return q;
}
static int agentSave(const char *account, const char *secret) {
    CFMutableDictionaryRef q = agentQuery(account);
    CFDataRef data = CFDataCreate(NULL, (const UInt8*)secret, strlen(secret));
    CFMutableDictionaryRef attributes = CFDictionaryCreateMutable(NULL, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    CFDictionarySetValue(attributes, kSecValueData, data);
    OSStatus status = SecItemUpdate(q, attributes);
    if (status == errSecItemNotFound) {
        CFDictionarySetValue(q, kSecValueData, data);
        status = SecItemAdd(q, NULL);
    }
    CFRelease(attributes); CFRelease(data); CFRelease(q);
    return status;
}
static int agentLoad(const char *account, char **secret) {
    CFMutableDictionaryRef q = agentQuery(account);
    CFDictionarySetValue(q, kSecReturnData, kCFBooleanTrue);
    CFDictionarySetValue(q, kSecMatchLimit, kSecMatchLimitOne);
    CFDictionarySetValue(q, kSecUseAuthenticationUI, kSecUseAuthenticationUIFail);
    CFTypeRef result = NULL;
    OSStatus status = SecItemCopyMatching(q, &result);
    if (status == errSecSuccess) {
        CFIndex length = CFDataGetLength((CFDataRef)result);
        *secret = malloc(length + 1);
        if (*secret) { memcpy(*secret, CFDataGetBytePtr((CFDataRef)result), length); (*secret)[length] = 0; }
        else { status = errSecAllocate; }
        CFRelease(result);
    }
    CFRelease(q);
    return status;
}
*/
import "C"

import (
	"fmt"
	"os"
	"unsafe"
)

func saveCredential(dir, token string) (string, error) {
	if os.Getenv("AGENTS_CREDENTIAL_STORE") == "file" {
		return "", nil
	}
	account := Hash(dir)
	a, s := C.CString(account), C.CString(token)
	defer C.free(unsafe.Pointer(a))
	defer C.free(unsafe.Pointer(s))
	if status := C.agentSave(a, s); status != 0 {
		return "", fmt.Errorf("Keychain could not store the device credential (status %d)", status)
	}
	return account, nil
}
func loadCredential(account string) (string, error) {
	a := C.CString(account)
	defer C.free(unsafe.Pointer(a))
	var secret *C.char
	if status := C.agentLoad(a, &secret); status != 0 {
		return "", fmt.Errorf("Keychain device credential unavailable (status %d)", status)
	}
	defer C.free(unsafe.Pointer(secret))
	return C.GoString(secret), nil
}
