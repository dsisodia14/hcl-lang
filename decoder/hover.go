package decoder

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl-lang/lang"
	"github.com/hashicorp/hcl-lang/schema"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

func (d *Decoder) HoverAtPos(filename string, pos hcl.Pos) (*lang.HoverData, error) {
	f, err := d.fileByName(filename)
	if err != nil {
		return nil, err
	}

	rootBody, err := d.bodyForFileAndPos(filename, f, pos)
	if err != nil {
		return nil, err
	}

	d.rootSchemaMu.RLock()
	defer d.rootSchemaMu.RUnlock()

	if d.rootSchema == nil {
		return nil, &NoSchemaError{}
	}

	data, err := d.hoverAtPos(rootBody, d.rootSchema, pos)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func (d *Decoder) hoverAtPos(body *hclsyntax.Body, bodySchema *schema.BodySchema, pos hcl.Pos) (*lang.HoverData, error) {
	if bodySchema == nil {
		return nil, nil
	}

	filename := body.Range().Filename

	for name, attr := range body.Attributes {
		if attr.Range().ContainsPos(pos) {
			aSchema, ok := bodySchema.Attributes[attr.Name]
			if !ok {
				if bodySchema.AnyAttribute == nil {
					return nil, &PositionalError{
						Filename: filename,
						Pos:      pos,
						Msg:      fmt.Sprintf("unknown attribute %q", attr.Name),
					}
				}
				aSchema = bodySchema.AnyAttribute
			}

			if attr.NameRange.ContainsPos(pos) {
				return &lang.HoverData{
					Content: hoverContentForAttribute(name, aSchema),
					Range:   attr.Range(),
				}, nil
			}

			if attr.Expr.Range().ContainsPos(pos) {
				content, err := hoverContentForExpr(attr.Expr, aSchema.Expr)
				if err != nil {
					return nil, &PositionalError{
						Filename: filename,
						Pos:      pos,
						Msg:      err.Error(),
					}
				}
				return &lang.HoverData{
					Content: content,
					Range:   attr.Expr.Range(),
				}, nil
			}
		}
	}

	for _, block := range body.Blocks {
		if block.Range().ContainsPos(pos) {
			bSchema, ok := bodySchema.Blocks[block.Type]
			if !ok {
				return nil, &PositionalError{
					Filename: filename,
					Pos:      pos,
					Msg:      fmt.Sprintf("unknown block type %q", block.Type),
				}
			}

			if block.TypeRange.ContainsPos(pos) {
				return &lang.HoverData{
					Content: hoverContentForBlock(block.Type, bSchema),
					Range:   block.TypeRange,
				}, nil
			}

			for i, labelRange := range block.LabelRanges {
				if labelRange.ContainsPos(pos) {
					if i+1 > len(bSchema.Labels) {
						return nil, &PositionalError{
							Filename: filename,
							Pos:      pos,
							Msg:      fmt.Sprintf("unexpected label (%d) %q", i, block.Labels[i]),
						}
					}

					return &lang.HoverData{
						Content: d.hoverContentForLabel(i, block, bSchema),
						Range:   labelRange,
					}, nil
				}
			}

			if isPosOutsideBody(block, pos) {
				return nil, &PositionalError{
					Filename: filename,
					Pos:      pos,
					Msg:      fmt.Sprintf("position outside of %q body", block.Type),
				}
			}

			if block.Body != nil && block.Body.Range().ContainsPos(pos) {
				mergedSchema, err := mergeBlockBodySchemas(block, bSchema)
				if err != nil {
					return nil, err
				}

				return d.hoverAtPos(block.Body, mergedSchema, pos)
			}
		}
	}

	// Position outside of any attribute or block
	return nil, &PositionalError{
		Filename: filename,
		Pos:      pos,
		Msg:      "position outside of any attribute name, value or block",
	}
}

func (d *Decoder) hoverContentForLabel(i int, block *hclsyntax.Block, bSchema *schema.BlockSchema) lang.MarkupContent {
	value := block.Labels[i]
	labelSchema := bSchema.Labels[i]

	if labelSchema.IsDepKey {
		dk := dependencyKeysFromBlock(block, bSchema)
		bs, ok := bSchema.DependentBodySchema(dk)
		if ok {
			content := fmt.Sprintf("`%s`", value)
			if bs.Detail != "" {
				content += " " + bs.Detail
			} else if labelSchema.Name != "" {
				content += " " + labelSchema.Name
			}
			if bs.Description.Value != "" {
				content += "\n\n" + bs.Description.Value
			} else if labelSchema.Description.Value != "" {
				content += "\n\n" + labelSchema.Description.Value
			}

			if bs.DocsLink != nil {
				link := bs.DocsLink
				u, err := d.docsURL(link.URL, "documentHover")
				if err == nil {
					content += fmt.Sprintf("\n\n[`%s` on %s](%s)",
						value, u.Hostname(), u.String())
				}
			}

			return lang.Markdown(content)
		}
	}

	content := fmt.Sprintf("%q", value)
	if labelSchema.Name != "" {
		content += fmt.Sprintf(" (%s)", labelSchema.Name)
	}
	content = strings.TrimSpace(content)
	if labelSchema.Description.Value != "" {
		content += "\n\n" + labelSchema.Description.Value
	}

	return lang.Markdown(content)
}

func hoverContentForAttribute(name string, schema *schema.AttributeSchema) lang.MarkupContent {
	value := fmt.Sprintf("**%s** _%s_", name, detailForAttribute(schema))
	if schema.Description.Value != "" {
		value += fmt.Sprintf("\n\n%s", schema.Description.Value)
	}
	return lang.MarkupContent{
		Kind:  lang.MarkdownKind,
		Value: value,
	}
}

func hoverContentForBlock(bType string, schema *schema.BlockSchema) lang.MarkupContent {
	value := fmt.Sprintf("**%s** _%s_", bType, detailForBlock(schema))
	if schema.Description.Value != "" {
		value += fmt.Sprintf("\n\n%s", schema.Description.Value)
	}
	return lang.MarkupContent{
		Kind:  lang.MarkdownKind,
		Value: value,
	}
}

func hoverContentForExpr(expr hcl.Expression, constraints schema.ExprConstraints) (lang.MarkupContent, error) {
	switch e := expr.(type) {
	case *hclsyntax.TemplateExpr:
		if len(e.Parts) == 1 {
			return hoverContentForExpr(e.Parts[0], constraints)
		}
	case *hclsyntax.TemplateWrapExpr:
		return hoverContentForExpr(e.Wrapped, constraints)
	case *hclsyntax.TupleConsExpr:
		listLve, ok := listTypeLiteralConstraint(constraints)
		if ok {
			return hoverContentForValueAndType(cty.NullVal(listLve.Type), listLve.Type)
		}
		setLve, ok := setTypeLiteralConstraint(constraints)
		if ok {
			return hoverContentForValueAndType(cty.NullVal(setLve.Type), setLve.Type)
		}
		tupleLve, ok := tupleTypeLiteralConstraint(constraints)
		if ok {
			return hoverContentForValueAndType(cty.NullVal(tupleLve.Type), tupleLve.Type)
		}
	case *hclsyntax.ObjectConsExpr:
		objLve, ok := objectTypeLiteralConstraint(constraints)
		if ok {
			return hoverContentForValueAndType(cty.NullVal(objLve.Type), objLve.Type)
		}
		mapLve, ok := mapTypeLiteralConstraint(constraints)
		if ok {
			return hoverContentForValueAndType(cty.NullVal(mapLve.Type), mapLve.Type)
		}
	case *hclsyntax.LiteralValueExpr:
		lve, ok := literalExprForType(e.Val.Type(), constraints)
		if !ok {
			// incompatible/unknown literal type
			return lang.MarkupContent{}, fmt.Errorf("no schema for literal type %q",
				e.Val.Type().FriendlyName())
		}

		return hoverContentForValueAndType(e.Val, lve.Type)
	}

	return lang.MarkupContent{}, fmt.Errorf("unsupported expression (%T)", expr)
}

func hoverContentForValueAndType(val cty.Value, t cty.Type) (lang.MarkupContent, error) {
	if t.IsPrimitiveType() {
		var value string
		switch t {
		case cty.String:
			value = fmt.Sprintf("`%s`", val.AsString())
		case cty.Bool:
			value = fmt.Sprintf("`%t`", val.True())
		case cty.Number:
			value = fmt.Sprintf("`%s`", val.AsBigFloat().String())
		}

		value += fmt.Sprintf(` _%s_`, t.FriendlyName())

		return lang.MarkupContent{
			Kind:  lang.MarkdownKind,
			Value: value,
		}, nil
	}

	if t.IsObjectType() {
		attrNames := sortedObjectAttrNames(t)
		if len(attrNames) == 0 {
			return lang.MarkupContent{
				Kind:  lang.MarkdownKind,
				Value: t.FriendlyName(),
			}, nil
		}
		value := "```\n{\n"
		for _, name := range attrNames {
			valType := t.AttributeType(name)
			value += fmt.Sprintf("  %s = %s\n", name,
				valType.FriendlyName())
		}
		value += "}\n```\n_object_"

		return lang.MarkupContent{
			Kind:  lang.MarkdownKind,
			Value: value,
		}, nil
	}

	if t.IsMapType() || t.IsListType() || t.IsSetType() || t.IsTupleType() {
		value := fmt.Sprintf(`_%s_`, t.FriendlyName())
		return lang.MarkupContent{
			Kind:  lang.MarkdownKind,
			Value: value,
		}, nil
	}

	return lang.MarkupContent{}, fmt.Errorf("unsupported type: %q", t.FriendlyName())
}

func listTypeLiteralConstraint(constraints schema.ExprConstraints) (schema.LiteralTypeExpr, bool) {
	for _, c := range constraints {
		if lve, ok := c.(schema.LiteralTypeExpr); ok && lve.Type.IsListType() {
			return lve, true
		}
	}
	return schema.LiteralTypeExpr{}, false
}

func setTypeLiteralConstraint(constraints schema.ExprConstraints) (schema.LiteralTypeExpr, bool) {
	for _, c := range constraints {
		if lve, ok := c.(schema.LiteralTypeExpr); ok && lve.Type.IsSetType() {
			return lve, true
		}
	}
	return schema.LiteralTypeExpr{}, false
}

func tupleTypeLiteralConstraint(constraints schema.ExprConstraints) (schema.LiteralTypeExpr, bool) {
	for _, c := range constraints {
		if lve, ok := c.(schema.LiteralTypeExpr); ok && lve.Type.IsTupleType() {
			return lve, true
		}
	}
	return schema.LiteralTypeExpr{}, false
}

func objectTypeLiteralConstraint(constraints schema.ExprConstraints) (schema.LiteralTypeExpr, bool) {
	for _, c := range constraints {
		if lve, ok := c.(schema.LiteralTypeExpr); ok && lve.Type.IsObjectType() {
			return lve, true
		}
	}
	return schema.LiteralTypeExpr{}, false
}

func mapTypeLiteralConstraint(constraints schema.ExprConstraints) (schema.LiteralTypeExpr, bool) {
	for _, c := range constraints {
		if lve, ok := c.(schema.LiteralTypeExpr); ok && lve.Type.IsMapType() {
			return lve, true
		}
	}
	return schema.LiteralTypeExpr{}, false
}

func literalExprForType(exprType cty.Type, constraints schema.ExprConstraints) (schema.LiteralTypeExpr, bool) {
	for _, c := range constraints {
		if lve, ok := c.(schema.LiteralTypeExpr); ok && lve.Type.Equals(exprType) {
			return lve, true
		}
	}
	return schema.LiteralTypeExpr{}, false
}
