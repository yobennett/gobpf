// Copyright 2016 PLUMgrid
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

package bcc

import (
	"bytes"
	"fmt"
	"unsafe"
)

/*
#cgo CFLAGS: -I/usr/include/bcc/compat
#cgo LDFLAGS: -lbcc
#include <bcc/bpf_common.h>
#include <bcc/libbpf.h>
*/
import "C"

type Table struct {
	id     C.size_t
	module *Module
}

// New tables returns a refernce to a BPF table.
func NewTable(id C.size_t, module *Module) *Table {
	return &Table{
		id:     id,
		module: module,
	}
}

// ID returns the table id.
func (table *Table) ID() string {
	return C.GoString(C.bpf_table_name(table.module.p, table.id))
}

// Name returns the table name.
func (table *Table) Name() string {
	return C.GoString(C.bpf_table_name(table.module.p, table.id))
}

// Config returns the table properties (name, fd, ...).
func (table *Table) Config() map[string]interface{} {
	mod := table.module.p
	return map[string]interface{}{
		"name":      C.GoString(C.bpf_table_name(mod, table.id)),
		"fd":        int(C.bpf_table_fd_id(mod, table.id)),
		"key_size":  uint64(C.bpf_table_key_size_id(mod, table.id)),
		"leaf_size": uint64(C.bpf_table_leaf_size_id(mod, table.id)),
		"key_desc":  C.GoString(C.bpf_table_key_desc_id(mod, table.id)),
		"leaf_desc": C.GoString(C.bpf_table_leaf_desc_id(mod, table.id)),
	}
}

func (table *Table) keyToBytes(keyStr string) ([]byte, error) {
	mod := table.module.p
	key_size := C.bpf_table_key_size_id(mod, table.id)
	key := make([]byte, key_size)
	keyP := unsafe.Pointer(&key[0])
	keyCS := C.CString(keyStr)
	defer C.free(unsafe.Pointer(keyCS))
	r := C.bpf_table_key_sscanf(mod, table.id, keyCS, keyP)
	if r != 0 {
		return nil, fmt.Errorf("error scanning key (%v) from string", keyStr)
	}
	return key, nil
}

func (table *Table) leafToBytes(leafStr string) ([]byte, error) {
	mod := table.module.p
	leaf_size := C.bpf_table_leaf_size_id(mod, table.id)
	leaf := make([]byte, leaf_size)
	leafP := unsafe.Pointer(&leaf[0])
	leafCS := C.CString(leafStr)
	defer C.free(unsafe.Pointer(leafCS))
	r := C.bpf_table_leaf_sscanf(mod, table.id, leafCS, leafP)
	if r != 0 {
		return nil, fmt.Errorf("error scanning leaf (%v) from string", leafStr)
	}
	return leaf, nil
}

// Entry represents a table entry.
type Entry struct {
	Key   string
	Value string
}

// Get takes a key and returns the value or nil, and an 'ok' style indicator.
func (table *Table) Get(keyStr string) (interface{}, bool) {
	mod := table.module.p
	fd := C.bpf_table_fd_id(mod, table.id)
	leaf_size := C.bpf_table_leaf_size_id(mod, table.id)
	key, err := table.keyToBytes(keyStr)
	if err != nil {
		return nil, false
	}
	leaf := make([]byte, leaf_size)
	keyP := unsafe.Pointer(&key[0])
	leafP := unsafe.Pointer(&leaf[0])
	r := C.bpf_lookup_elem(fd, keyP, leafP)
	if r != 0 {
		return nil, false
	}
	leafStr := make([]byte, leaf_size*8)
	leafStrP := (*C.char)(unsafe.Pointer(&leafStr[0]))
	r = C.bpf_table_leaf_snprintf(mod, table.id, leafStrP, C.size_t(len(leafStr)), leafP)
	if r != 0 {
		return nil, false
	}
	return Entry{
		Key:   keyStr,
		Value: string(leafStr[:bytes.IndexByte(leafStr, 0)]),
	}, true
}

// Set a key to a value.
func (table *Table) Set(keyStr, leafStr string) error {
	if table == nil || table.module.p == nil {
		panic("table is nil")
	}
	fd := C.bpf_table_fd_id(table.module.p, table.id)
	key, err := table.keyToBytes(keyStr)
	if err != nil {
		return err
	}
	leaf, err := table.leafToBytes(leafStr)
	if err != nil {
		return err
	}
	keyP := unsafe.Pointer(&key[0])
	leafP := unsafe.Pointer(&leaf[0])
	r, err := C.bpf_update_elem(fd, keyP, leafP, 0)
	if r != 0 {
		return fmt.Errorf("Table.Set: unable to update element (%s=%s): %v", keyStr, leafStr, err)
	}
	return nil
}

// Delete a key.
func (table *Table) Delete(keyStr string) error {
	fd := C.bpf_table_fd_id(table.module.p, table.id)
	key, err := table.keyToBytes(keyStr)
	if err != nil {
		return err
	}
	keyP := unsafe.Pointer(&key[0])
	r, err := C.bpf_delete_elem(fd, keyP)
	if r != 0 {
		return fmt.Errorf("Table.Delete: unable to delete element (%s): %v", keyStr, err)
	}
	return nil
}

// Iter returns a receiver channel to iterate over all table entries.
func (table *Table) Iter() <-chan Entry {
	mod := table.module.p
	ch := make(chan Entry, 128)
	go func() {
		defer close(ch)
		fd := C.bpf_table_fd_id(mod, table.id)
		key_size := C.bpf_table_key_size_id(mod, table.id)
		leaf_size := C.bpf_table_leaf_size_id(mod, table.id)
		key := make([]byte, key_size)
		leaf := make([]byte, leaf_size)
		keyP := unsafe.Pointer(&key[0])
		leafP := unsafe.Pointer(&leaf[0])
		alternateKeys := []byte{0xff, 0x55}
		res := C.bpf_lookup_elem(fd, keyP, leafP)
		// make sure the start iterator is an invalid key
		for i := 0; i <= len(alternateKeys); i++ {
			if res < 0 {
				break
			}
			for j := range key {
				key[j] = alternateKeys[i]
			}
			res = C.bpf_lookup_elem(fd, keyP, leafP)
		}
		if res == 0 {
			return
		}
		keyStr := make([]byte, key_size*8)
		leafStr := make([]byte, leaf_size*8)
		keyStrP := (*C.char)(unsafe.Pointer(&keyStr[0]))
		leafStrP := (*C.char)(unsafe.Pointer(&leafStr[0]))
		for res = C.bpf_get_next_key(fd, keyP, keyP); res == 0; res = C.bpf_get_next_key(fd, keyP, keyP) {
			r := C.bpf_lookup_elem(fd, keyP, leafP)
			if r != 0 {
				continue
			}
			r = C.bpf_table_key_snprintf(mod, table.id, keyStrP, C.size_t(len(keyStr)), keyP)
			if r != 0 {
				break
			}
			r = C.bpf_table_leaf_snprintf(mod, table.id, leafStrP, C.size_t(len(leafStr)), leafP)
			if r != 0 {
				break
			}
			ch <- Entry{
				Key:   string(keyStr[:bytes.IndexByte(keyStr, 0)]),
				Value: string(leafStr[:bytes.IndexByte(leafStr, 0)]),
			}
		}
	}()
	return ch
}
