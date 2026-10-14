package aclaudit

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func sidAt(data []byte, offset int) (string, error) {
	if offset < 0 || offset > len(data)-8 || data[offset] != 1 {
		return "", errors.New("invalid SID header")
	}
	n := int(data[offset+1])
	if n > 15 || n*4 > len(data)-offset-8 {
		return "", errors.New("invalid SID length")
	}
	authority := uint64(0)
	for _, b := range data[offset+2 : offset+8] {
		authority = authority<<8 | uint64(b)
	}
	parts := []string{"S", "1", strconv.FormatUint(authority, 10)}
	for i := 0; i < n; i++ {
		parts = append(parts, strconv.FormatUint(uint64(binary.LittleEndian.Uint32(data[offset+8+i*4:])), 10))
	}
	return strings.Join(parts, "-"), nil
}

func decodeDescriptor(raw []byte) (descriptor, error) {
	var d descriptor
	if len(raw) < 20 || len(raw) > maxDescriptor || raw[0] != 1 {
		return d, errors.New("invalid security descriptor size/revision")
	}
	d.Hash = sha256.Sum256(raw)
	d.Control = binary.LittleEndian.Uint16(raw[2:])
	if d.Control&0x8000 == 0 {
		return d, errors.New("descriptor is not self-relative")
	}
	owner := binary.LittleEndian.Uint32(raw[4:])
	if owner < 20 || owner%4 != 0 || uint64(owner) >= uint64(len(raw)) {
		return d, errors.New("missing/invalid owner")
	}
	var err error
	d.Owner, err = sidAt(raw, int(owner))
	if err != nil {
		return d, err
	}
	offset := binary.LittleEndian.Uint32(raw[16:])
	if d.Control&4 == 0 || offset == 0 {
		d.NullDACL = true
		return d, nil
	}
	if offset < 20 || offset%4 != 0 || uint64(offset) > uint64(len(raw)-8) {
		return d, errors.New("invalid DACL offset")
	}
	acl := raw[int(offset):]
	if acl[0] != 2 && acl[0] != 4 {
		return d, errors.New("unsupported ACL revision")
	}
	size := int(binary.LittleEndian.Uint16(acl[2:]))
	count := int(binary.LittleEndian.Uint16(acl[4:]))
	if size < 8 || size > len(acl) || count > maxACEs {
		return d, errors.New("invalid/oversized DACL")
	}
	acl = acl[:size]
	pos := 8
	for i := 0; i < count; i++ {
		if pos > len(acl)-4 {
			return d, errors.New("truncated ACE header")
		}
		entrySize := int(binary.LittleEndian.Uint16(acl[pos+2:]))
		if entrySize < 4 || entrySize%4 != 0 || entrySize > len(acl)-pos {
			return d, errors.New("invalid ACE size")
		}
		entry := acl[pos : pos+entrySize]
		a := ace{Type: entry[0], Flags: entry[1]}
		if len(entry) >= 8 {
			a.Mask = binary.LittleEndian.Uint32(entry[4:])
		}
		if a.Type == 0 || a.Type == 1 {
			if len(entry) < 16 {
				return d, errors.New("truncated standard ACE")
			}
			a.Mask = binary.LittleEndian.Uint32(entry[4:])
			a.SID, err = sidAt(entry, 8)
			if err != nil {
				return d, fmt.Errorf("invalid ACE SID: %w", err)
			}
			if len(entry) != 16+4*int(entry[9]) {
				return d, errors.New("unsupported standard ACE payload")
			}
		}
		d.ACEs = append(d.ACEs, a)
		pos += entrySize
	}
	return d, nil
}
