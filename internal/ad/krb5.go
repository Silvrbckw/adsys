package ad

/*
#include <errno.h>
#include <string.h>

#include <krb5.h>

char *get_ticket_path() {
  krb5_error_code ret;
  krb5_context context;

  ret = krb5_init_context(&context);
  if (ret) {
    errno = ret;
    return NULL;
  }

  const char* cc_name = krb5_cc_default_name(context);
  if (cc_name == NULL) {
    return NULL;
  }

  return strdup(cc_name);
}
*/
// #cgo pkg-config: krb5
import "C"

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"github.com/leonelquinteros/gotext"
)

// TicketPath returns the path of the default kerberos ticket cache for the
// current user.
// It returns an error if the path is empty or does not exist on the disk.
func TicketPath() (string, error) {
	cKrb5cc, err := C.get_ticket_path()
	defer C.free(unsafe.Pointer(cKrb5cc))
	if err != nil {
		return "", fmt.Errorf(gotext.Get("error initializing krb5 context, krb5_error_code: %d", err))
	}
	krb5cc := C.GoString(cKrb5cc)
	if krb5cc == "" {
		return "", errors.New(gotext.Get("path is empty"))
	}

	krb5ccPath := strings.TrimPrefix(krb5cc, "FILE:")
	fileInfo, err := os.Stat(krb5ccPath)
	if err != nil {
		return "", fmt.Errorf(gotext.Get("%q does not exist or is not accessible: %v", krb5ccPath, err))
	}
	if !fileInfo.Mode().IsRegular() {
		return "", fmt.Errorf(gotext.Get("%q is not a regular file", krb5ccPath))
	}

	return krb5ccPath, nil
}
