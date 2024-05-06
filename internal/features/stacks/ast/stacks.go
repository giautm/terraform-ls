// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package ast

import (
	"strings"

	"github.com/hashicorp/hcl/v2"
	globalAst "github.com/hashicorp/terraform-ls/internal/terraform/ast"
)

type StacksFilename string

func (mf StacksFilename) String() string {
	return string(mf)
}

func (mf StacksFilename) IsJSON() bool {
	return strings.HasSuffix(string(mf), ".json")
}

func (mf StacksFilename) IsIgnored() bool {
	return globalAst.IsIgnoredFile(string(mf))
}

func IsStacksFilename(name string) bool {
	return strings.HasSuffix(name, ".tfstack.hcl") ||
		strings.HasSuffix(name, ".tfstack.json")
}

type StacksFiles map[StacksFilename]*hcl.File

func ModFilesFromMap(m map[string]*hcl.File) StacksFiles {
	mf := make(StacksFiles, len(m))
	for name, file := range m {
		mf[StacksFilename(name)] = file
	}
	return mf
}

func (mf StacksFiles) AsMap() map[string]*hcl.File {
	m := make(map[string]*hcl.File, len(mf))
	for name, file := range mf {
		m[string(name)] = file
	}
	return m
}

func (mf StacksFiles) Copy() StacksFiles {
	m := make(StacksFiles, len(mf))
	for name, file := range mf {
		m[name] = file
	}
	return m
}

type StacksDiags map[StacksFilename]hcl.Diagnostics

func StacksDiagsFromMap(m map[string]hcl.Diagnostics) StacksDiags {
	mf := make(StacksDiags, len(m))
	for name, file := range m {
		mf[StacksFilename(name)] = file
	}
	return mf
}

func (md StacksDiags) AutoloadedOnly() StacksDiags {
	diags := make(StacksDiags)
	for name, f := range md {
		if !name.IsIgnored() {
			diags[name] = f
		}
	}
	return diags
}

func (md StacksDiags) AsMap() map[string]hcl.Diagnostics {
	m := make(map[string]hcl.Diagnostics, len(md))
	for name, diags := range md {
		m[string(name)] = diags
	}
	return m
}

func (md StacksDiags) Copy() StacksDiags {
	m := make(StacksDiags, len(md))
	for name, diags := range md {
		m[name] = diags
	}
	return m
}

func (md StacksDiags) Count() int {
	count := 0
	for _, diags := range md {
		count += len(diags)
	}
	return count
}

type SourceStacksDiags map[globalAst.DiagnosticSource]StacksDiags

func (smd SourceStacksDiags) Count() int {
	count := 0
	for _, diags := range smd {
		count += diags.Count()
	}
	return count
}
