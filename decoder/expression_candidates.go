package decoder

import (
	"fmt"

	"github.com/hashicorp/hcl-lang/lang"
	"github.com/hashicorp/hcl-lang/schema"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

func (d *Decoder) attrValueCandidatesAtPos(attr *hclsyntax.Attribute, schema *schema.AttributeSchema, pos hcl.Pos) (lang.Candidates, error) {
	constraints, rng := constraintsAtPos(attr.Expr, schema.Expr)
	if len(constraints) > 0 {
		return d.expressionCandidatesAtPos(constraints, rng)
	}
	return lang.ZeroCandidates(), nil
}

func constraintsAtPos(expr hcl.Expression, constraints schema.ExprConstraints) (schema.ExprConstraints, hcl.Range) {
	switch eType := expr.(type) {
	case *hclsyntax.LiteralValueExpr:
		// Only provide candidates if there is no expression
		// i.e. avoid completing middle of expression.
		// This means we also don't need to care about position.
		if !eType.Val.IsWhollyKnown() {
			return constraints, hcl.Range{
				Start:    eType.Range().Start,
				End:      eType.Range().Start,
				Filename: eType.Range().Filename,
			}
		}
	}
	return schema.ExprConstraints{}, expr.Range()
}

func (d *Decoder) expressionCandidatesAtPos(constraints schema.ExprConstraints, editRng hcl.Range) (lang.Candidates, error) {
	candidates := lang.NewCandidates()

	for _, c := range constraints {
		candidates.List = append(candidates.List, constraintToCandidates(c, editRng)...)
	}

	candidates.IsComplete = true
	return candidates, nil
}

func constraintToCandidates(constraint schema.ExprConstraint, editRng hcl.Range) []lang.Candidate {
	candidates := make([]lang.Candidate, 0)

	switch c := constraint.(type) {
	case schema.LiteralTypeExpr:
		candidates = append(candidates, typeToCandidates(c.Type, editRng)...)
	}

	return candidates
}

func typeToCandidates(ofType cty.Type, editRng hcl.Range) []lang.Candidate {
	candidates := make([]lang.Candidate, 0)

	// TODO: Ensure TextEdit is always single-line, otherwise use AdditionalTextEdit
	// See https://github.com/microsoft/language-server-protocol/issues/92

	if ofType == cty.Bool {
		return valuesToCandidates([]cty.Value{
			cty.False,
			cty.True,
		}, editRng)
	}

	if ofType.IsPrimitiveType() {
		// Avoid completing other primitive types
		return candidates
	}

	candidates = append(candidates, lang.Candidate{
		Label:  labelForType(ofType),
		Detail: ofType.FriendlyName(),
		Kind:   lang.LiteralValueCandidateKind,
		TextEdit: lang.TextEdit{
			NewText: newTextForType(ofType),
			Snippet: snippetForAttrType(1, ofType),
			Range:   editRng,
		},
	})

	return candidates
}

func valuesToCandidates(vals []cty.Value, editRng hcl.Range) []lang.Candidate {
	candidates := make([]lang.Candidate, 0)

	for _, val := range vals {
		switch val.Type() {
		case cty.Bool:
			candidates = append(candidates, lang.Candidate{
				Label:       fmt.Sprintf("%t", val.True()),
				Detail:      val.Type().FriendlyName(),
				Description: lang.PlainText(val.Type().FriendlyName()),
				Kind:        lang.LiteralValueCandidateKind,
				TextEdit: lang.TextEdit{
					NewText: fmt.Sprintf("%t", val.True()),
					Snippet: fmt.Sprintf(`${%d:%t}`, 1, val.True()),
					Range:   editRng,
				},
			})
		case cty.String:
			candidates = append(candidates, lang.Candidate{
				Label:       val.AsString(),
				Detail:      val.Type().FriendlyName(),
				Description: lang.PlainText(val.Type().FriendlyName()),
				Kind:        lang.LiteralValueCandidateKind,
				TextEdit: lang.TextEdit{
					NewText: fmt.Sprintf("%q", val.AsString()),
					Snippet: fmt.Sprintf(`${%d:%q}`, 1, val.AsString()),
					Range:   editRng,
				},
			})
		case cty.Number:
			bf := val.AsBigFloat()
			var numberAsStr string
			if bf.IsInt() {
				intNum, _ := bf.Int64()
				numberAsStr = fmt.Sprintf("%d", intNum)
			} else {
				fNum, _ := bf.Float64()
				numberAsStr = fmt.Sprintf("%f", fNum)
			}
			candidates = append(candidates, lang.Candidate{
				Label:       numberAsStr,
				Detail:      val.Type().FriendlyName(),
				Description: lang.PlainText(val.Type().FriendlyName()),
				Kind:        lang.LiteralValueCandidateKind,
				TextEdit: lang.TextEdit{
					NewText: numberAsStr,
					Snippet: fmt.Sprintf(`${%d:%s}`, 1, numberAsStr),
					Range:   editRng,
				},
			})
		}
		// TODO: Support complex types (object, map, list, set, tuple)
	}

	return candidates
}

func snippetForAttrType(placeholder uint, attrType cty.Type) string {
	switch attrType {
	case cty.String:
		return fmt.Sprintf(`"${%d:value}"`, placeholder)
	case cty.Bool:
		return fmt.Sprintf(`${%d:false}`, placeholder)
	case cty.Number:
		return fmt.Sprintf(`${%d:1}`, placeholder)
	case cty.DynamicPseudoType:
		return fmt.Sprintf(`${%d}`, placeholder)
	}

	if attrType.IsMapType() {
		return fmt.Sprintf("{\n"+`  "${1:key}" = %s`+"\n}",
			snippetForAttrType(placeholder+1, *attrType.MapElementType()))
	}

	if attrType.IsListType() || attrType.IsSetType() {
		elType := attrType.ElementType()
		return fmt.Sprintf("[ %s ]", snippetForAttrType(placeholder, elType))
	}

	if attrType.IsObjectType() {
		objSnippet := ""
		for _, name := range sortedObjectAttrNames(attrType) {
			valType := attrType.AttributeType(name)

			objSnippet += fmt.Sprintf("  %s = %s\n", name,
				snippetForAttrType(placeholder, valType))
			placeholder++
		}
		return fmt.Sprintf("{\n%s}", objSnippet)
	}

	if attrType.IsTupleType() {
		elTypes := attrType.TupleElementTypes()
		if len(elTypes) == 1 {
			return fmt.Sprintf("[ %s ]", snippetForAttrType(placeholder, elTypes[0]))
		}

		tupleSnippet := ""
		for _, elType := range elTypes {
			placeholder++
			tupleSnippet += snippetForAttrType(placeholder, elType)
		}
		return fmt.Sprintf("[\n%s]", tupleSnippet)
	}

	return ""
}

func snippetForExprContraints(placeholder uint, ec schema.ExprConstraints) string {
	if len(ec) > 0 {
		expr := ec[0]

		switch et := expr.(type) {
		case schema.LiteralTypeExpr:
			return snippetForAttrType(placeholder, et.Type)
		}
		return ""
	}
	return ""
}

func labelForType(attrType cty.Type) string {
	if attrType.IsMapType() {
		elType := *attrType.MapElementType()
		return fmt.Sprintf(`{ "key" = %s }`,
			labelForType(elType))
	}

	if attrType.IsListType() || attrType.IsSetType() {
		elType := attrType.ElementType()
		return fmt.Sprintf(`[ %s ]`,
			labelForType(elType))
	}

	if attrType.IsTupleType() {
		elTypes := attrType.TupleElementTypes()
		if len(elTypes) > 2 {
			return fmt.Sprintf("[ %s , %s, ... ]",
				labelForType(elTypes[0]),
				labelForType(elTypes[1]))
		}
		if len(elTypes) == 2 {
			return fmt.Sprintf("[ %s , %s ]",
				labelForType(elTypes[0]),
				labelForType(elTypes[1]))
		}
		if len(elTypes) == 1 {
			return fmt.Sprintf("[ %s ]", labelForType(elTypes[0]))
		}
		return "[ ]"
	}

	if attrType.IsObjectType() {
		attrNames := sortedObjectAttrNames(attrType)
		if len(attrNames) > 2 {
			return fmt.Sprintf("{ %s = %s, %s = %s … }",
				attrNames[0],
				labelForType(attrType.AttributeType(attrNames[0])),
				attrNames[1],
				labelForType(attrType.AttributeType(attrNames[1])),
			)
		}
		if len(attrNames) == 2 {
			return fmt.Sprintf("{ %s = %s, %s = %s }",
				attrNames[0],
				labelForType(attrType.AttributeType(attrNames[0])),
				attrNames[1],
				labelForType(attrType.AttributeType(attrNames[1])),
			)
		}
		if len(attrNames) > 1 {
			return fmt.Sprintf("{ %s = %s ... }",
				attrNames[0],
				labelForType(attrType.AttributeType(attrNames[0])),
			)
		}
		if len(attrNames) == 1 {
			return fmt.Sprintf("{ %s = %s }",
				attrNames[0],
				labelForType(attrType.AttributeType(attrNames[0])))
		}
		return "{ }"
	}

	return attrType.FriendlyName()
}

func newTextForType(attrType cty.Type) string {
	switch attrType {
	case cty.String:
		return `""`
	case cty.Bool:
		return `false`
	case cty.Number:
		return `1`
	case cty.DynamicPseudoType:
		return ``
	}

	if attrType.IsMapType() {
		elType := *attrType.MapElementType()
		return fmt.Sprintf("{\n"+`  "key" = %s`+"\n}",
			newTextForType(elType))
	}

	if attrType.IsListType() || attrType.IsSetType() {
		elType := attrType.ElementType()
		return fmt.Sprintf("[ %s ]", newTextForType(elType))
	}

	if attrType.IsObjectType() {
		objSnippet := ""
		attrNames := sortedObjectAttrNames(attrType)
		for _, name := range attrNames {
			valType := attrType.AttributeType(name)

			objSnippet += fmt.Sprintf("  %s = %s\n", name,
				newTextForType(valType))
		}
		return fmt.Sprintf("{\n%s}", objSnippet)
	}

	if attrType.IsTupleType() {
		elTypes := attrType.TupleElementTypes()
		if len(elTypes) == 1 {
			return fmt.Sprintf("[ %s ]", newTextForType(elTypes[0]))
		}

		tupleSnippet := ""
		for _, elType := range elTypes {
			tupleSnippet += newTextForType(elType)
		}
		return fmt.Sprintf("[\n%s]", tupleSnippet)
	}

	return ""
}
