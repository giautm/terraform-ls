// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package decoder

import (
	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl-lang/lang"
	"github.com/hashicorp/hcl-lang/schema"
	"github.com/hashicorp/terraform-ls/internal/flavors/stacks/state"
	"github.com/zclconf/go-cty/cty"
	// tfmodule "github.com/hashicorp/terraform-schema/module"
	// tfschema "github.com/hashicorp/terraform-schema/schema"
)

func schemaForStack(mod *state.StacksRecord, stateReader StateReader) (*schema.BodySchema, error) {

	// TODO: this will come from terraform-schema eventually
	return &schema.BodySchema{
		Blocks: map[string]*schema.BlockSchema{
			"component": componentBlock(nil),
		},
	}, nil
}

func componentBlock(v *version.Version) *schema.BlockSchema {
	return &schema.BlockSchema{
		Description: lang.Markdown("Component represents the declaration of a single component within a particular Terraform Stack. Components are the most important object in a stack configuration, just as resources are the most important object in a Terraform module: each one refers to a Terraform module that describes the infrastructure that the component is 'made of'."),
		Address: &schema.BlockAddrSchema{
			FriendlyName: "component",
			// ScopeId:      refscope.ModuleScope,
			AsReference:  true,
			Steps: []schema.AddrStep{
				schema.StaticStep{Name: "component"},
				schema.LabelStep{Index: 0},
			},
		},
		// SemanticTokenModifiers: lang.SemanticTokenModifiers{tokmod.Module},
		Labels: []*schema.LabelSchema{
			{
				Name:                   "name",
				// SemanticTokenModifiers: lang.SemanticTokenModifiers{tokmod.Name},
				Description:            lang.PlainText("Component Name"),
			},
		},
		Body: &schema.BodySchema{
			Attributes: map[string]*schema.AttributeSchema{
				"source": {
					Description:            lang.Markdown("The Terraform module location to load the Component from, a local directory (e.g. `./module`)"),
					IsRequired:             true,
					IsDepKey:               true,
					Constraint:             schema.LiteralType{Type: cty.String},
					SemanticTokenModifiers: lang.SemanticTokenModifiers{lang.TokenModifierDependent},
					CompletionHooks: lang.CompletionHooks{
						{Name: "CompleteLocalModuleSources"},
					},
				},
				"inputs": {
					Description: lang.Markdown("A mapping of module input variable names to values. The keys of this map must correspond to the Terraform variable names in the module defined by source. The values can be one of three types:" +
						"A variable reference, denoted by var.variable_name;" +
						"A component output, denoted by component.component_name.output_name;" +
						"A literal value, e.g. 'string value', or 1234, or any other valid HCL value"),
					IsOptional: true,
					Constraint: schema.Map{
						Name: "map of input references",
						// Elem: schema.Reference{OfScopeId: refscope.ProviderScope},
					},
				},
				"providers": {
					Description: lang.Markdown(" A mapping of provider names to providers declared in the stack configuration. Providers must be declared in the top level of the stack and passed into each module in the stack. Modules cannot configure their own providers"),
					IsOptional:  true,
					Constraint: schema.Map{
						Name: "map of provider references",
						// Elem: schema.Reference{OfScopeId: refscope.ProviderScope},
					},
				},
			},
		},
	}
}
