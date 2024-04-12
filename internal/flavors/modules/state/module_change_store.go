// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package state

import "github.com/hashicorp/go-memdb"

type ModuleChangeStore struct {
	db *memdb.MemDB
}
