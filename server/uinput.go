package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// 通过 /dev/uinput 创建一个只有一个键的虚拟键盘，用来让输入法刷新候选。
// 需要当前用户能写 /dev/uinput（见 70-rime-translate-uinput.rules）。

const (
	uiDevCreate = 0x5501     // _IO('U', 1)
	uiSetEvBit  = 0x40045564 // _IOW('U', 100, int)
	uiSetKeyBit = 0x40045565 // _IOW('U', 101, int)

	evSyn      = 0x00
	evKey      = 0x01
	busVirtual = 0x06

	uinputUserDevSize = 80 + 8 + 4 + 4*64*4 // name + input_id + ff_effects_max + abs*
	inputEventSize    = 16 + 2 + 2 + 4      // timeval + type + code + value（64 位）
)

type VirtualKey struct {
	f    *os.File
	code uint16
}

func ioctl(f *os.File, req, arg uintptr) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, arg); e != 0 {
		return e
	}
	return nil
}

func NewVirtualKey(code uint16) (*VirtualKey, error) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		return nil, fmt.Errorf("only 64-bit supported")
	}
	f, err := os.OpenFile("/dev/uinput", os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*VirtualKey, error) { f.Close(); return nil, err }
	if err := ioctl(f, uiSetEvBit, evKey); err != nil {
		return fail(fmt.Errorf("UI_SET_EVBIT: %w", err))
	}
	if err := ioctl(f, uiSetKeyBit, uintptr(code)); err != nil {
		return fail(fmt.Errorf("UI_SET_KEYBIT: %w", err))
	}
	dev := make([]byte, uinputUserDevSize)
	copy(dev[:79], "rime-translate refresh key")
	binary.LittleEndian.PutUint16(dev[80:], busVirtual)
	binary.LittleEndian.PutUint16(dev[82:], 0x1209) // vendor
	binary.LittleEndian.PutUint16(dev[84:], 0x7274) // product
	binary.LittleEndian.PutUint16(dev[86:], 1)      // version
	if _, err := f.Write(dev); err != nil {
		return fail(fmt.Errorf("write uinput_user_dev: %w", err))
	}
	if err := ioctl(f, uiDevCreate, 0); err != nil {
		return fail(fmt.Errorf("UI_DEV_CREATE: %w", err))
	}
	return &VirtualKey{f: f, code: code}, nil
}

// Tap 按下并松开一次。
func (k *VirtualKey) Tap() error {
	buf := make([]byte, 0, 4*inputEventSize)
	ev := func(typ, code uint16, val int32) {
		var e [inputEventSize]byte
		binary.LittleEndian.PutUint16(e[16:], typ)
		binary.LittleEndian.PutUint16(e[18:], code)
		binary.LittleEndian.PutUint32(e[20:], uint32(val))
		buf = append(buf, e[:]...)
	}
	ev(evKey, k.code, 1)
	ev(evSyn, 0, 0)
	ev(evKey, k.code, 0)
	ev(evSyn, 0, 0)
	_, err := k.f.Write(buf)
	return err
}
