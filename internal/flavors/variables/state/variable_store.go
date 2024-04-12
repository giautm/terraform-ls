// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package state

import (
	"log"

	"github.com/hashicorp/go-memdb"
	"github.com/hashicorp/hcl-lang/reference"
	"github.com/hashicorp/terraform-ls/internal/terraform/ast"
)

type VariableStore struct {
	db        *memdb.MemDB
	tableName string
	logger    *log.Logger
}

func (s *VariableStore) Add(modPath string) error {
	txn := s.db.Txn(true)
	defer txn.Abort()

	err := s.add(txn, modPath)
	if err != nil {
		return err
	}
	txn.Commit()

	return nil
}

func (s *VariableStore) add(txn *memdb.Txn, modPath string) error {
	// TODO: Introduce Exists method to Txn?
	obj, err := txn.First(s.tableName, "id", modPath)
	if err != nil {
		return err
	}
	if obj != nil {
		return &AlreadyExistsError{
			Idx: modPath,
		}
	}

	mod := newVariableRecord(modPath)
	err = txn.Insert(s.tableName, mod)
	if err != nil {
		return err
	}

	// TODO! queue change
	// err = s.queueModuleChange(txn, nil, mod)
	// if err != nil {
	// 	return err
	// }

	return nil
}

func (s *VariableStore) AddIfNotExists(path string) error {
	txn := s.db.Txn(true)
	defer txn.Abort()

	_, err := variableRecordByPath(txn, path)
	if err != nil {
		if IsRecordNotFound(err) {
			err := s.add(txn, path)
			if err != nil {
				return err
			}
			txn.Commit()
			return nil
		}

		return err
	}

	return nil
}

func (s *VariableStore) Remove(modPath string) error {
	txn := s.db.Txn(true)
	defer txn.Abort()

	oldObj, err := txn.First(s.tableName, "id", modPath)
	if err != nil {
		return err
	}

	if oldObj == nil {
		// already removed
		return nil
	}

	// TODO! queue change
	// oldMod := oldObj.(*VariableRecord)
	// err = s.queueModuleChange(txn, oldMod, nil)
	// if err != nil {
	// 	return err
	// }

	_, err = txn.DeleteAll(s.tableName, "id", modPath)
	if err != nil {
		return err
	}

	txn.Commit()
	return nil
}

func (s *VariableStore) List() ([]*VariableRecord, error) {
	txn := s.db.Txn(false)

	it, err := txn.Get(s.tableName, "id")
	if err != nil {
		return nil, err
	}

	records := make([]*VariableRecord, 0)
	for item := it.Next(); item != nil; item = it.Next() {
		record := item.(*VariableRecord)
		records = append(records, record)
	}

	return records, nil
}

func (s *VariableStore) Exists(path string) bool {
	txn := s.db.Txn(false)

	obj, err := txn.First(s.tableName, "id", path)
	if err != nil {
		return false
	}

	return obj != nil
}

func (s *VariableStore) VariableRecordByPath(path string) (*VariableRecord, error) {
	txn := s.db.Txn(false)

	mod, err := variableRecordByPath(txn, path)
	if err != nil {
		return nil, err
	}

	return mod, nil
}

func variableRecordByPath(txn *memdb.Txn, path string) (*VariableRecord, error) {
	obj, err := txn.First(variableTableName, "id", path)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, &RecordNotFoundError{
			Source: path,
		}
	}
	return obj.(*VariableRecord), nil
}

func variableRecordCopyByPath(txn *memdb.Txn, path string) (*VariableRecord, error) {
	record, err := variableRecordByPath(txn, path)
	if err != nil {
		return nil, err
	}

	return record.Copy(), nil
}

func (s *VariableStore) UpdateParsedVarsFiles(path string, vFiles ast.VarsFiles, vErr error) error {
	txn := s.db.Txn(true)
	defer txn.Abort()

	mod, err := variableRecordCopyByPath(txn, path)
	if err != nil {
		return err
	}

	mod.ParsedVarsFiles = vFiles

	mod.VarsParsingErr = vErr

	err = txn.Insert(s.tableName, mod)
	if err != nil {
		return err
	}

	txn.Commit()
	return nil
}

func (s *VariableStore) UpdateVarsDiagnostics(path string, source ast.DiagnosticSource, diags ast.VarsDiags) error {
	txn := s.db.Txn(true)
	txn.Defer(func() {
		s.SetVarsDiagnosticsState(path, source, op.OpStateLoaded)
	})
	defer txn.Abort()

	oldMod, err := variableRecordByPath(txn, path)
	if err != nil {
		return err
	}

	mod := oldMod.Copy()
	if mod.VarsDiagnostics == nil {
		mod.VarsDiagnostics = make(ast.SourceVarsDiags)
	}
	mod.VarsDiagnostics[source] = diags

	err = txn.Insert(s.tableName, mod)
	if err != nil {
		return err
	}

	// TODO! queue change
	// err = s.queueModuleChange(txn, oldMod, mod)
	// if err != nil {
	// 	return err
	// }

	txn.Commit()
	return nil
}

func (s *VariableStore) SetVarsDiagnosticsState(path string, source ast.DiagnosticSource, state op.OpState) error {
	txn := s.db.Txn(true)
	defer txn.Abort()

	mod, err := variableRecordCopyByPath(txn, path)
	if err != nil {
		return err
	}
	mod.VarsDiagnosticsState[source] = state

	err = txn.Insert(s.tableName, mod)
	if err != nil {
		return err
	}

	txn.Commit()
	return nil
}

func (s *VariableStore) SetVarsReferenceOriginsState(path string, state op.OpState) error {
	txn := s.db.Txn(true)
	defer txn.Abort()

	mod, err := variableRecordCopyByPath(txn, path)
	if err != nil {
		return err
	}

	mod.VarsRefOriginsState = state
	err = txn.Insert(s.tableName, mod)
	if err != nil {
		return err
	}

	txn.Commit()
	return nil
}

func (s *VariableStore) UpdateVarsReferenceOrigins(path string, origins reference.Origins, roErr error) error {
	txn := s.db.Txn(true)
	txn.Defer(func() {
		s.SetVarsReferenceOriginsState(path, op.OpStateLoaded)
	})
	defer txn.Abort()

	mod, err := variableRecordCopyByPath(txn, path)
	if err != nil {
		return err
	}

	mod.VarsRefOrigins = origins
	mod.VarsRefOriginsErr = roErr

	err = txn.Insert(s.tableName, mod)
	if err != nil {
		return err
	}

	txn.Commit()
	return nil
}
