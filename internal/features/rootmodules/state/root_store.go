// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package state

import (
	"log"

	"github.com/hashicorp/go-memdb"
)

type RootStore struct {
	db        *memdb.MemDB
	tableName string
	logger    *log.Logger
}

func (s *RootStore) SetLogger(logger *log.Logger) {
	s.logger = logger
}
