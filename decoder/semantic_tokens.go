package decoder

import (
	"sort"

	"github.com/hashicorp/hcl-lang/lang"
	"github.com/hashicorp/hcl-lang/schema"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// SemanticTokensInFile returns a sequence of semantic tokens
// within the config file.
func (d *Decoder) SemanticTokensInFile(filename string) ([]lang.SemanticToken, error) {
	f, err := d.fileByName(filename)
	if err != nil {
		return nil, err
	}

	body, err := d.bodyForFileAndPos(filename, f, hcl.InitialPos)
	if err != nil {
		return nil, err
	}

	if d.rootSchema == nil {
		return []lang.SemanticToken{}, nil
	}

	tokens := tokensForBody(body, d.rootSchema, false)

	sort.Slice(tokens, func(i, j int) bool {
		return tokens[i].Range.Start.Byte < tokens[j].Range.Start.Byte
	})

	return tokens, nil
}

func tokensForBody(body *hclsyntax.Body, bodySchema *schema.BodySchema, isDependent bool) []lang.SemanticToken {
	tokens := make([]lang.SemanticToken, 0)

	if bodySchema == nil {
		return tokens
	}

	for name, attr := range body.Attributes {
		attrSchema, ok := bodySchema.Attributes[name]
		if !ok {
			if bodySchema.AnyAttribute == nil {
				// unknown attribute
				continue
			}
			attrSchema = bodySchema.AnyAttribute
		}

		modifiers := make([]lang.SemanticTokenModifier, 0)
		if isDependent {
			modifiers = append(modifiers, lang.TokenModifierDependent)
		}
		if attrSchema.IsDeprecated {
			modifiers = append(modifiers, lang.TokenModifierDeprecated)
		}

		tokens = append(tokens, lang.SemanticToken{
			Type:      lang.TokenAttrName,
			Modifiers: modifiers,
			Range:     attr.NameRange,
		})

		tokens = append(tokens, tokensForConstrainedExpression(attr.Expr, attrSchema.Expr)...)
	}

	for _, block := range body.Blocks {
		blockSchema, ok := bodySchema.Blocks[block.Type]
		if !ok {
			// unknown block
			continue
		}

		modifiers := make([]lang.SemanticTokenModifier, 0)
		if isDependent {
			modifiers = append(modifiers, lang.TokenModifierDependent)
		}
		if blockSchema.IsDeprecated {
			modifiers = append(modifiers, lang.TokenModifierDeprecated)
		}

		tokens = append(tokens, lang.SemanticToken{
			Type:      lang.TokenBlockType,
			Modifiers: modifiers,
			Range:     block.TypeRange,
		})

		for i, labelRange := range block.LabelRanges {
			if i+1 > len(blockSchema.Labels) {
				// unknown label
				continue
			}

			labelSchema := blockSchema.Labels[i]

			modifiers := make([]lang.SemanticTokenModifier, 0)
			if labelSchema.IsDepKey {
				modifiers = append(modifiers, lang.TokenModifierDependent)
			}

			tokens = append(tokens, lang.SemanticToken{
				Type:      lang.TokenBlockLabel,
				Modifiers: modifiers,
				Range:     labelRange,
			})
		}

		if block.Body != nil {
			tokens = append(tokens, tokensForBody(block.Body, blockSchema.Body, false)...)
		}

		dk := dependencyKeysFromBlock(block, blockSchema)
		depSchema, ok := blockSchema.DependentBodySchema(dk)
		if ok {
			tokens = append(tokens, tokensForBody(block.Body, depSchema, true)...)
		}
	}

	return tokens
}

func tokensForConstrainedExpression(expr hclsyntax.Expression, constraints schema.ExprConstraints) []lang.SemanticToken {
	tokens := make([]lang.SemanticToken, 0)

	switch eType := expr.(type) {
	case *hclsyntax.TemplateExpr:
		if len(eType.Parts) == 1 {
			return tokensForConstrainedExpression(eType.Parts[0], constraints)
		}
	case *hclsyntax.TemplateWrapExpr:
		return tokensForConstrainedExpression(eType.Wrapped, constraints)
	case *hclsyntax.TupleConsExpr:
		listLve, ok := listTypeLiteralConstraint(constraints)
		if ok {
			return tokensForTupleConsExpr(eType, listLve.Type)
		}
		setLve, ok := setTypeLiteralConstraint(constraints)
		if ok {
			return tokensForTupleConsExpr(eType, setLve.Type)
		}
		tupleLve, ok := tupleTypeLiteralConstraint(constraints)
		if ok {
			return tokensForTupleConsExpr(eType, tupleLve.Type)
		}
	case *hclsyntax.ObjectConsExpr:
		objLve, ok := objectTypeLiteralConstraint(constraints)
		if ok {
			return tokensForObjectConsExpr(eType, objLve.Type)
		}
		mapLve, ok := mapTypeLiteralConstraint(constraints)
		if ok {
			return tokensForObjectConsExpr(eType, mapLve.Type)
		}
	case *hclsyntax.LiteralValueExpr:
		valType := eType.Val.Type()
		_, ok := literalExprForType(valType, constraints)
		if !ok {
			// incompatible/unknown literal type
			return []lang.SemanticToken{}
		}
		return tokenForTypedExpression(eType, valType)
	}
	return tokens
}

func tokenForTypedExpression(expr hclsyntax.Expression, valType cty.Type) []lang.SemanticToken {
	switch eType := expr.(type) {
	case *hclsyntax.LiteralValueExpr:
		if valType.IsPrimitiveType() {
			return tokensForLiteralValueExpr(eType, valType)
		}
	case *hclsyntax.ObjectConsExpr:
		return tokensForObjectConsExpr(eType, valType)
	case *hclsyntax.TupleConsExpr:
		return tokensForTupleConsExpr(eType, valType)
	}

	return []lang.SemanticToken{}
}

func tokensForLiteralValueExpr(expr *hclsyntax.LiteralValueExpr, valType cty.Type) []lang.SemanticToken {
	tokens := make([]lang.SemanticToken, 0)

	switch valType {
	case cty.Bool:
		tokens = append(tokens, lang.SemanticToken{
			Type:      lang.TokenBool,
			Modifiers: []lang.SemanticTokenModifier{},
			Range:     expr.Range(),
		})
	case cty.String:
		tokens = append(tokens, lang.SemanticToken{
			Type:      lang.TokenString,
			Modifiers: []lang.SemanticTokenModifier{},
			Range:     expr.Range(),
		})
	case cty.Number:
		tokens = append(tokens, lang.SemanticToken{
			Type:      lang.TokenNumber,
			Modifiers: []lang.SemanticTokenModifier{},
			Range:     expr.Range(),
		})
	}

	return tokens
}

func tokensForObjectConsExpr(expr *hclsyntax.ObjectConsExpr, exprType cty.Type) []lang.SemanticToken {
	tokens := make([]lang.SemanticToken, 0)

	if exprType.IsObjectType() {
		attrTypes := exprType.AttributeTypes()
		for _, item := range expr.Items {
			key, _ := item.KeyExpr.Value(nil)
			if key.IsWhollyKnown() && key.Type() == cty.String {
				valType, ok := attrTypes[key.AsString()]
				if !ok {
					// unknown attribute
					continue
				}
				tokens = append(tokens, lang.SemanticToken{
					Type:      lang.TokenObjectKey,
					Modifiers: []lang.SemanticTokenModifier{},
					Range:     item.KeyExpr.Range(),
				})
				tokens = append(tokens, tokenForTypedExpression(item.ValueExpr, valType)...)
			}
		}
	}
	if exprType.IsMapType() {
		elemType := *exprType.MapElementType()
		for _, item := range expr.Items {
			tokens = append(tokens, lang.SemanticToken{
				Type:      lang.TokenMapKey,
				Modifiers: []lang.SemanticTokenModifier{},
				Range:     item.KeyExpr.Range(),
			})
			tokens = append(tokens, tokenForTypedExpression(item.ValueExpr, elemType)...)
		}
	}

	return tokens
}

func tokensForTupleConsExpr(expr *hclsyntax.TupleConsExpr, exprType cty.Type) []lang.SemanticToken {
	tokens := make([]lang.SemanticToken, 0)

	for i, e := range expr.Exprs {
		var elemType cty.Type
		if exprType.IsListType() {
			elemType = *exprType.ListElementType()
		}
		if exprType.IsSetType() {
			elemType = *exprType.SetElementType()
		}
		if exprType.IsTupleType() {
			elemType = exprType.TupleElementType(i)
		}

		tokens = append(tokens, tokenForTypedExpression(e, elemType)...)
	}

	return tokens
}
