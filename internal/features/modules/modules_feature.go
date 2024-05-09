// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package modules

import (
	"context"
	"fmt"
	"io"
	"log"

	"github.com/algolia/algoliasearch-client-go/v3/algolia/search"
	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl-lang/decoder"
	"github.com/hashicorp/hcl-lang/lang"
	"github.com/hashicorp/terraform-ls/internal/algolia"
	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/eventbus"
	fdecoder "github.com/hashicorp/terraform-ls/internal/features/modules/decoder"
	"github.com/hashicorp/terraform-ls/internal/features/modules/hooks"
	"github.com/hashicorp/terraform-ls/internal/features/modules/jobs"
	"github.com/hashicorp/terraform-ls/internal/features/modules/state"
	"github.com/hashicorp/terraform-ls/internal/langserver/diagnostics"
	"github.com/hashicorp/terraform-ls/internal/registry"
	globalState "github.com/hashicorp/terraform-ls/internal/state"
	"github.com/hashicorp/terraform-ls/internal/telemetry"
	"github.com/hashicorp/terraform-schema/backend"
	tfmod "github.com/hashicorp/terraform-schema/module"
)

type ModulesFeature struct {
	store    *state.ModuleStore
	eventbus *eventbus.EventBus
	stopFunc context.CancelFunc
	logger   *log.Logger

	rootFeature          fdecoder.RootReader
	documentStore        *globalState.DocumentStore
	jobStore             *globalState.JobStore
	providerSchemasStore *globalState.ProviderSchemaStore
	registryModuleStore  *globalState.RegistryModuleStore

	registryClient registry.Client
	fs             jobs.ReadOnlyFS
}

func NewModulesFeature(eventbus *eventbus.EventBus, documentStore *globalState.DocumentStore, jobStore *globalState.JobStore, providerSchemasStore *globalState.ProviderSchemaStore,
	registryModuleStore *globalState.RegistryModuleStore, changeStore *globalState.ChangeStore, fs jobs.ReadOnlyFS, rootFeature fdecoder.RootReader, registryClient registry.Client) (*ModulesFeature, error) {
	store, err := state.NewModuleStore(providerSchemasStore, registryModuleStore, changeStore)
	if err != nil {
		return nil, err
	}
	discardLogger := log.New(io.Discard, "", 0)

	return &ModulesFeature{
		store:                store,
		eventbus:             eventbus,
		stopFunc:             func() {},
		logger:               discardLogger,
		documentStore:        documentStore,
		jobStore:             jobStore,
		rootFeature:          rootFeature,
		providerSchemasStore: providerSchemasStore,
		registryModuleStore:  registryModuleStore,
		fs:                   fs,
		registryClient:       registryClient,
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
	documentChanged := f.eventbus.OnDocumentChanged("feature.modules")
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
			case documentChanged := <-documentChanged:
				// TODO collect errors
				f.documentChanged(documentChanged.Context, documentChanged.Path)

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

func (f *ModulesFeature) Diagnostics(path string) diagnostics.Diagnostics {
	diags := diagnostics.NewDiagnostics()

	mod, err := f.store.ModuleRecordByPath(path)
	if err != nil {
		return diags
	}

	for source, dm := range mod.ModuleDiagnostics {
		diags.Append(source, dm.AutoloadedOnly().AsMap())
	}

	return diags
}

func (f *ModulesFeature) Telemetry(path string) map[string]interface{} {
	properties := make(map[string]interface{})

	mod, err := f.store.ModuleRecordByPath(path)
	if err != nil {
		return properties
	}

	if len(mod.Meta.CoreRequirements) > 0 {
		properties["tfRequirements"] = mod.Meta.CoreRequirements.String()
	}
	if mod.Meta.Cloud != nil {
		properties["cloud"] = true

		hostname := mod.Meta.Cloud.Hostname

		// https://developer.hashicorp.com/terraform/language/settings/terraform-cloud#usage-example
		// Required for Terraform Enterprise;
		// Defaults to app.terraform.io for HCP Terraform
		if hostname == "" {
			hostname = "app.terraform.io"
		}

		// anonymize any non-default hostnames
		if hostname != "app.terraform.io" {
			hostname = "custom-hostname"
		}

		properties["cloud.hostname"] = hostname
	}
	if mod.Meta.Backend != nil {
		properties["backend"] = mod.Meta.Backend.Type
		if data, ok := mod.Meta.Backend.Data.(*backend.Remote); ok {
			hostname := data.Hostname

			// https://developer.hashicorp.com/terraform/language/settings/backends/remote#hostname
			// Defaults to app.terraform.io for HCP Terraform
			if hostname == "" {
				hostname = "app.terraform.io"
			}

			// anonymize any non-default hostnames
			if hostname != "app.terraform.io" {
				hostname = "custom-hostname"
			}

			properties["backend.remote.hostname"] = hostname
		}
	}
	if len(mod.Meta.ProviderRequirements) > 0 {
		reqs := make(map[string]string, 0)
		for pAddr, cons := range mod.Meta.ProviderRequirements {
			if telemetry.IsPublicProvider(pAddr) {
				reqs[pAddr.String()] = cons.String()
				continue
			}

			// anonymize any unknown providers or the ones not publicly listed
			id, err := f.providerSchemasStore.GetProviderID(pAddr)
			if err != nil {
				continue
			}
			addr := fmt.Sprintf("unlisted/%s", id)
			reqs[addr] = cons.String()
		}
		properties["providerRequirements"] = reqs
	}

	modId, err := f.store.GetModuleID(mod.Path())
	if err != nil {
		return properties
	}
	properties["moduleId"] = modId

	return properties
}

func (f *ModulesFeature) MetadataReady(dir document.DirHandle) (<-chan struct{}, bool, error) {
	return f.store.MetadataReady(dir)
}
