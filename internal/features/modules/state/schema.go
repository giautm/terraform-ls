// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package state

import (
	"io"
	"log"
	"time"

	"github.com/hashicorp/go-memdb"
	"github.com/hashicorp/terraform-ls/internal/state"
	globalState "github.com/hashicorp/terraform-ls/internal/state"
)

const (
	moduleTableName        = "module"
	moduleIdsTableName     = "module_ids"
	moduleChangesTableName = "module_changes"
)

var dbSchema = &memdb.DBSchema{
	Tables: map[string]*memdb.TableSchema{
		moduleTableName: {
			Name: moduleTableName,
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &memdb.StringFieldIndex{Field: "path"},
				},
			},
		},
		moduleIdsTableName: {
			Name: moduleIdsTableName,
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &memdb.StringFieldIndex{Field: "Path"},
				},
			},
		},
		moduleChangesTableName: {
			Name: moduleChangesTableName,
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &state.DirHandleFieldIndexer{Field: "DirHandle"},
				},
				"time": {
					Name:    "time",
					Indexer: &state.TimeFieldIndex{Field: "FirstChangeTime"},
				},
			},
		},
	},
}

func NewModuleStore(providerSchemasStore *globalState.ProviderSchemaStore, registryModuleStore *globalState.RegistryModuleStore, rootStore *globalState.RootStore, terraformVersionStore *globalState.TerraformVersionStore) (*ModuleStore, error) {
	db, err := memdb.NewMemDB(dbSchema)
	if err != nil {
		return nil, err
	}

	discardLogger := log.New(io.Discard, "", 0)

	return &ModuleStore{
		db:                    db,
		tableName:             moduleTableName,
		logger:                discardLogger,
		TimeProvider:          time.Now,
		MaxModuleNesting:      50,
		providerSchemasStore:  providerSchemasStore,
		registryModuleStore:   registryModuleStore,
		rootStore:             rootStore,
		terraformVersionStore: terraformVersionStore,
	}, nil
}
