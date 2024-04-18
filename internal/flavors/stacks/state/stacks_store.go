// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package state

import (
	"log"

	"github.com/hashicorp/go-memdb"
	"github.com/hashicorp/terraform-ls/internal/flavors/stacks/ast"
	"github.com/hashicorp/terraform-ls/internal/state"
	op "github.com/hashicorp/terraform-ls/internal/terraform/module/operation"
)

type StacksStore struct {
	db        *memdb.MemDB
	tableName string
	logger    *log.Logger
}

func (s *StacksStore) Add(modPath string) error {
	txn := s.db.Txn(true)
	defer txn.Abort()

	err := s.add(txn, modPath)
	if err != nil {
		return err
	}
	txn.Commit()

	return nil
}

func (s *StacksStore) AddIfNotExists(path string) error {
	txn := s.db.Txn(true)
	defer txn.Abort()

	_, err := stacksByPath(txn, path)
	if err != nil {
		if state.IsRecordNotFound(err) {
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

func (s *StacksStore) Exists(path string) bool {
	txn := s.db.Txn(false)

	obj, err := txn.First(s.tableName, "id", path)
	if err != nil {
		return false
	}

	return obj != nil
}

func (s *StacksStore) List() ([]*StacksRecord, error) {
	txn := s.db.Txn(false)

	it, err := txn.Get(s.tableName, "id")
	if err != nil {
		return nil, err
	}

	stacks := make([]*StacksRecord, 0)
	for item := it.Next(); item != nil; item = it.Next() {
		stack := item.(*StacksRecord)
		stacks = append(stacks, stack)
	}

	return stacks, nil
}

func (s *StacksStore) StacksRecordByPath(path string) (*StacksRecord, error) {
	txn := s.db.Txn(false)

	stack, err := stacksByPath(txn, path)
	if err != nil {
		return nil, err
	}

	return stack, nil
}

func (s *StacksStore) SetStacksDiagnosticsState(
	path string,
	source ast.DiagnosticSource,
	state op.OpState,
) error {
	txn := s.db.Txn(true)
	defer txn.Abort()

	stack, err := stacksCopyByPath(txn, path)
	if err != nil {
		return err
	}
	stack.StacksDiagnosticsState[source] = state

	err = txn.Insert(s.tableName, stack)
	if err != nil {
		return err
	}

	txn.Commit()
	return nil
}

func (s *StacksStore) UpdateParsedStacksFiles(path string, pFiles ast.StacksFiles, pErr error) error {
	txn := s.db.Txn(true)
	defer txn.Abort()

	stack, err := stacksCopyByPath(txn, path)
	if err != nil {
		return err
	}

	stack.ParsedStacksFiles = pFiles

	stack.StacksParsingErr = pErr

	err = txn.Insert(s.tableName, stack)
	if err != nil {
		return err
	}

	txn.Commit()
	return nil
}

func (s *StacksStore) UpdateStacksDiagnostics(path string, source ast.DiagnosticSource, diags ast.StacksDiags) error {
	txn := s.db.Txn(true)
	txn.Defer(func() {
		s.SetStacksDiagnosticsState(path, source, op.OpStateLoaded)
	})
	defer txn.Abort()

	oldStack, err := stacksByPath(txn, path)
	if err != nil {
		return err
	}

	stack := oldStack.Copy()
	if stack.StacksDiagnostics == nil {
		stack.StacksDiagnostics = make(ast.SourceStacksDiags)
	}
	stack.StacksDiagnostics[source] = diags

	err = txn.Insert(s.tableName, stack)
	if err != nil {
		return err
	}

	// TODO! is still relevant?
	// err = s.queueModuleChange(txn, oldStack, stack)
	// if err != nil {
	// 	return err
	// }

	txn.Commit()
	return nil
}

func (s *StacksStore) add(txn *memdb.Txn, modPath string) error {
	// TODO: Introduce Exists method to Txn?
	obj, err := txn.First(s.tableName, "id", modPath)
	if err != nil {
		return err
	}
	if obj != nil {
		return &state.AlreadyExistsError{
			Idx: modPath,
		}
	}

	mod := newStacks(modPath)
	err = txn.Insert(s.tableName, mod)
	if err != nil {
		return err
	}

	return nil
}

func stacksByPath(txn *memdb.Txn, path string) (*StacksRecord, error) {
	obj, err := txn.First(stacksTableName, "id", path)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, &state.RecordNotFoundError{
			Source: path,
		}
	}
	return obj.(*StacksRecord), nil
}

func stacksCopyByPath(txn *memdb.Txn, path string) (*StacksRecord, error) {
	stack, err := stacksByPath(txn, path)
	if err != nil {
		return nil, err
	}

	return stack.Copy(), nil
}
