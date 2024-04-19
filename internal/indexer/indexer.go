// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package indexer

import (
	"io"
	"log"

	"github.com/hashicorp/terraform-ls/internal/job"
	"github.com/hashicorp/terraform-ls/internal/registry"
	"github.com/hashicorp/terraform-ls/internal/state"
	"github.com/hashicorp/terraform-ls/internal/terraform/exec"
)

type Indexer struct {
	logger                *log.Logger
	rootStore             *state.RootStore
	terraformVersionStore *state.TerraformVersionStore
	fs                    ReadOnlyFS
	jobStore              job.JobStore
	tfExecFactory         exec.ExecutorFactory
	registryClient        registry.Client
}

func NewIndexer(fs ReadOnlyFS, jobStore job.JobStore, rootStore *state.RootStore, terraformVersionStore *state.TerraformVersionStore, tfExec exec.ExecutorFactory, registryClient registry.Client) *Indexer {

	discardLogger := log.New(io.Discard, "", 0)

	return &Indexer{
		fs:                    fs,
		jobStore:              jobStore,
		rootStore:             rootStore,
		terraformVersionStore: terraformVersionStore,
		tfExecFactory:         tfExec,
		registryClient:        registryClient,
		logger:                discardLogger,
	}
}

func (idx *Indexer) SetLogger(logger *log.Logger) {
	idx.logger = logger
}

type Collector interface {
	CollectJobId(jobId job.ID)
}
