package aclaudit

import (
	"encoding/binary"
	"testing"
)

func descriptorFixture() []byte {
	// Self-relative descriptor: owner SYSTEM, one Everyone allow ACE.
	raw := make([]byte, 20+12+8+20)
	raw[0] = 1
	binary.LittleEndian.PutUint16(raw[2:], 0x8004)
	binary.LittleEndian.PutUint32(raw[4:], 20)
	binary.LittleEndian.PutUint32(raw[16:], 32)
	copy(raw[20:], []byte{1, 1, 0, 0, 0, 0, 0, 5, 18, 0, 0, 0})
	raw[32] = 2
	binary.LittleEndian.PutUint16(raw[34:], 28)
	binary.LittleEndian.PutUint16(raw[36:], 1)
	binary.LittleEndian.PutUint16(raw[42:], 20)
	binary.LittleEndian.PutUint32(raw[44:], 0x40)
	copy(raw[48:], []byte{1, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0})
	return raw
}

func TestDecodeDescriptorPreservesNumericEvidence(t *testing.T) {
	d, err := decodeDescriptor(descriptorFixture())
	if err != nil {
		t.Fatal(err)
	}
	if d.Owner != "S-1-5-18" || len(d.ACEs) != 1 || d.ACEs[0].SID != "S-1-1-0" || d.ACEs[0].Mask != 0x40 || d.Control != 0x8004 {
		t.Fatalf("descriptor=%+v", d)
	}
	for _, mutate := range []func([]byte){func(b []byte) { b[0] = 0 }, func(b []byte) { b[3] = 0 }, func(b []byte) { binary.LittleEndian.PutUint32(b[4:], 0xffffffff) }, func(b []byte) { b[40+2] = 0 }, func(b []byte) { b[49] = 255 }, func(b []byte) { b[34] = 255 }} {
		raw := descriptorFixture()
		mutate(raw)
		if _, err := decodeDescriptor(raw); err == nil {
			t.Fatal("malformed descriptor accepted")
		}
	}
}

func FuzzDecodeDescriptor(f *testing.F) {
	f.Add(descriptorFixture())
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		d, err := decodeDescriptor(raw)
		if err == nil && len(d.ACEs) > maxACEs {
			t.Fatal("ACE limit bypass")
		}
	})
}
