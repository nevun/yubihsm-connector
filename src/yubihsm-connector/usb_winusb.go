// Copyright 2016-2018 Yubico AB
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// +build windows

package main

import (
	"fmt"
	"sync"
	"unsafe"

	log "github.com/sirupsen/logrus"
)

// #cgo CFLAGS: -DUNICODE -D_UNICODE
// #cgo LDFLAGS: -lwinusb -lsetupapi -luuid
// #include "usb_windows.h"
import "C"

var device struct {
	ctx C.PDEVICE_CONTEXT
	mtx sync.Mutex
}

func (e C.DWORD) Error() string {
	return fmt.Sprintf("Windows Error: 0x%x", uint(e))
}

const (
	SUCCESS                 C.DWORD = C.ERROR_SUCCESS
	ERROR_INVALID_STATE     C.DWORD = C.ERROR_INVALID_STATE
	ERROR_INVALID_HANDLE    C.DWORD = C.ERROR_INVALID_HANDLE
	ERROR_INVALID_PARAMETER C.DWORD = C.ERROR_INVALID_PARAMETER
	ERROR_OUTOFMEMORY       C.DWORD = C.ERROR_OUTOFMEMORY
	ERROR_GEN_FAILURE       C.DWORD = C.ERROR_GEN_FAILURE
	ERROR_OBJECT_NOT_FOUND  C.DWORD = C.ERROR_OBJECT_NOT_FOUND
	ERROR_NOT_SUPPORTED     C.DWORD = C.ERROR_NOT_SUPPORTED
	ERROR_SHARING_VIOLATION C.DWORD = C.ERROR_SHARING_VIOLATION
	ERROR_BAD_COMMAND       C.DWORD = C.ERROR_BAD_COMMAND
)

func winusbError(err error) error {
	if err != SUCCESS {
		return err
	}
	return nil
}

func usbopen(cid string) (err error) {
	if device.ctx != nil {
		log.WithField("Correlation-ID", cid).Debug("usb context already open")
		return nil
	}

	if serial != "" {
		cSerial := C.CString(serial)
		defer C.free(unsafe.Pointer(cSerial))

		err = winusbError(C.usbOpen(0x1050, 0x0030, cSerial, &device.ctx))
	} else {
		err = winusbError(C.usbOpen(0x1050, 0x0030, nil, &device.ctx))
	}

	if device.ctx == nil {
		err = fmt.Errorf("device not found")
	}

	return err
}

func usbclose(cid string) {
	if device.ctx != nil {
		C.usbClose(&device.ctx)
	}
}

func usbreopen(cid string, why error) (err error) {
	log.WithFields(log.Fields{
		"Correlation-ID": cid,
		"why":            why,
	}).Debug("reopening usb context")

	// If the first request to the connector is a status request,
	// the device context might not have been created yet.
	if device.ctx != nil {
		if err = winusbError(C.usbReopen(device.ctx)); err != nil {
			log.WithField(
				"Correlation-ID", cid,
			).WithError(err).Error("unable to reset device")
		}
	}

	usbclose(cid)
	return usbopen(cid)
}

func usbReopen(cid string, why error) (err error) {
	device.mtx.Lock()
	defer device.mtx.Unlock()

	return usbreopen(cid, why)
}

func usbwrite(buf []byte, cid string) (err error) {
	var n C.ULONG

	if err = winusbError(C.usbWrite(
		device.ctx,
		(*C.UCHAR)(unsafe.Pointer(&buf[0])),
		C.ULONG(len(buf)),
		&n)); err != nil {
		goto out
	}

	if len(buf)%64 == 0 {
		var empty []byte

		if err = winusbError(C.usbWrite(
			device.ctx,
			(*C.UCHAR)(unsafe.Pointer(&buf[0])),
			C.ULONG(len(empty)),
			&n)); err != nil {
			goto out
		}
	}

out:
	log.WithFields(log.Fields{
		"Correlation-ID": cid,
		"n":              n,
		"err":            err,
		"len":            len(buf),
		"buf":            buf,
	}).Debug("usb endpoint write")

	return err
}

func usbread(cid string) (buf []byte, err error) {
	var n C.ULONG

	buf = make([]byte, 8192)

	if err = winusbError(C.usbRead(
		device.ctx,
		(*C.UCHAR)(unsafe.Pointer(&buf[0])),
		C.ULONG(len(buf)),
		&n)); err != nil {
		goto out
	}
	buf = buf[:n]

out:
	log.WithFields(log.Fields{
		"Correlation-ID": cid,
		"n":              n,
		"err":            err,
		"len":            len(buf),
		"buf":            buf,
	}).Debug("usb endpoint read")

	return buf, err
}

func usbProxy(req []byte, cid string) (resp []byte, err error) {
	device.mtx.Lock()
	defer device.mtx.Unlock()

	if err = usbopen(cid); err != nil {
		return nil, err
	}

	for {
		err = usbwrite(req, cid)
		switch err {
		case ERROR_INVALID_STATE, ERROR_INVALID_HANDLE, ERROR_BAD_COMMAND:
			if err = usbreopen(cid, err); err != nil {
				return nil, err
			}
			continue
		}

		resp, err = usbread(cid)
		switch err {
		case ERROR_INVALID_STATE, ERROR_INVALID_HANDLE, ERROR_BAD_COMMAND:
			if err = usbreopen(cid, err); err != nil {
				return nil, err
			}
			continue
		}

		break
	}

	return resp, err
}
