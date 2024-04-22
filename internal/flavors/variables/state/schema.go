// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package state

import (
	"io"
	"log"

	"github.com/hashicorp/go-memdb"
)

const (
	variableTableName = "variable"
)

var dbSchema = &memdb.DBSchema{
	Tables: map[string]*memdb.TableSchema{
		variableTableName: {
			Name: variableTableName,
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &memdb.StringFieldIndex{Field: "path"},
				},
			},
		},
	},
}

func NewVariableStore() (*VariableStore, error) {
	db, err := memdb.NewMemDB(dbSchema)
	if err != nil {
		return nil, err
	}
	discardLogger := log.New(io.Discard, "", 0)

	return &VariableStore{
		db:        db,
		tableName: variableTableName,
		logger:    discardLogger,
	}, nil
}
