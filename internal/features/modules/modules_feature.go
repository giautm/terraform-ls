// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package modules

import (
	"context"
	"io"
	"log"

	"github.com/algolia/algoliasearch-client-go/v3/algolia/search"
	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl-lang/decoder"
	"github.com/hashicorp/hcl-lang/lang"
	"github.com/hashicorp/terraform-ls/internal/algolia"
	"github.com/hashicorp/terraform-ls/internal/eventbus"
	fdecoder "github.com/hashicorp/terraform-ls/internal/features/modules/decoder"
	"github.com/hashicorp/terraform-ls/internal/features/modules/hooks"
	"github.com/hashicorp/terraform-ls/internal/features/modules/jobs"
	"github.com/hashicorp/terraform-ls/internal/features/modules/state"
	"github.com/hashicorp/terraform-ls/internal/registry"
	globalState "github.com/hashicorp/terraform-ls/internal/state"
	tfmod "github.com/hashicorp/terraform-schema/module"
)

type ModulesFeature struct {
	store    *state.ModuleStore
	eventbus *eventbus.EventBus
	stopFunc context.CancelFunc
	logger   *log.Logger

	rootFeature          fdecoder.RootReader
	jobStore             *globalState.JobStore
	providerSchemasStore *globalState.ProviderSchemaStore
	registryModuleStore  *globalState.RegistryModuleStore

	registryClient registry.Client
	fs             jobs.ReadOnlyFS
}

func NewModulesFeature(eventbus *eventbus.EventBus, jobStore *globalState.JobStore, providerSchemasStore *globalState.ProviderSchemaStore, registryModuleStore *globalState.RegistryModuleStore, fs jobs.ReadOnlyFS, rootFeature fdecoder.RootReader) (*ModulesFeature, error) {
	store, err := state.NewModuleStore(providerSchemasStore, registryModuleStore)
	if err != nil {
		return nil, err
	}
	discardLogger := log.New(io.Discard, "", 0)

	return &ModulesFeature{
		store:                store,
		eventbus:             eventbus,
		stopFunc:             func() {},
		logger:               discardLogger,
		jobStore:             jobStore,
		rootFeature:          rootFeature,
		providerSchemasStore: providerSchemasStore,
		registryModuleStore:  registryModuleStore,
		fs:                   fs,
	}, nil
}

func (f *ModulesFeature) SetLogger(logger *log.Logger) {
	f.logger = logger
	f.store.SetLogger(logger)
}

func (f *ModulesFeature) Start(ctx context.Context) {
	ctx, cancelFunc := context.WithCancel(ctx)
	f.stopFunc = cancelFunc

	didOpen := f.eventbus.OnDidOpen("feature.modules")
	didChange := f.eventbus.OnDidChange("feature.modules")
	discover := f.eventbus.OnDiscover("feature.modules")
	go func() {
		for {
			select {
			case didOpen := <-didOpen:
				// TODO collect errors
				f.didOpen(didOpen.Context, didOpen.Dir, didOpen.LanguageID)
			case didChange := <-didChange:
				// TODO move into own handler
				// TODO collect errors
				f.didOpen(didChange.Context, didChange.Dir, didChange.LanguageID)
			case discover := <-discover:
				// TODO collect errors
				f.discover(discover.Path, discover.Files)

			case <-ctx.Done():
				return
			}
		}
	}()
}

func (f *ModulesFeature) Stop() {
	f.stopFunc()
	f.logger.Print("stopped modules feature")
}

func (f *ModulesFeature) PathContext(path lang.Path) (*decoder.PathContext, error) {
	pathReader := &fdecoder.PathReader{
		StateReader: f.store,
		RootReader:  f.rootFeature,
	}

	return pathReader.PathContext(path)
}

func (f *ModulesFeature) Paths(ctx context.Context) []lang.Path {
	pathReader := &fdecoder.PathReader{
		StateReader: f.store,
		RootReader:  f.rootFeature,
	}

	return pathReader.Paths(ctx)
}

func (f *ModulesFeature) DeclaredModuleCalls(modPath string) (map[string]tfmod.DeclaredModuleCall, error) {
	return f.store.DeclaredModuleCalls(modPath)
}

func (f *ModulesFeature) ProviderRequirements(modPath string) (tfmod.ProviderRequirements, error) {
	mod, err := f.store.ModuleRecordByPath(modPath)
	if err != nil {
		return nil, err
	}

	return mod.Meta.ProviderRequirements, nil
}

func (f *ModulesFeature) CoreRequirements(modPath string) (version.Constraints, error) {
	mod, err := f.store.ModuleRecordByPath(modPath)
	if err != nil {
		return nil, err
	}

	return mod.Meta.CoreRequirements, nil
}

func (f *ModulesFeature) ModuleInputs(modPath string) (map[string]tfmod.Variable, error) {
	mod, err := f.store.ModuleRecordByPath(modPath)
	if err != nil {
		return nil, err
	}

	return mod.Meta.Variables, nil
}

func (f *ModulesFeature) AppendCompletionHooks(srvCtx context.Context, decoderContext decoder.DecoderContext) {
	h := hooks.Hooks{
		ModStore:       f.store,
		RegistryClient: f.registryClient,
		Logger:         f.logger,
	}

	credentials, ok := algolia.CredentialsFromContext(srvCtx)
	if ok {
		h.AlgoliaClient = search.NewClient(credentials.AppID, credentials.APIKey)
	}

	decoderContext.CompletionHooks["CompleteLocalModuleSources"] = h.LocalModuleSources
	decoderContext.CompletionHooks["CompleteRegistryModuleSources"] = h.RegistryModuleSources
	decoderContext.CompletionHooks["CompleteRegistryModuleVersions"] = h.RegistryModuleVersions
}
