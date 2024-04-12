// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl-lang/lang"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/hashicorp/terraform-ls/internal/document"
	"github.com/hashicorp/terraform-ls/internal/job"
	"github.com/hashicorp/terraform-ls/internal/langserver/diagnostics"
	"github.com/hashicorp/terraform-ls/internal/registry"
	"github.com/hashicorp/terraform-ls/internal/schemas"
	"github.com/hashicorp/terraform-ls/internal/state"
	"github.com/hashicorp/terraform-ls/internal/terraform/ast"
	"github.com/hashicorp/terraform-ls/internal/terraform/datadir"
	op "github.com/hashicorp/terraform-ls/internal/terraform/module/operation"
	tfaddr "github.com/hashicorp/terraform-registry-address"
	tfregistry "github.com/hashicorp/terraform-schema/registry"
	tfschema "github.com/hashicorp/terraform-schema/schema"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/hashicorp/terraform-ls/internal/terraform/module"

type DeferFunc func(opError error)

// GetTerraformVersion obtains "installed" Terraform version
// which can inform what version of core schema to pick.
// Knowing the version is not required though as we can rely on
// the constraint in `required_version` (as parsed via
// [LoadModuleMetadata] and compare it against known released versions.
func GetTerraformVersion(ctx context.Context, rootStore *state.RootStore, modPath string) error {
	mod, err := rootStore.RootRecordByPath(modPath)
	if err != nil {
		return err
	}

	// Avoid getting version if getting is already in progress or already known
	if mod.TerraformVersionState != op.OpStateUnknown && !job.IgnoreState(ctx) {
		return job.StateNotChangedErr{Dir: document.DirHandleFromPath(modPath)}
	}

	err = rootStore.SetTerraformVersionState(modPath, op.OpStateLoading)
	if err != nil {
		return err
	}
	defer rootStore.SetTerraformVersionState(modPath, op.OpStateLoaded)

	tfExec, err := TerraformExecutorForModule(ctx, mod.Path())
	if err != nil {
		sErr := rootStore.UpdateTerraformAndProviderVersions(modPath, nil, nil, err)
		if err != nil {
			return sErr
		}
		return err
	}

	v, pv, err := tfExec.Version(ctx)

	// TODO: Remove and rely purely on ParseProviderVersions
	// In most cases we get the provider version from the datadir/lockfile
	// but there is an edge case with custom plugin location
	// when this may not be available, so leveraging versions
	// from "terraform version" accounts for this.
	// See https://github.com/hashicorp/terraform-ls/issues/24
	pVersions := providerVersionsFromTfVersion(pv)

	sErr := rootStore.UpdateTerraformAndProviderVersions(modPath, v, pVersions, err)
	if sErr != nil {
		return sErr
	}

	return err
}

func providerVersionsFromTfVersion(pv map[string]*version.Version) map[tfaddr.Provider]*version.Version {
	m := make(map[tfaddr.Provider]*version.Version, 0)

	for rawAddr, v := range pv {
		pAddr, err := tfaddr.ParseProviderSource(rawAddr)
		if err != nil {
			// skip unparsable address
			continue
		}
		if pAddr.IsLegacy() {
			// TODO: check for migrations via Registry API?
		}
		m[pAddr] = v
	}

	return m
}

// ObtainSchema obtains provider schemas via Terraform CLI.
// This is useful if we do not have the schemas available
// from the embedded FS (i.e. in [PreloadEmbeddedSchema]).
func ObtainSchema(ctx context.Context, modStore *state.ModuleStore, schemaStore *state.ProviderSchemaStore, modPath string) error {
	mod, err := modStore.ModuleByPath(modPath)
	if err != nil {
		return err
	}

	// Avoid obtaining schema if it is already in progress or already known
	if mod.ProviderSchemaState != op.OpStateUnknown && !job.IgnoreState(ctx) {
		return job.StateNotChangedErr{Dir: document.DirHandleFromPath(modPath)}
	}

	pReqs, err := modStore.ProviderRequirementsForModule(modPath)
	if err != nil {
		return err
	}

	exist, err := schemaStore.AllSchemasExist(pReqs)
	if err != nil {
		return err
	}
	if exist {
		// avoid obtaining schemas if we already have it
		return nil
	}

	tfExec, err := TerraformExecutorForModule(ctx, modPath)
	if err != nil {
		sErr := modStore.FinishProviderSchemaLoading(modPath, err)
		if sErr != nil {
			return sErr
		}
		return err
	}

	ps, err := tfExec.ProviderSchemas(ctx)
	if err != nil {
		sErr := modStore.FinishProviderSchemaLoading(modPath, err)
		if sErr != nil {
			return sErr
		}
		return err
	}

	for rawAddr, pJsonSchema := range ps.Schemas {
		pAddr, err := tfaddr.ParseProviderSource(rawAddr)
		if err != nil {
			// skip unparsable address
			continue
		}

		if pAddr.IsLegacy() {
			// TODO: check for migrations via Registry API?
		}

		pSchema := tfschema.ProviderSchemaFromJson(pJsonSchema, pAddr)

		err = schemaStore.AddLocalSchema(modPath, pAddr, pSchema)
		if err != nil {
			return err
		}
	}

	return nil
}

// PreloadEmbeddedSchema loads provider schemas based on
// provider requirements parsed earlier via [LoadModuleMetadata].
// This is the cheapest way of getting provider schemas in terms
// of resources, time and complexity/UX.
func PreloadEmbeddedSchema(ctx context.Context, logger *log.Logger, fs fs.ReadDirFS, modStore *state.ModuleStore, schemaStore *state.ProviderSchemaStore, modPath string) error {
	mod, err := modStore.ModuleByPath(modPath)
	if err != nil {
		return err
	}

	// Avoid preloading schema if it is already in progress or already known
	if mod.PreloadEmbeddedSchemaState != op.OpStateUnknown && !job.IgnoreState(ctx) {
		return job.StateNotChangedErr{Dir: document.DirHandleFromPath(modPath)}
	}

	err = modStore.SetPreloadEmbeddedSchemaState(modPath, op.OpStateLoading)
	if err != nil {
		return err
	}
	defer modStore.SetPreloadEmbeddedSchemaState(modPath, op.OpStateLoaded)

	pReqs, err := modStore.ProviderRequirementsForModule(modPath)
	if err != nil {
		return err
	}

	missingReqs, err := schemaStore.MissingSchemas(pReqs)
	if err != nil {
		return err
	}
	if len(missingReqs) == 0 {
		// avoid preloading any schemas if we already have all
		return nil
	}

	for _, pAddr := range missingReqs {
		err := preloadSchemaForProviderAddr(ctx, pAddr, fs, schemaStore, logger)
		if err != nil {
			return err
		}
	}

	return nil
}

func preloadSchemaForProviderAddr(ctx context.Context, pAddr tfaddr.Provider, fs fs.ReadDirFS,
	schemaStore *state.ProviderSchemaStore, logger *log.Logger) error {

	startTime := time.Now()

	if pAddr.IsLegacy() && pAddr.Type == "terraform" {
		// The terraform provider is built into Terraform 0.11+
		// and while it's possible, users typically don't declare
		// entry in required_providers block for it.
		pAddr = tfaddr.NewProvider(tfaddr.BuiltInProviderHost, tfaddr.BuiltInProviderNamespace, "terraform")
	} else if pAddr.IsLegacy() {
		// Since we use recent version of Terraform to generate
		// embedded schemas, these will never contain legacy
		// addresses.
		//
		// A legacy namespace may come from missing
		// required_providers entry & implied requirement
		// from the provider block or 0.12-style entry,
		// such as { grafana = "1.0" }.
		//
		// Implying "hashicorp" namespace here mimics behaviour
		// of all recent (0.14+) Terraform versions.
		originalAddr := pAddr
		pAddr.Namespace = "hashicorp"
		logger.Printf("preloading schema for %s (implying %s)",
			originalAddr.ForDisplay(), pAddr.ForDisplay())
	}

	ctx, rootSpan := otel.Tracer(tracerName).Start(ctx, "preloadProviderSchema",
		trace.WithAttributes(attribute.KeyValue{
			Key:   attribute.Key("ProviderAddress"),
			Value: attribute.StringValue(pAddr.String()),
		}))
	defer rootSpan.End()

	pSchemaFile, err := schemas.FindProviderSchemaFile(fs, pAddr)
	if err != nil {
		rootSpan.RecordError(err)
		rootSpan.SetStatus(codes.Error, "schema file not found")
		if errors.Is(err, schemas.SchemaNotAvailable{Addr: pAddr}) {
			logger.Printf("preloaded schema not available for %s", pAddr)
			return nil
		}
		return err
	}

	_, span := otel.Tracer(tracerName).Start(ctx, "readProviderSchemaFile",
		trace.WithAttributes(attribute.KeyValue{
			Key:   attribute.Key("ProviderAddress"),
			Value: attribute.StringValue(pAddr.String()),
		}))
	b, err := io.ReadAll(pSchemaFile.File)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "schema file not readable")
		return err
	}
	span.SetStatus(codes.Ok, "schema file read successfully")
	span.End()

	_, span = otel.Tracer(tracerName).Start(ctx, "decodeProviderSchemaData",
		trace.WithAttributes(attribute.KeyValue{
			Key:   attribute.Key("ProviderAddress"),
			Value: attribute.StringValue(pAddr.String()),
		}))
	jsonSchemas := tfjson.ProviderSchemas{}
	err = json.Unmarshal(b, &jsonSchemas)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "schema file not decodable")
		return err
	}
	span.SetStatus(codes.Ok, "schema data decoded successfully")
	span.End()

	ps, ok := jsonSchemas.Schemas[pAddr.String()]
	if !ok {
		return fmt.Errorf("%q: no schema found in file", pAddr)
	}

	pSchema := tfschema.ProviderSchemaFromJson(ps, pAddr)
	pSchema.SetProviderVersion(pAddr, pSchemaFile.Version)

	_, span = otel.Tracer(tracerName).Start(ctx, "loadProviderSchemaDataIntoMemDb",
		trace.WithAttributes(attribute.KeyValue{
			Key:   attribute.Key("ProviderAddress"),
			Value: attribute.StringValue(pAddr.String()),
		}))
	err = schemaStore.AddPreloadedSchema(pAddr, pSchemaFile.Version, pSchema)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "loading schema into mem-db failed")
		span.End()
		existsError := &state.AlreadyExistsError{}
		if errors.As(err, &existsError) {
			// This accounts for a possible race condition
			// where we may be preloading the same schema
			// for different providers at the same time
			logger.Printf("schema for %s is already loaded", pAddr)
			return nil
		}
		return err
	}
	span.SetStatus(codes.Ok, "schema loaded successfully")
	span.End()

	elapsedTime := time.Now().Sub(startTime)
	logger.Printf("preloaded schema for %s %s in %s", pAddr, pSchemaFile.Version, elapsedTime)
	rootSpan.SetStatus(codes.Ok, "schema loaded successfully")

	return nil
}

// ParseModuleManifest parses the "module manifest" which
// contains records of installed modules, e.g. where they're
// installed on the filesystem.
// This is useful for processing any modules which are not local
// nor hosted in the Registry (which would be handled by
// [GetModuleDataFromRegistry]).
func ParseModuleManifest(ctx context.Context, fs ReadOnlyFS, rootStore *state.RootStore, modPath string) error {
	mod, err := rootStore.RootRecordByPath(modPath)
	if err != nil {
		return err
	}

	// Avoid parsing if it is already in progress or already known
	if mod.ModManifestState != op.OpStateUnknown && !job.IgnoreState(ctx) {
		return job.StateNotChangedErr{Dir: document.DirHandleFromPath(modPath)}
	}

	err = rootStore.SetModManifestState(modPath, op.OpStateLoading)
	if err != nil {
		return err
	}

	manifestPath, ok := datadir.ModuleManifestFilePath(fs, modPath)
	if !ok {
		err := fmt.Errorf("%s: manifest file does not exist", modPath)
		sErr := rootStore.UpdateModManifest(modPath, nil, err)
		if sErr != nil {
			return sErr
		}
		return err
	}

	mm, err := datadir.ParseModuleManifestFromFile(manifestPath)
	if err != nil {
		err := fmt.Errorf("failed to parse manifest: %w", err)
		sErr := rootStore.UpdateModManifest(modPath, nil, err)
		if sErr != nil {
			return sErr
		}
		return err
	}

	sErr := rootStore.UpdateModManifest(modPath, mm, err)

	if sErr != nil {
		return sErr
	}
	return err
}

// ParseProviderVersions is a job complimentary to [ObtainSchema]
// in that it obtains versions of providers/schemas from Terraform
// CLI's lock file.
func ParseProviderVersions(ctx context.Context, fs ReadOnlyFS, rootStore *state.RootStore, modPath string) error {
	mod, err := rootStore.RootRecordByPath(modPath)
	if err != nil {
		return err
	}

	// Avoid parsing if it is already in progress or already known
	if mod.InstalledProvidersState != op.OpStateUnknown && !job.IgnoreState(ctx) {
		return job.StateNotChangedErr{Dir: document.DirHandleFromPath(modPath)}
	}

	err = rootStore.SetInstalledProvidersState(modPath, op.OpStateLoading)
	if err != nil {
		return err
	}

	pvm, err := datadir.ParsePluginVersions(fs, modPath)

	sErr := rootStore.UpdateInstalledProviders(modPath, pvm, err)
	if sErr != nil {
		return sErr
	}

	return err
}

// TerraformValidate uses Terraform CLI to run validate subcommand
// and turn the provided (JSON) output into diagnostics associated
// with "invalid" parts of code.
func TerraformValidate(ctx context.Context, modStore *state.ModuleStore, modPath string) error {
	mod, err := modStore.ModuleByPath(modPath)
	if err != nil {
		return err
	}

	// Avoid validation if it is already in progress or already finished
	if mod.ModuleDiagnosticsState[ast.TerraformValidateSource] != op.OpStateUnknown && !job.IgnoreState(ctx) {
		return job.StateNotChangedErr{Dir: document.DirHandleFromPath(modPath)}
	}

	err = modStore.SetModuleDiagnosticsState(modPath, ast.TerraformValidateSource, op.OpStateLoading)
	if err != nil {
		return err
	}

	tfExec, err := TerraformExecutorForModule(ctx, mod.Path())
	if err != nil {
		return err
	}

	jsonDiags, err := tfExec.Validate(ctx)
	if err != nil {
		return err
	}
	validateDiags := diagnostics.HCLDiagsFromJSON(jsonDiags)

	return modStore.UpdateModuleDiagnostics(modPath, ast.TerraformValidateSource, ast.ModDiagsFromMap(validateDiags))
}

// GetModuleDataFromRegistry obtains data about any modules (inputs & outputs)
// from the Registry API based on module calls which were previously parsed
// via [LoadModuleMetadata]. The same data could be obtained via [ParseModuleManifest]
// but getting it from the API comes with little expectations,
// specifically the modules do not need to be installed on disk and we don't
// need to parse and decode all files.
func GetModuleDataFromRegistry(ctx context.Context, regClient registry.Client, modStore *state.ModuleStore, modRegStore *state.RegistryModuleStore, modPath string) error {
	// loop over module calls
	calls, err := modStore.DeclaredModuleCalls(modPath)
	if err != nil {
		return err
	}

	// TODO: Avoid collection if upstream jobs (parsing, meta) reported no changes

	var errs *multierror.Error

	for _, declaredModule := range calls {
		sourceAddr, ok := declaredModule.SourceAddr.(tfaddr.Module)
		if !ok {
			// skip any modules which do not come from the Registry
			continue
		}

		// check if that address was already cached
		// if there was an error finding in cache, so cache again
		exists, err := modRegStore.Exists(sourceAddr, declaredModule.Version)
		if err != nil {
			errs = multierror.Append(errs, err)
			continue
		}
		if exists {
			// entry in cache, no need to look up
			continue
		}

		// get module data from Terraform Registry
		metaData, err := regClient.GetModuleData(ctx, sourceAddr, declaredModule.Version)
		if err != nil {
			errs = multierror.Append(errs, err)

			clientError := registry.ClientError{}
			if errors.As(err, &clientError) &&
				((clientError.StatusCode >= 400 && clientError.StatusCode < 408) ||
					(clientError.StatusCode > 408 && clientError.StatusCode < 429)) {
				// Still cache the module
				err = modRegStore.CacheError(sourceAddr)
				if err != nil {
					errs = multierror.Append(errs, err)
				}
			}

			continue
		}

		inputs := make([]tfregistry.Input, len(metaData.Root.Inputs))
		for i, input := range metaData.Root.Inputs {
			isRequired := isRegistryModuleInputRequired(metaData.PublishedAt, input)
			inputs[i] = tfregistry.Input{
				Name:        input.Name,
				Description: lang.Markdown(input.Description),
				Required:    isRequired,
			}

			inputType := cty.DynamicPseudoType
			if input.Type != "" {
				// Registry API unfortunately doesn't marshal types using
				// cty marshalers, making it lossy, so we just try to decode
				// on best-effort basis.
				rawType := []byte(fmt.Sprintf("%q", input.Type))
				typ, err := ctyjson.UnmarshalType(rawType)
				if err == nil {
					inputType = typ
				}
			}
			inputs[i].Type = inputType

			if input.Default != "" {
				// Registry API unfortunately doesn't marshal values using
				// cty marshalers, making it lossy, so we just try to decode
				// on best-effort basis.
				val, err := ctyjson.Unmarshal([]byte(input.Default), inputType)
				if err == nil {
					inputs[i].Default = val
				}
			}
		}
		outputs := make([]tfregistry.Output, len(metaData.Root.Outputs))
		for i, output := range metaData.Root.Outputs {
			outputs[i] = tfregistry.Output{
				Name:        output.Name,
				Description: lang.Markdown(output.Description),
			}
		}

		modVersion, err := version.NewVersion(metaData.Version)
		if err != nil {
			errs = multierror.Append(errs, err)
			continue
		}

		// if not, cache it
		err = modRegStore.Cache(sourceAddr, modVersion, inputs, outputs)
		if err != nil {
			// A different job which ran in parallel for a different module block
			// with the same source may have already cached the same module.
			existsError := &state.AlreadyExistsError{}
			if errors.As(err, &existsError) {
				continue
			}

			errs = multierror.Append(errs, err)
			continue
		}
	}

	return errs.ErrorOrNil()
}

// isRegistryModuleInputRequired checks whether the module input is required.
// It reflects the fact that modules ingested into the Registry
// may have used `default = null` (implying optional variable) which
// the Registry wasn't able to recognise until ~ 19th August 2022.
func isRegistryModuleInputRequired(publishTime time.Time, input registry.Input) bool {
	fixTime := time.Date(2022, time.August, 20, 0, 0, 0, 0, time.UTC)
	// Modules published after the date have "nullable" inputs
	// (default = null) ingested as Required=false and Default="null".
	//
	// The same inputs ingested prior to the date make it impossible
	// to distinguish variable with `default = null` and missing default.
	if input.Required && input.Default == "" && publishTime.Before(fixTime) {
		// To avoid false diagnostics, we safely assume the input is optional
		return false
	}
	return input.Required
}

// GetInstalledTerraformVersion obtains "installed" Terraform version
// which can inform what version of core schema to pick.
// It relies on Terraform CLI to run version subcommand and ignores all
// provider related information. We only aim to run this job once.
func GetInstalledTerraformVersion(ctx context.Context, tfVersionStore *state.TerraformVersionStore, path string) error {
	record, err := tfVersionStore.TerraformVersionRecord()
	if err != nil {
		return err
	}

	// Avoid getting version if getting is already in progress or already known
	if record.TerraformVersionState != op.OpStateUnknown && !job.IgnoreState(ctx) {
		return job.StateNotChangedErr{Dir: document.DirHandleFromPath(path)}
	}

	err = tfVersionStore.SetTerraformVersionState(op.OpStateLoading)
	if err != nil {
		return err
	}
	defer tfVersionStore.SetTerraformVersionState(op.OpStateLoaded)

	tfExec, err := TerraformExecutorForModule(ctx, path)
	if err != nil {
		return err
	}

	v, _, err := tfExec.Version(ctx)
	sErr := tfVersionStore.UpdateTerraformVersion(v, err)
	if sErr != nil {
		return sErr
	}

	return err
}
